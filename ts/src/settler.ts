/**
 * Calldata and UNSIGNED transactions for the maker's on-chain setup. Nothing here signs or sends: hand the result to
 * your own wallet, keystore or multisig (`castCommand` also renders each one as a `cast send` line to run yourself).
 * Mirrors hedgehood_maker.settler.
 *
 * Onboarding, in order (docs/MAKER_GUIDE.md):
 *
 * 1. `setSigner(hotKey, expiresAt)` from the maker: lets a hot key sign quotes and log in for the maker.
 * 2. Funding, either
 *    a. settler balance: `approve(usdg, amount)` + `deposit(usdg, maker, amount)`, or
 *    b. wallet by allowance: `approve(usdg, amount)` from the paying wallet, plus `linkMaker(maker, true)` from that
 *       wallet when it is not the maker (quotes then carry externalAccount = that wallet).
 * 3. For MakerSells quotes (selling longs you hold): `setOperator()` on OptionsCore.
 */

import { type Address, concat, encodeAbiParameters, type Hex, keccak256, parseAbiParameters, stringToHex } from "viem";
import { QUOTE_TYPES, quoteValues, SERIES_TYPES, seriesId, seriesValues } from "./eip712.js";
import { address, type Quote, type Series, uint, ValidationError } from "./models.js";
import type { Network } from "./networks.js";

export const SERIES_TUPLE = `(${SERIES_TYPES.join(",")})`;
export const QUOTE_TUPLE = `(${QUOTE_TYPES.map((t) => (t === "SeriesParams" ? SERIES_TUPLE : t)).join(",")})`;

// RfqSettler
export const SET_SIGNER = "setSigner(address,uint64)";
export const DEPOSIT = "deposit(address,address,uint256)";
export const REQUEST_WITHDRAW = "requestWithdraw(address,uint256)";
export const WITHDRAW = "withdraw(address,address)";
export const INCREASE_EPOCH = "increaseEpoch(address)";
export const INVALIDATE_NONCES = "invalidateNonces(address,uint256,uint256)";
export const LINK_MAKER = "linkMaker(address,bool)";
export const FILL = `fill(${QUOTE_TUPLE},bytes,uint256)`;
export const CLOSE = `close(${QUOTE_TUPLE},bytes,uint256)`;
// RfqSettler views
export const EPOCH = "epoch(address)";
export const SIGNER_EXPIRY = "signerExpiry(address,address)";
export const NONCE_USED = "nonceUsed(address,uint256)";
/** (token, account) */
export const SETTLER_BALANCE = "balanceOf(address,address)";
export const PENDING_WITHDRAW = "pendingWithdraw(address,address)";
/** (account, maker) */
export const LINKED = "linked(address,address)";
export const FEE_BPS = "feeBps()";
export const NOTIONAL_FEE_BPS = "notionalFeeBps()";
export const QUOTE_DIGEST = `quoteDigest(${QUOTE_TUPLE})`;
export const PREVIEW_FILL = `previewFill(${QUOTE_TUPLE},uint256)`;
export const IS_LISTED = `isListed(${SERIES_TUPLE})`;
// OptionsCore (ERC-6909 longs)
export const SET_OPERATOR = "setOperator(address,bool)";
/** (owner, spender) */
export const IS_OPERATOR = "isOperator(address,address)";
/** (owner, id) */
export const LONG_BALANCE = "balanceOf(address,uint256)";
export const REDEEM_LONG = `redeemLong(${SERIES_TUPLE},uint256,address,address)`;
// ERC-20
export const APPROVE = "approve(address,uint256)";
export const ERC20_BALANCE = "balanceOf(address)";
export const ALLOWANCE = "allowance(address,address)";

/** The 4-byte selector of a function signature, 0x hex. */
export function selector(signature: string): Hex {
  return keccak256(stringToHex(signature)).slice(0, 10) as Hex;
}

/** Top-level argument types of a function signature, tuples kept whole. */
export function argTypes(signature: string): string[] {
  const inner = signature.slice(signature.indexOf("(") + 1, -1);
  const out: string[] = [];
  let depth = 0;
  let cur = "";
  for (const ch of inner) {
    if (ch === "," && depth === 0) {
      out.push(cur);
      cur = "";
      continue;
    }
    if (ch === "(") depth++;
    if (ch === ")") depth--;
    cur += ch;
  }
  if (cur) out.push(cur);
  return out;
}

