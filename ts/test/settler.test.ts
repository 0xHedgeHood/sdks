/** Unsigned transaction builders and read-only calls. Nothing is signed or sent: the RPC transport is a fake. */

import { createPublicClient, custom, decodeAbiParameters, encodeAbiParameters, type Hex } from "viem";
import { describe, expect, it } from "vitest";
import { quoteValues } from "../src/eip712.js";
import { TESTNET } from "../src/networks.js";
import { masks, NonceManager, split } from "../src/nonces.js";
import { Reader, RPCError } from "../src/rpc.js";
import * as S from "../src/settler.js";
import * as G from "./goldens.js";

const MAKER = "0x36437b878415EdA1a24186CF79AFffBc9ecEd298";
const HOT = "0xa6E328D981E199bD7A9C466AC3684679CF3a6D97";

const SIGNATURES: Array<[string, string]> = [
  ["setSigner", S.SET_SIGNER],
  ["deposit", S.DEPOSIT],
  ["requestWithdraw", S.REQUEST_WITHDRAW],
  ["withdraw", S.WITHDRAW],
  ["increaseEpoch", S.INCREASE_EPOCH],
  ["invalidateNonces", S.INVALIDATE_NONCES],
  ["linkMaker", S.LINK_MAKER],
  ["fill", S.FILL],
  ["close", S.CLOSE],
  ["epoch", S.EPOCH],
  ["signerExpiry", S.SIGNER_EXPIRY],
  ["nonceUsed", S.NONCE_USED],
  ["settlerBalanceOf", S.SETTLER_BALANCE],
  ["pendingWithdraw", S.PENDING_WITHDRAW],
  ["linked", S.LINKED],
  ["feeBps", S.FEE_BPS],
  ["notionalFeeBps", S.NOTIONAL_FEE_BPS],
  ["quoteDigest", S.QUOTE_DIGEST],
  ["previewFill", S.PREVIEW_FILL],
  ["isListed", S.IS_LISTED],
  ["core.setOperator", S.SET_OPERATOR],
  ["core.isOperator", S.IS_OPERATOR],
  ["core.balanceOf", S.LONG_BALANCE],
  ["core.redeemLong", S.REDEEM_LONG],
  ["erc20.approve", S.APPROVE],
  ["erc20.allowance", S.ALLOWANCE],
  ["erc20.balanceOf", S.ERC20_BALANCE],
];

describe("selectors match the compiled contracts", () => {
  for (const [name, sig] of SIGNATURES) {
    it(name, () => expect(S.selector(sig)).toBe(`0x${G.SELECTORS[name]}`));
  }
});

function argsOf(tx: S.UnsignedTx): readonly unknown[] {
  return decodeAbiParameters(S.abiParams(S.argTypes(tx.function)), `0x${tx.data.slice(10)}`);
}

describe("SettlerTxs", () => {
  const b = S.SettlerTxs.forNetwork(TESTNET);
  const usdg = TESTNET.quoteToken;

  it("builds the onboarding transactions, unsigned", () => {
    let t = b.setSigner(HOT, 1_800_000_000, { from: MAKER });
    expect(t.to).toBe(TESTNET.settler);
    expect(t.data.startsWith("0x07843c87")).toBe(true);
    expect(t.from).toBe(MAKER);
    expect(argsOf(t)).toEqual([HOT, 1_800_000_000n]);
    expect(S.txRequest(t)).toEqual({ chainId: 46630, to: TESTNET.settler, data: t.data, value: 0n, from: MAKER });
    expect(
      S.castCommand(t).startsWith(`cast send ${TESTNET.settler} "setSigner(address,uint64)" "${HOT}" "1800000000"`),
    ).toBe(true);

    t = b.approve(usdg, 1000n * 10n ** 6n, { from: MAKER });
    expect(t.to).toBe(usdg);
    expect(argsOf(t)).toEqual([TESTNET.settler, 1000n * 10n ** 6n]);
    expect(argsOf(b.deposit(usdg, MAKER, 1000n * 10n ** 6n))).toEqual([usdg, MAKER, 1000n * 10n ** 6n]);
    expect(argsOf(b.requestWithdraw(usdg, 5))).toEqual([usdg, 5n]);
    expect(argsOf(b.withdraw(usdg, MAKER))).toEqual([usdg, MAKER]);
    t = b.increaseEpoch(MAKER);
    expect(argsOf(t)).toEqual([MAKER]);
    expect(t.from).toBe(MAKER);
    expect(argsOf(b.invalidateNonces(MAKER, 7, 0b101))).toEqual([MAKER, 7n, 5n]);
    expect(argsOf(b.linkMaker(MAKER, true, { from: HOT }))).toEqual([MAKER, true]);
    t = b.setOperator({ from: MAKER });
    expect(t.to).toBe(TESTNET.core);
    expect(argsOf(t)).toEqual([TESTNET.settler, true]);
    t = b.redeemLong(G.SERIES, 10n ** 18n, MAKER, MAKER);
    expect(t.to).toBe(TESTNET.core);
    expect(t.data.startsWith("0xf8ad1b36")).toBe(true);
    expect(S.castCommand(t)).toContain(`"(${G.SERIES.underlying},`);
    // nothing about a nonce, gas or a signature: these are for the user's own wallet
    for (const k of ["nonce", "gas", "signature", "r", "s", "v"]) expect(t).not.toHaveProperty(k);
  });

  it("validates", () => {
    expect(() => b.setSigner(HOT, 2n ** 64n)).toThrow();
    expect(() => b.deposit(usdg, "0x1234", 1)).toThrow();
    expect(() => new S.SettlerTxs(46630, TESTNET.settler).setOperator()).toThrow();
  });
});

