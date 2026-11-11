/**
 * Read-only eth_calls through a viem PublicClient: the settler and core state a maker checks before and while quoting.
 * No method here signs or sends anything. Mirrors hedgehood_maker.rpc.
 */

import { type Address, createPublicClient, decodeAbiParameters, type Hex, http, type PublicClient } from "viem";
import { quoteValues, seriesId, seriesValues } from "./eip712.js";
import { address, type Quote, type Series, ValidationError } from "./models.js";
import type { Network } from "./networks.js";
import * as S from "./settler.js";

export class RPCError extends Error {
  override name = "RPCError";
}

/** The subset of a viem PublicClient the reader uses. */
export type ReadClient = Pick<PublicClient, "call" | "getChainId">;

export interface ReaderOptions {
  /** A viem PublicClient (any transport); or give `rpcUrl` to have one made over HTTP. */
  client?: ReadClient;
  rpcUrl?: string;
  settler: string;
  core?: string;
  /** HTTP timeout when the reader makes its own client, ms. */
  timeoutMs?: number;
}

/** eth_call reads of one deployment. */
export class Reader {
  readonly client: ReadClient;
  readonly settler: Address;
  readonly core: Address | undefined;

  constructor(opts: ReaderOptions) {
    if (opts.client) this.client = opts.client;
    else if (opts.rpcUrl)
      this.client = createPublicClient({ transport: http(opts.rpcUrl, { timeout: opts.timeoutMs ?? 10_000 }) });
    else throw new ValidationError("give a viem client or an rpcUrl");
    this.settler = address(opts.settler, "settler");
    this.core = opts.core ? address(opts.core, "core") : undefined;
  }

  static forNetwork(n: Network, opts: { client?: ReadClient; rpcUrl?: string; timeoutMs?: number } = {}): Reader {
    return new Reader({ ...opts, rpcUrl: opts.rpcUrl ?? n.rpc, settler: n.settler, core: n.core });
  }

  async call(to: string, signature: string, args: readonly unknown[], returns: readonly string[]): Promise<unknown[]> {
    const data = S.calldata(signature, ...args);
    let raw: Hex | undefined;
    try {
      raw = (await this.client.call({ to: address(to, "to"), data })).data;
    } catch (e) {
      throw new RPCError(`${signature}: ${e instanceof Error ? e.message : String(e)}`, { cause: e });
    }
    if (!raw || raw === "0x") throw new RPCError(`${signature}: empty result (no contract at ${to}?)`);
    return [...decodeAbiParameters(S.abiParams(returns), raw)];
  }

  private async one<T>(to: string, signature: string, args: readonly unknown[], ret: string): Promise<T> {
    return (await this.call(to, signature, args, [ret]))[0] as T;
  }

  private requireCore(): Address {
    if (!this.core) throw new ValidationError("core address not set");
    return this.core;
  }

  async chainId(): Promise<number> {
    return this.client.getChainId();
  }

  // -------------------------------------------------- settler

  epoch(maker: string): Promise<bigint> {
    return this.one(this.settler, S.EPOCH, [address(maker, "maker")], "uint256");
  }

  signerExpiry(maker: string, signer: string): Promise<bigint> {
    return this.one(this.settler, S.SIGNER_EXPIRY, [address(maker, "maker"), address(signer, "signer")], "uint64");
  }

  nonceUsed(maker: string, nonce: bigint | number): Promise<boolean> {
    return this.one(this.settler, S.NONCE_USED, [address(maker, "maker"), BigInt(nonce)], "bool");
  }

  settlerBalance(token: string, account: string): Promise<bigint> {
    return this.one(this.settler, S.SETTLER_BALANCE, [address(token, "token"), address(account, "account")], "uint256");
  }

  /** {amount, availableAt} of the pending withdrawal. */
  async pendingWithdraw(token: string, account: string): Promise<{ amount: bigint; availableAt: bigint }> {
    const [amount, availableAt] = (await this.call(
      this.settler,
      S.PENDING_WITHDRAW,
      [address(token, "token"), address(account, "account")],
      ["uint256", "uint256"],
    )) as [bigint, bigint];
    return { amount, availableAt };
  }

  linked(account: string, maker: string): Promise<boolean> {
    return this.one(this.settler, S.LINKED, [address(account, "account"), address(maker, "maker")], "bool");
  }

  /** {feeBps, notionalFeeBps}. */
  async fees(): Promise<{ feeBps: bigint; notionalFeeBps: bigint }> {
    const [feeBps, notionalFeeBps] = await Promise.all([
      this.one<bigint>(this.settler, S.FEE_BPS, [], "uint256"),
      this.one<bigint>(this.settler, S.NOTIONAL_FEE_BPS, [], "uint256"),
    ]);
    return { feeBps, notionalFeeBps };
  }

  /** The settler's own digest: compare with eip712.quoteDigest to catch domain or schema drift. */
  async quoteDigest(q: Quote): Promise<Hex> {
    return (
      (await this.one<Hex>(this.settler, S.QUOTE_DIGEST, [quoteValues(q)], "bytes32")) as string
    ).toLowerCase() as Hex;
  }

  async previewFill(q: Quote, amount: bigint | number): Promise<{ premium: bigint; fee: bigint }> {
    const [premium, fee] = (await this.call(
      this.settler,
      S.PREVIEW_FILL,
      [quoteValues(q), BigInt(amount)],
      ["uint256", "uint256"],
    )) as [bigint, bigint];
    return { premium, fee };
  }

  isListed(series: Series): Promise<boolean> {
    return this.one(this.settler, S.IS_LISTED, [seriesValues(series)], "bool");
  }

  // -------------------------------------------------- core and tokens

  isOperator(owner: string, spender?: string): Promise<boolean> {
    return this.one(
      this.requireCore(),
      S.IS_OPERATOR,
      [address(owner, "owner"), address(spender ?? this.settler, "spender")],
      "bool",
    );
  }

  longBalance(owner: string, seriesId: bigint): Promise<bigint> {
    return this.one(this.requireCore(), S.LONG_BALANCE, [address(owner, "owner"), seriesId], "uint256");
  }

  /**
   * Why the settler could not deliver `amount` of `series` from `maker` (null: it can). The gateway rejects a close
   * quote it cannot fill (`inventory`): check before quoting one. The settler moves the long as the maker's
   * OptionsCore operator (`setOperator`, the SDK's setup path).
   */
  async canSell(maker: string, series: Series, amount: bigint): Promise<string | null> {
    const held = await this.longBalance(maker, seriesId(series));
    if (held < amount) return `holds ${held} of ${amount}`;
    if (!(await this.isOperator(maker))) return "the settler is not the maker's operator on OptionsCore (setOperator)";
    return null;
  }

  tokenBalance(token: string, account: string): Promise<bigint> {
    return this.one(address(token, "token"), S.ERC20_BALANCE, [address(account, "account")], "uint256");
  }

  allowance(token: string, owner: string, spender?: string): Promise<bigint> {
    return this.one(
      address(token, "token"),
      S.ALLOWANCE,
      [address(owner, "owner"), address(spender ?? this.settler, "spender")],
      "uint256",
    );
  }
}
