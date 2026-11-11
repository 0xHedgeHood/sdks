import { describe, expect, it } from "vitest";
import { quoteDigest } from "../src/eip712.js";
import {
  declineFrame,
  makeSeries,
  parseFrame,
  quoteEquals,
  quoteFrame,
  quoteFromJson,
  quoteToJson,
  type Ready,
  type RFQ,
  seriesEquals,
  seriesFromJson,
  seriesToJson,
  ValidationError,
} from "../src/models.js";
import * as G from "./goldens.js";

describe("quote JSON", () => {
  it("has the types the gateway parses", () => {
    const j = quoteToJson(G.QUOTE);
    // uint256 as decimal strings, uint64 / uint8 as numbers (Go: quote.Dec vs uint64)
    expect(j.amount).toBe("5000000000000000000");
    expect(j.premium).toBe("5164250");
    expect(j.nonce).toBe("42");
    expect(j.epoch).toBe("0");
    expect(j.minFill).toBe("0");
    expect(j.series.strike).toBe("215000000");
    expect(j.validUntil).toBe(1793766600);
    expect(j.series.expiry).toBe(1793826000);
    expect(j.side).toBe(0);
    expect(j.series.physical).toBe(false);
    expect(j.externalAccount).toBe(`0x${"00".repeat(20)}`);
    const back = quoteFromJson(JSON.parse(JSON.stringify(j)));
    expect(quoteEquals(back, G.QUOTE)).toBe(true);
    expect(quoteDigest(back, G.CHAIN, G.SETTLER)).toBe(G.DIGEST);
  });

  it("accepts bare numbers and lower case", () => {
    const j: Record<string, unknown> = { ...quoteToJson(G.QUOTE) };
    j.amount = 5_000_000_000; // a safe integer
    j.maker = (j.maker as string).toLowerCase();
    const q = quoteFromJson(j);
    expect(q.amount).toBe(5_000_000_000n);
    expect(q.maker).toBe(G.QUOTE.maker);
    j.amount = "5000000000000000000";
    expect(quoteEquals(quoteFromJson(j), G.QUOTE)).toBe(true);
  });

  const bad: Array<Record<string, unknown>> = [
    { side: 2 },
    { amount: "-1" },
    { amount: 1.5 },
    { premium: "0x10" },
    { maker: "0x1234" },
    { validUntil: 2 ** 64 },
    { validUntil: "18446744073709551616" },
    { nonce: (2n ** 256n).toString() },
    { amount: true },
    { amount: 2 ** 60 }, // not a safe integer: precision already lost in JSON.parse
    { amount: "" },
  ];
  for (const change of bad) {
    it(`rejects ${JSON.stringify(change)}`, () => {
      expect(() => quoteFromJson({ ...quoteToJson(G.QUOTE), ...change })).toThrow(ValidationError);
    });
  }

  it("refuses a uint64 that a JS number cannot carry exactly", () => {
    expect(() => quoteToJson({ ...G.QUOTE, validUntil: 2n ** 60n })).toThrow(ValidationError);
  });
});

describe("series", () => {
  it("validates", () => {
    expect(() => seriesFromJson({ ...seriesToJson(G.SERIES), isPut: "false" })).toThrow(ValidationError);
    const { physical: _, ...noPhysical } = seriesToJson(G.SERIES);
    expect(seriesEquals(seriesFromJson(noPhysical), G.SERIES)).toBe(true);
    expect(() => makeSeries({ ...G.SERIES, oracle: "0x12" })).toThrow(ValidationError);
  });
});

describe("server frames", () => {
  it("parses every type", () => {
    const rfq = parseFrame({
      type: "rfq",
      rfqId: "19a6c1f2b3c-0011223344556677",
      chainId: 46630,
      settler: "0xda43b0933031e41d5b367e1807536fcf4ebdee7e",
      taker: "0x36437b878415eda1a24186cf79afffbc9ecED298",
      side: 0,
      series: {
        underlying: "0x6661d20c2e343bb78b42535eb060c3058c744f3f",
        quote: "0x850de815d41f6983aaf09247f9a465c0aefff04c",
        oracle: "0x9e6c529caa69d44b63df27483c57f97a42d6e898",
        expiry: 1794603600,
        isPut: true,
        strike: "330000000",
        physical: false,
      },
      amount: "1000000000000000000",
      respondBy: 1794500000250,
      minValidUntil: 1794500015,
    }) as RFQ;
    expect(rfq.type).toBe("rfq");
    expect(rfq.side).toBe(0);
    expect(rfq.series.strike).toBe(330_000_000n);
    expect(rfq.settler).toBe("0xda43b0933031e41D5B367E1807536FcF4ebdEE7e");
    expect(rfq.amount).toBe(10n ** 18n);
    expect(rfq.respondBy).toBe(1794500000250);
    expect(rfq.minValidUntil).toBe(1794500015n);

    // the gateway sends epoch as a decimal string and the bps as numbers
    const ready = parseFrame({
      type: "ready",
      maker: `0x${"11".repeat(20)}`,
      settler: `0x${"22".repeat(20)}`,
      feeBps: 1250,
      notionalFeeBps: 100,
      epoch: "3",
    }) as Ready;
    expect(ready.type).toBe("ready");
    expect(ready.epoch).toBe(3n);
    expect(ready.notionalFeeBps).toBe(100);
    expect(parseFrame({ type: "challenge", nonce: "ab", chainId: 46630 })).toMatchObject({ type: "challenge" });
    expect(parseFrame({ type: "won", rfqId: "r", digest: "0xABCD" })).toMatchObject({ type: "won", digest: "0xabcd" });
    expect(parseFrame({ type: "fill", digest: "0xab", txHash: "0xcd", amount: "5", rfqId: "r" })).toMatchObject({
      type: "fill",
      amount: 5n,
      rfqId: "r",
    });
    expect(parseFrame({ type: "lost", rfqId: "r" })).toMatchObject({ type: "lost", rfqId: "r" });
    expect(parseFrame({ type: "rejected", rfqId: "r", code: "stale_epoch", reason: "x" })).toMatchObject({
      type: "rejected",
      code: "stale_epoch",
    });
    expect(parseFrame({ type: "error", code: "login", reason: "x" })).toMatchObject({ type: "error", code: "login" });
    expect(parseFrame({ type: "paused" }).type).toBe("paused");
    expect(parseFrame({ type: "resumed" }).type).toBe("resumed");
    const unknown = parseFrame({ type: "something-new" });
    expect(unknown.type).toBe("unknown");
    expect(unknown.raw.type).toBe("something-new");
    expect(() => parseFrame({ type: "rfq", rfqId: "r", side: 0 })).toThrow(ValidationError);
    expect(() => parseFrame("rfq")).toThrow(ValidationError);
  });

  it("builds outgoing frames", () => {
    const f = quoteFrame("r1", G.QUOTE, G.SIGNATURE);
    expect(f.type).toBe("quote");
    expect(f.quote.premium).toBe("5164250");
    expect(f.signature).toBe(G.SIGNATURE);
    expect(declineFrame("r1", "")).toEqual({ type: "decline", rfqId: "r1", reason: "declined" });
  });
});