/** The ABI parameters of a function signature's arguments (for encodeAbiParameters / decodeAbiParameters). */
export function abiParams(types: readonly string[]) {
  return types.length ? parseAbiParameters(types.join(",")) : [];
}

/** 0x calldata of `signature(args...)`. Tuples are passed as arrays. */
export function calldata(signature: string, ...args: readonly unknown[]): Hex {
  const params = abiParams(argTypes(signature));
  return concat([selector(signature), encodeAbiParameters(params, args as unknown[])]);
}

/** RfqSettler.fill (or close) calldata: what the taker sends (the gateway returns it as fillCalldata). */
export function encodeFill(q: Quote, signature: Hex, amount: bigint | number, close = false): Hex {
  return calldata(close ? CLOSE : FILL, quoteValues(q), signature, BigInt(amount));
}

/** A transaction to review, sign and send yourself. `from` is who must send it (when known). */
export interface UnsignedTx {
  readonly chainId: number;
  readonly to: Address;
  readonly data: Hex;
  readonly value: bigint;
  readonly from?: Address;
  readonly description: string;
  readonly function: string;
  readonly args: readonly unknown[];
}

function castArg(v: unknown): string {
  if (typeof v === "boolean") return v ? "true" : "false";
  if (Array.isArray(v)) return `(${v.map(castArg).join(",")})`;
  return String(v);
}

/** The equivalent Foundry command, to run yourself with your own key or keystore. */
export function castCommand(tx: UnsignedTx, rpc = "$RPC"): string {
  const args = tx.args.map((a) => `"${castArg(a)}"`).join(" ");
  return `cast send ${tx.to} "${tx.function}" ${args} --rpc-url ${rpc} --account <your-keystore>`.replaceAll("  ", " ");
}

/** The fields a wallet needs (add nonce and gas, then sign with your own key). */
export function txRequest(tx: UnsignedTx): { chainId: number; to: Address; data: Hex; value: bigint; from?: Address } {
  const out: { chainId: number; to: Address; data: Hex; value: bigint; from?: Address } = {
    chainId: tx.chainId,
    to: tx.to,
    data: tx.data,
    value: tx.value,
  };
  if (tx.from) out.from = tx.from;
  return out;
}

function hex(v: bigint): string {
  return `0x${v.toString(16)}`;
}

/** Builders for one deployment's settler and core. */
export class SettlerTxs {
  readonly chainId: number;
  readonly settler: Address;
  readonly core: Address | undefined;

  constructor(chainId: number, settler: string, core?: string) {
    this.chainId = chainId;
    this.settler = address(settler, "settler");
    this.core = core ? address(core, "core") : undefined;
  }

  static forNetwork(n: Network): SettlerTxs {
    return new SettlerTxs(n.chainId, n.settler, n.core);
  }

  private tx(to: Address, fn: string, args: readonly unknown[], description: string, from?: string): UnsignedTx {
    const t: UnsignedTx = {
      chainId: this.chainId,
      to,
      data: calldata(fn, ...args),
      value: 0n,
      description,
      function: fn,
      args,
      ...(from ? { from: address(from, "from") } : {}),
    };
    return Object.freeze(t);
  }

  private requireCore(): Address {
    if (!this.core) throw new ValidationError("core address not set");
    return this.core;
  }

  // -------------------------------------------------- settler

  /**
   * From the maker: `signer` may sign quotes (and log in) for it until `expiresAt` (unix s); 0 revokes. A quote is
   * accepted only while the signer outlives its validUntil: rotate before expiry.
   */
  setSigner(signer: string, expiresAt: bigint | number, opts: { from?: string } = {}): UnsignedTx {
    const s = address(signer, "signer");
    const e = uint(expiresAt, "expiresAt", 64);
    return this.tx(this.settler, SET_SIGNER, [s, e], `setSigner(${s}, ${e})${e === 0n ? " -- revoke" : ""}`, opts.from);
  }

