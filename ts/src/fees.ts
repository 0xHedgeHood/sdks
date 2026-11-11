/**
 * The settler's money math (RfqSettler.previewFill), in bigints, rounding exactly as the contract does. Mirrors
 * hedgehood_maker.fees.
 *
 * The maker pays the protocol fee: fee = min(premium * feeBps, notional * notionalFeeBps) / 10 000 with notional =
 * amount * strike / unit (unit = one whole underlying token, 1e18), every step rounded down; notionalFeeBps = 0 means
 * no cap. MakerBuys: the maker pays premium + fee. MakerSells: the maker receives premium - fee. The relayer's gas fee
 * (when the taker's fill is submitted by the gateway operator) is the taker's: out of the premium it receives on an
 * open, on top of the premium it pays on a close. It never changes what the maker pays or receives.
 */

import { BPS, MAKER_BUYS, SHARE } from "./constants.js";
import type { Quote, Series } from "./models.js";

type Int = bigint | number;
const big = (v: Int): bigint => (typeof v === "bigint" ? v : BigInt(v));

export function feeOf(premium: Int, amount: Int, strike: Int, feeBps: Int, notionalFeeBps: Int = 0, unit: Int = SHARE) {
  let fee = (big(premium) * big(feeBps)) / BPS;
  if (big(notionalFeeBps) !== 0n && fee !== 0n) {
    const cap = (((big(amount) * big(strike)) / big(unit)) * big(notionalFeeBps)) / BPS;
    if (cap < fee) fee = cap;
  }
  return fee;
}

/**
 * {premium, fee} for filling `amount` of `q`: the premium pro rata, rounded down when the maker pays and up when it
 * receives.
 */
export function previewFill(
  q: Quote,
  amount: Int,
  feeBps: Int,
  notionalFeeBps: Int = 0,
  unit: Int = SHARE,
): { premium: bigint; fee: bigint } {
  const a = big(amount);
  if (!(a > 0n && a <= q.amount)) throw new RangeError("amount must be in (0, quote.amount]");
  const num = q.premium * a;
  const premium = q.side === MAKER_BUYS ? num / q.amount : (num + q.amount - 1n) / q.amount;
  return { premium, fee: feeOf(premium, a, q.series.strike, feeBps, notionalFeeBps, unit) };
}

/**
 * The maker's signed quote-token flow for a whole fill: -(premium + fee) when it buys, premium - fee when it sells.
 */
export function makerCash(
  side: number,
  premium: Int,
  amount: Int,
  strike: Int,
  feeBps: Int,
  notionalFeeBps: Int = 0,
  unit: Int = SHARE,
): bigint {
  const fee = feeOf(premium, amount, strike, feeBps, notionalFeeBps, unit);
  return side === MAKER_BUYS ? -(big(premium) + fee) : big(premium) - fee;
}

/**
 * The premium to quote so the fee lands on your side of `net` (quote raw, >= 0):
 *
 * - MakerBuys: the largest premium whose premium + fee <= net (what you are willing to pay all-in);
 * - MakerSells: the smallest premium whose premium - fee >= net (what you want to keep).
 */
export function premiumForNet(
  side: number,
  net: Int,
  amount: Int,
  strike: Int,
  feeBps: Int,
  notionalFeeBps: Int = 0,
  unit: Int = SHARE,
): bigint {
  const n = big(net);
  if (n < 0n) throw new RangeError("net must be >= 0");
  const fee = (p: bigint) => feeOf(p, amount, strike, feeBps, notionalFeeBps, unit);
  if (side === MAKER_BUYS) {
    // cost(p) = p + fee(p) is strictly increasing: binary search the last p with cost(p) <= net
    let lo = 0n;
    let hi = n;
    while (lo < hi) {
      const mid = (lo + hi + 1n) / 2n;
      if (mid + fee(mid) <= n) lo = mid;
      else hi = mid - 1n;
    }
    return lo;
  }
  // kept(p) = p - fee(p) is non-decreasing and kept(p) >= p * (1 - feeBps / 1e4); search the first p with kept >= net
  if (big(feeBps) >= BPS && big(notionalFeeBps) === 0n) throw new RangeError("a 100 % fee leaves nothing to keep");
  let lo = n;
  let hi = n > 1n ? n : 1n;
  while (hi - fee(hi) < n) hi *= 2n;
  while (lo < hi) {
    const mid = (lo + hi) / 2n;
    if (mid - fee(mid) >= n) hi = mid;
    else lo = mid + 1n;
  }
  return lo;
}

/**
 * What `amount` long pays at settlement price `price` (quote raw per token), rounded down as OptionsCore does: a call
 * pays amount * (P - K) / P underlying, a put amount * (K - P) / unit quote.
 */
export function longPayout(series: Series, amount: Int, price: Int, unit: Int = SHARE): bigint {
  const a = big(amount);
  const k = series.strike;
  const p = big(price);
  if (series.isPut) return p >= k ? 0n : (a * (k - p)) / big(unit);
  return p <= k ? 0n : (a * (p - k)) / p;
}

/** amount * strike / unit in quote raw: the base of the notional fee cap. */
export function notional(amount: Int, strike: Int, unit: Int = SHARE): bigint {
  return (big(amount) * big(strike)) / big(unit);
}
