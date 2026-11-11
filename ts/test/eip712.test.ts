import { hashTypedData, hexToBigInt, keccak256, recoverMessageAddress, toHex } from "viem";
import { generatePrivateKey, privateKeyToAccount } from "viem/accounts";
import { describe, expect, it } from "vitest";
import {
  domainSeparator,
  loginMessage,
  QUOTE_TYPE_STRING,
  quoteDigest,
  recoverLogin,
  recoverQuoteSigner,
  SERIES_TYPE_STRING,
  seriesId,
  signLogin,
  signQuote,
  typedData,
} from "../src/eip712.js";
import { makeQuote, ValidationError } from "../src/models.js";
import { encodeFill } from "../src/settler.js";
import * as G from "./goldens.js";

const N = 0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141n;

describe("goldens shared with the Solidity, Go and desk implementations", () => {
  it("type strings are the settler's", () => {
    expect(SERIES_TYPE_STRING).toBe(G.SOLIDITY_SERIES_TYPE);
    expect(QUOTE_TYPE_STRING).toBe(G.SOLIDITY_QUOTE_TYPE);
  });

  it("digest", () => {
    expect(quoteDigest(G.QUOTE, G.CHAIN, G.SETTLER)).toBe(G.DIGEST);
  });

  it("digest equals viem's standard typed-data hash", () => {
    expect(hashTypedData(typedData(G.QUOTE, G.CHAIN, G.SETTLER))).toBe(G.DIGEST);
  });

  it("series id", () => {
    expect(seriesId(G.SERIES)).toBe(G.SERIES_ID);
  });

  it("signature with private key 7, and recovery", async () => {
    expect(await signQuote(G.QUOTE, G.CHAIN, G.SETTLER, G.KEY_7)).toBe(G.SIGNATURE);
    expect(await signQuote(G.QUOTE, G.CHAIN, G.SETTLER, privateKeyToAccount(G.KEY_7))).toBe(G.SIGNATURE);
    expect(await recoverQuoteSigner(G.QUOTE, G.CHAIN, G.SETTLER, G.SIGNATURE)).toBe(
      privateKeyToAccount(G.KEY_7).address,
    );
  });

  it("refuses the high-s twin", async () => {
    const sig = Buffer.from(G.SIGNATURE.slice(2), "hex");
    const s = hexToBigInt(toHex(sig.subarray(32, 64)));
    const twin = Buffer.concat([
      sig.subarray(0, 32),
      Buffer.from((N - s).toString(16).padStart(64, "0"), "hex"),
      Buffer.from([sig[64]! ^ 1]),
    ]);
    await expect(recoverQuoteSigner(G.QUOTE, G.CHAIN, G.SETTLER, toHex(twin))).rejects.toThrow(ValidationError);
    // v as 0 / 1 is accepted
    const v01 = Buffer.from(sig);
    v01[64] = sig[64]! - 27;
    expect(await recoverQuoteSigner(G.QUOTE, G.CHAIN, G.SETTLER, toHex(v01))).toBe(
      privateKeyToAccount(G.KEY_7).address,
    );
    await expect(recoverQuoteSigner(G.QUOTE, G.CHAIN, G.SETTLER, "0x1234")).rejects.toThrow(ValidationError);
  });

  it("fill and close calldata", () => {
    expect(keccak256(encodeFill(G.QUOTE, G.SIGNATURE, 5n * 10n ** 18n))).toBe(G.FILL_CALLDATA_HASH);
    expect(keccak256(encodeFill(G.QUOTE, G.SIGNATURE, 10n ** 18n, true))).toBe(G.CLOSE_CALLDATA_HASH);
  });

  it("testnet DOMAIN_SEPARATOR", () => {
    expect(domainSeparator(46630, "0xda43b0933031e41D5B367E1807536FcF4ebdEE7e")).toBe(G.TESTNET_DOMAIN_SEPARATOR);
  });
});

/** mulberry32: a small seeded PRNG so the random cases are reproducible. */
function rng(seed: number) {
  let a = seed >>> 0;
  const next = () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  const bytes = (n: number) => toHex(Uint8Array.from({ length: n }, () => Math.floor(next() * 256)));
  const bits = (n: number) => hexToBigInt(bytes(Math.ceil(n / 8))) & ((1n << BigInt(n)) - 1n);
  return { next, bytes, bits };
}

describe("random quotes hash and sign like viem's own typed-data code", () => {
  for (let seed = 0; seed < 8; seed++) {
    it(`seed ${seed}`, async () => {
      const r = rng(seed + 1);
      const addr = () => r.bytes(20);
      const q = makeQuote({
        maker: addr(),
        signer: addr(),
        taker: addr(),
        externalAccount: r.next() < 0.5 ? `0x${"00".repeat(20)}` : addr(),
        series: {
          underlying: addr(),
          quote: addr(),
          oracle: addr(),
          expiry: r.bits(64),
          isPut: r.next() < 0.5,
          strike: r.bits(256),
          physical: r.next() < 0.5,
        },
        side: r.next() < 0.5 ? 0 : 1,
        amount: r.bits(256),
        minFill: r.bits(128),
        premium: r.bits(200),
        validUntil: r.bits(64),
        nonce: r.bits(256),
        epoch: r.bits(32),
      });
      const settler = addr();
      const chain = 1 + Math.floor(r.next() * 2 ** 32);
      expect(quoteDigest(q, chain, settler)).toBe(hashTypedData(typedData(q, chain, settler)));
      const key = generatePrivateKey();
      const acct = privateKeyToAccount(key);
      const sig = await signQuote(q, chain, settler, key);
      expect(await recoverQuoteSigner(q, chain, settler, sig)).toBe(acct.address);
      // signing the typed data with a wallet gives the same signer
      const walletSig = await acct.signTypedData(typedData(q, chain, settler));
      expect(await recoverQuoteSigner(q, chain, settler, walletSig)).toBe(acct.address);
    });
  }
});

describe("gateway login", () => {
  it("message and signature match the gateway's", async () => {
    const maker = "0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308";
    expect(loginMessage(46630, maker, "ab12")).toBe(`hedgehood-rfq-login:46630:${maker.toLowerCase()}:ab12`);
    const key = generatePrivateKey();
    const acct = privateKeyToAccount(key);
    const sig = await signLogin(46630, maker, "ab12", key);
    const msg = `hedgehood-rfq-login:46630:${maker.toLowerCase()}:ab12`;
    expect(await recoverMessageAddress({ message: msg, signature: sig })).toBe(acct.address);
    expect(await recoverLogin(46630, maker.toLowerCase(), "ab12", sig)).toBe(acct.address);
  });
});
