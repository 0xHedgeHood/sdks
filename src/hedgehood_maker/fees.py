"""The settler's money math (RfqSettler.previewFill), in integers, rounding exactly as the contract does.

The maker pays the protocol fee: fee = min(premium * feeBps, notional * notionalFeeBps) / 10 000 with notional =
amount * strike / unit (unit = one whole underlying token, 1e18), every step rounded down; notionalFeeBps = 0 means no
cap. MakerBuys: the maker pays premium + fee. MakerSells: the maker receives premium - fee. The relayer's gas fee
(when the taker's fill is submitted by the gateway operator) is the taker's: out of the premium it receives on an open,
on top of the premium it pays on a close. It never changes what the maker pays or receives.
"""

from __future__ import annotations

from .constants import BPS, MAKER_BUYS
from .models import Quote, Series

SHARE = 10**18  # one whole stock token
USDG = 10**6  # one USDG


def fee_of(premium: int, amount: int, strike: int, fee_bps: int, notional_fee_bps: int = 0, unit: int = SHARE) -> int:
    fee = int(premium) * int(fee_bps) // BPS
    if int(notional_fee_bps) and fee:
        cap = int(amount) * int(strike) // int(unit) * int(notional_fee_bps) // BPS
        fee = min(fee, cap)
    return fee


def preview_fill(q: Quote, amount: int, fee_bps: int, notional_fee_bps: int = 0, unit: int = SHARE) -> tuple[int, int]:
    """(premium, fee) for filling `amount` of `q`: the premium pro rata, rounded down when the maker pays and up when
    it receives."""
    amount = int(amount)
    if not 0 < amount <= q.amount:
        raise ValueError("amount must be in (0, quote.amount]")
    num = q.premium * amount
    premium = num // q.amount if q.side == MAKER_BUYS else -(-num // q.amount)
    return premium, fee_of(premium, amount, q.series.strike, fee_bps, notional_fee_bps, unit)


def maker_cash(side: int, premium: int, amount: int, strike: int, fee_bps: int, notional_fee_bps: int = 0,
               unit: int = SHARE) -> int:
    """The maker's signed quote-token flow for a whole fill: -(premium + fee) when it buys, premium - fee when it
    sells."""
    fee = fee_of(premium, amount, strike, fee_bps, notional_fee_bps, unit)
    return -(premium + fee) if side == MAKER_BUYS else premium - fee


def premium_for_net(side: int, net: int, amount: int, strike: int, fee_bps: int, notional_fee_bps: int = 0,
                    unit: int = SHARE) -> int:
    """The premium to quote so the fee lands on your side of `net` (quote raw, >= 0):

    - MakerBuys: the largest premium whose premium + fee <= net (what you are willing to pay all-in);
    - MakerSells: the smallest premium whose premium - fee >= net (what you want to keep).
    """
    net = int(net)
    if net < 0:
        raise ValueError("net must be >= 0")

    def cost(p: int) -> int:
        return p + fee_of(p, amount, strike, fee_bps, notional_fee_bps, unit)

    def kept(p: int) -> int:
        return p - fee_of(p, amount, strike, fee_bps, notional_fee_bps, unit)

    if side == MAKER_BUYS:  # cost() is strictly increasing: binary search the last p with cost(p) <= net
        lo, hi = 0, net
        while lo < hi:
            mid = (lo + hi + 1) // 2
            if cost(mid) <= net:
                lo = mid
            else:
                hi = mid - 1
        return lo
    # kept() is non-decreasing and kept(p) >= p * (1 - feeBps / 1e4); search the first p with kept(p) >= net
    if fee_bps >= BPS and not notional_fee_bps:
        raise ValueError("a 100 % fee leaves nothing to keep")
    lo, hi = net, max(net, 1)
    while kept(hi) < net:
        hi *= 2
    while lo < hi:
        mid = (lo + hi) // 2
        if kept(mid) >= net:
            hi = mid
        else:
            lo = mid + 1
    return lo


def long_payout(series: Series, amount: int, price: int, unit: int = SHARE) -> int:
    """What `amount` long pays at settlement price `price` (quote raw per token), rounded down as OptionsCore does:
    a call pays amount * (P - K) / P underlying, a put amount * (K - P) / unit quote."""
    amount, k, p = int(amount), series.strike, int(price)
    if series.is_put:
        return 0 if p >= k else amount * (k - p) // unit
    return 0 if p <= k else amount * (p - k) // p


def notional(amount: int, strike: int, unit: int = SHARE) -> int:
    """amount * strike / unit in quote raw: the base of the notional fee cap."""
    return int(amount) * int(strike) // int(unit)