describe("nonces", () => {
  it("are unique and roll slots every 256", () => {
    const m = new NonceManager(5);
    const got = Array.from({ length: 300 }, () => m.next());
    expect(new Set(got).size).toBe(300);
    expect(split(got[0]!)).toEqual({ slot: 5n, bit: 0 });
    expect(split(got[255]!)).toEqual({ slot: 5n, bit: 255 });
    expect(split(got[256]!)).toEqual({ slot: 6n, bit: 0 });
    expect(masks(got)).toEqual(
      new Map([
        [5n, 2n ** 256n - 1n],
        [6n, 2n ** 44n - 1n],
      ]),
    );
    expect(m.invalidateAll()).toEqual(masks(got));
    expect(new NonceManager().next()).not.toBe(new NonceManager().next()); // random 64-bit start slots
  });
});

/** Answers eth_call by selector; records requests. */
function fakeRpc(answers: Record<string, Hex>) {
  const requests: Array<{ method: string; params?: unknown }> = [];
  const transport = custom(
    {
      async request({ method, params }: { method: string; params?: unknown }) {
        requests.push({ method, params });
        if (method === "eth_chainId") return "0xb626";
        if (method !== "eth_call") throw new Error(`unexpected ${method}`);
        const data = (params as [{ data: Hex }])[0].data;
        const answer = answers[data.slice(2, 10)];
        if (!answer) throw new Error("execution reverted");
        return answer;
      },
    },
    { retryCount: 0 },
  );
  return { client: createPublicClient({ transport }), requests };
}

const enc = (types: string[], values: unknown[]) =>
  encodeAbiParameters(
    types.map((type) => ({ type })),
    values,
  );

describe("Reader", () => {
  it("decodes settler state over eth_call only", async () => {
    const rpc = fakeRpc({
      "8b810c36": enc(["uint256"], [3n]),
      "24a9d853": enc(["uint256"], [1250n]),
      "1faa35a9": enc(["uint256"], [100n]),
      e57def0c: enc(["uint64"], [1_800_000_000n]),
      "1d971f5d": G.DIGEST as Hex,
      f59a193d: enc(["uint256", "uint256"], [3_098_550n, 154_927n]),
      b6363cf2: enc(["bool"], [true]),
      "742e1c0f": enc(["uint256", "uint256"], [5n, 1_800_000_300n]),
    });
    const r = Reader.forNetwork(TESTNET, { client: rpc.client });
    expect(await r.chainId()).toBe(46630);
    expect(await r.epoch(MAKER)).toBe(3n);
    expect(await r.fees()).toEqual({ feeBps: 1250n, notionalFeeBps: 100n });
    expect(await r.signerExpiry(MAKER, HOT)).toBe(1_800_000_000n);
    expect(await r.quoteDigest(G.QUOTE)).toBe(G.DIGEST);
    const digestCall = rpc.requests.at(-1)!.params as [{ to: string; data: Hex }];
    expect(digestCall[0].to.toLowerCase()).toBe(TESTNET.settler.toLowerCase());
    expect(digestCall[0].data).toBe(S.calldata(S.QUOTE_DIGEST, quoteValues(G.QUOTE)));
    expect(await r.previewFill(G.QUOTE, 3n * 10n ** 18n)).toEqual({ premium: 3_098_550n, fee: 154_927n });
    expect(await r.isOperator(MAKER)).toBe(true);
    expect(await r.pendingWithdraw(TESTNET.quoteToken, MAKER)).toEqual({ amount: 5n, availableAt: 1_800_000_300n });
    expect(rpc.requests.every((p) => p.method === "eth_call" || p.method === "eth_chainId")).toBe(true); // reads only
    await expect(r.nonceUsed(MAKER, 1)).rejects.toThrow(RPCError);
  });
});
