import { describe, expect, it } from "vitest";
import { MAKER_BUYS, MAKER_SELLS } from "../src/constants.js";
import { feeOf, longPayout, makerCash, premiumForNet, previewFill } from "../src/fees.js";
import { makeSeries, replaceQuote } from "../src/models.js";
import * as G from "./goldens.js";

const E18 = 10n ** 18n;
const K = 220_000_000n;

describe("fees", () => {
  it("goldens from the settler, Go and desk", () => {
    // RfqSettler.t.sol test_feeIsCappedAtTheNotional / Go quote.TestFee: 12.5 % of the premium, at most 1 % of 10 x 220
    expect(feeOf(30n * 10n ** 6n, 10n * E18, K, 1250, 100)).toBe(3_750_000n);
    expect(feeOf(300n * 10n ** 6n, 10n * E18, K, 1250, 100)).toBe(22n * 10n ** 6n);
    expect(feeOf(150n * 10n ** 6n, 5n * E18, K, 1250, 100)).toBe(11n * 10n ** 6n);
    expect(feeOf(300n * 10n ** 6n, 10n * E18, K, 1250)).toBe(37_500_000n); // notionalFeeBps 0 = no cap
    expect(feeOf(2_185_069n, E18, K, 500)).toBe(109_253n); // the testnet E2E fill
  });

  it("previewFill prorates in the maker's favour", () => {
    expect(previewFill(G.QUOTE, 3n * E18, 500)).toEqual({ premium: 3_098_550n, fee: 154_927n });
    const sells = replaceQuote(G.QUOTE, { side: MAKER_SELLS, premium: 1_000_001n, amount: 3n });
    expect(previewFill(sells, 1n, 500).premium).toBe(333_334n);
    expect(() => previewFill(G.QUOTE, 0n, 500)).toThrow(RangeError);
  });

  it("makerCash", () => {
    expect(makerCash(MAKER_BUYS, 30n * 10n ** 6n, 10n * E18, K, 1250, 100)).toBe(-33_750_000n);
    expect(makerCash(MAKER_SELLS, 300n * 10n ** 6n, 10n * E18, K, 1250, 100)).toBe(278_000_000n);
  });

  const nets = [0n, 1n, 7n, 1_000_000n, 33_750_000n, 250_000_000n, 10n ** 12n];
  const feeSets: Array<[number, number]> = [
    [1250, 100],
    [1250, 0],
    [500, 0],
    [0, 0],
  ];
  for (const net of nets) {
    for (const [fb, nb] of feeSets) {
      it(`premiumForNet net=${net} fees=${fb}/${nb}`, () => {
        const amount = 10n * E18;
        let p = premiumForNet(MAKER_BUYS, net, amount, K, fb, nb);
        expect(-makerCash(MAKER_BUYS, p, amount, K, fb, nb) <= net).toBe(true);
        expect(-makerCash(MAKER_BUYS, p + 1n, amount, K, fb, nb) > net).toBe(true);
        p = premiumForNet(MAKER_SELLS, net, amount, K, fb, nb);
        expect(makerCash(MAKER_SELLS, p, amount, K, fb, nb) >= net).toBe(true);
        expect(p === 0n || makerCash(MAKER_SELLS, p - 1n, amount, K, fb, nb) < net).toBe(true);
      });
    }
  }

  it("longPayout rounds down", () => {
    const base = { underlying: `0x${"11".repeat(20)}`, quote: `0x${"22".repeat(20)}`, oracle: `0x${"33".repeat(20)}` };
    const call = makeSeries({ ...base, expiry: 1, isPut: false, strike: 215_000_000n });
    const put = makeSeries({ ...base, expiry: 1, isPut: true, strike: 195_000_000n });
    const p = 227_227_000n;
    expect(longPayout(call, 3n * E18, p)).toBe((3n * E18 * (p - 215_000_000n)) / p);
    expect(longPayout(put, 2n * E18, p)).toBe(0n);
    expect(longPayout(put, 2n * E18, 150_000_000n)).toBe(90_000_000n);
  });
});