  /** Credits `account`'s settler balance (anyone may deposit for anyone; approve the settler first). */
  deposit(token: string, account: string, amount: bigint | number | string, opts: { from?: string } = {}): UnsignedTx {
    const t = address(token, "token");
    const a = address(account, "account");
    const v = uint(amount, "amount");
    return this.tx(this.settler, DEPOSIT, [t, a, v], `deposit ${v} raw of ${t} for ${a}`, opts.from);
  }

  /** Starts the 5 min delay; the requested amount keeps backing fills until `withdraw`. */
  requestWithdraw(token: string, amount: bigint | number | string, opts: { from?: string } = {}): UnsignedTx {
    const t = address(token, "token");
    const v = uint(amount, "amount");
    return this.tx(
      this.settler,
      REQUEST_WITHDRAW,
      [t, v],
      `requestWithdraw ${v} raw of ${t} (withdraw between 5 min and 1 day + 5 min later)`,
      opts.from,
    );
  }

  withdraw(token: string, to: string, opts: { from?: string } = {}): UnsignedTx {
    const t = address(token, "token");
    const r = address(to, "to");
    return this.tx(this.settler, WITHDRAW, [t, r], `withdraw the requested ${t} to ${r}`, opts.from);
  }

  /**
   * Voids every outstanding quote of `maker` (the maker or a live signer sends it). Doing this to back out of a quote
   * that already WON counts as a fade on the gateway scorecard.
   */
  increaseEpoch(maker: string, opts: { from?: string } = {}): UnsignedTx {
    const m = address(maker, "maker");
    return this.tx(this.settler, INCREASE_EPOCH, [m], `increaseEpoch(${m}): cancel all quotes`, opts.from ?? m);
  }

  invalidateNonces(maker: string, slot: bigint | number, mask: bigint | number, opts: { from?: string } = {}) {
    const m = address(maker, "maker");
    const s = uint(slot, "slot");
    const k = uint(mask, "mask");
    return this.tx(
      this.settler,
      INVALIDATE_NONCES,
      [m, s, k],
      `invalidateNonces(${m}, slot ${s}, mask ${hex(k)})`,
      opts.from ?? m,
    );
  }

  /** From a paying wallet that is not the maker: lets the maker's quotes name it as externalAccount. */
  linkMaker(maker: string, linked = true, opts: { from?: string } = {}): UnsignedTx {
    const m = address(maker, "maker");
    return this.tx(this.settler, LINK_MAKER, [m, linked], `linkMaker(${m}, ${linked})`, opts.from);
  }

  // -------------------------------------------------- core

  /** OptionsCore.setOperator(settler, true): lets the settler deliver your longs on MakerSells fills. */
  setOperator(opts: { operator?: string; approved?: boolean; from?: string } = {}): UnsignedTx {
    const core = this.requireCore();
    const op = address(opts.operator ?? this.settler, "operator");
    const approved = opts.approved ?? true;
    return this.tx(core, SET_OPERATOR, [op, approved], `core.setOperator(${op}, ${approved})`, opts.from);
  }

  /** After settlement: burn `amount` long of `onBehalf` and pay `receiver`. */
  redeemLong(series: Series, amount: bigint | number | string, onBehalf: string, receiver: string): UnsignedTx {
    const core = this.requireCore();
    const ob = address(onBehalf, "onBehalf");
    const rc = address(receiver, "receiver");
    const v = uint(amount, "amount");
    return this.tx(
      core,
      REDEEM_LONG,
      [seriesValues(series), v, ob, rc],
      `core.redeemLong(series ${seriesId(series)}, ${v})`,
      ob,
    );
  }

  // -------------------------------------------------- ERC-20

  /** ERC-20 approve of the settler (default spender) for `amount` raw. */
  approve(token: string, amount: bigint | number | string, opts: { spender?: string; from?: string } = {}) {
    const t = address(token, "token");
    const sp = address(opts.spender ?? this.settler, "spender");
    const v = uint(amount, "amount");
    return this.tx(t, APPROVE, [sp, v], `${t}.approve(${sp}, ${v})`, opts.from);
  }
}
