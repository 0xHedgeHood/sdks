/** The example stays runnable: the pricer's arithmetic and declines (offline). */

import { describe, expect, it } from "vitest";
import { blackScholes, makePricer, type PricerArgs } from "../examples/simple-maker.js";
import { Decline, makerCash, makeSeries, parseFrame, type RFQ, TESTNET } from "../src/index.js";

const TSLA = TESTNET.underlyings.TSLA!;

function rfq(side = 0, amount = 10n ** 18n, underlying: string = TSLA.token): RFQ {
  const s = makeSeries({
    underlying,
    quote: TESTNET.quoteToken,
    oracle: TSLA.oracle,
    expiry: Math.floor(Date.now() / 1000) + 14 * 86400,
    isPut: true,
    strike: 330_000_000n,
  });
  return parseFrame({
    type: "rfq",
    rfqId: "r",
    chainId: 46630,
    settler: TESTNET.settler,
    taker: `0x${"11".repeat(20)}`,
    side,
    series: { ...s, expiry: Number(s.expiry), strike: s.strike.toString() },
    amount: amount.toString(),
    respondBy: 0,
    minValidUntil: 0,
  }) as RFQ;
}

function pricer(over: Partial<PricerArgs> = {}) {
  const args: PricerArgs = {
    mode: "bps",
    bps: 50,
    vol: 0.6,
    spots: { TSLA: 330 },
    edge: 0.05,
    maxAmount: 10,
    sell: false,
    ...over,
  };
  return makePricer(args, () => ({ feeBps: 1250, notionalFeeBps: 100 }));
}

describe("examples/simple-maker.ts", () => {
  it("bps pricer pays at most theo less the edge, all in", () => {
    const premium = pricer()(rfq()) as bigint;
    const theo = (330_000_000n * 50n) / 10_000n; // 1 token x 330 x 50 bps
    expect(typeof premium).toBe("bigint");
    expect(-makerCash(0, premium, 10n ** 18n, 330_000_000n, 1250, 100) <= BigInt(Math.floor(Number(theo) * 0.95))).toBe(
      true,
    );
  });

  it("vol pricer and declines", () => {
    expect(typeof pricer({ mode: "vol" })(rfq())).toBe("bigint");
    expect(pricer()(rfq(1))).toEqual(new Decline("no_inventory"));
    expect(pricer()(rfq(0, 11n * 10n ** 18n))).toEqual(new Decline("too_large"));
    expect(pricer()(rfq(0, 10n ** 18n, `0x${"22".repeat(20)}`))).toEqual(new Decline("unlisted"));
    expect(pricer({ mode: "vol", spots: {} })(rfq())).toEqual(new Decline("no_price"));
  });

  it("Black-Scholes put-call parity at rate 0", () => {
    const c = blackScholes(330, 300, 0.25, 0.6, false);
    const p = blackScholes(330, 300, 0.25, 0.6, true);
    expect(c - p).toBeCloseTo(30, 5);
  });
});
