"""A minimal maker: quotes every listed TESTNET request at a fixed volatility (Black-Scholes on a spot you pass) or at a
fixed premium in basis points of the notional. A starting point, not a pricing model: no spot feed, no hedging, no
inventory limits beyond --max-amount. With --sell it also quotes early closes, but only for series it holds (it reads
its longs on chain first; the gateway would reject the rest as `inventory`).

    export HEDGEHOOD_SIGNER_KEY=...           # the maker key, or a hot key the maker delegated with setSigner
    python examples/simple_maker.py --maker 0xYourMaker --mode vol --vol 0.6 --spot TSLA=330 --edge 0.05
    python examples/simple_maker.py --mode bps --bps 50

Testnet only. The key is read from the environment (or --key-file) and never printed.
"""

from __future__ import annotations

import argparse
import asyncio
import logging
import math
import os
import sys
import time

from eth_utils import to_checksum_address

from hedgehood_maker import MAKER_BUYS, RFQ, TESTNET, Decline, MakerClient
from hedgehood_maker.fees import SHARE, USDG, notional, premium_for_net
from hedgehood_maker.rpc import Reader

YEAR = 365 * 24 * 3600


def black_scholes(spot: float, strike: float, years: float, vol: float, is_put: bool) -> float:
    """Price per share (rate 0, no dividends)."""
    if years <= 0 or vol <= 0:
        return max(0.0, (strike - spot) if is_put else (spot - strike))
    sd = vol * math.sqrt(years)
    d1 = (math.log(spot / strike) + 0.5 * sd * sd) / sd
    d2 = d1 - sd

    def n(x: float) -> float:
        return 0.5 * (1 + math.erf(x / math.sqrt(2)))

    return strike * n(-d2) - spot * n(-d1) if is_put else spot * n(d1) - strike * n(d2)


class Pricer:
    def __init__(self, args: argparse.Namespace):
        self.args = args
        self.spots = dict(s.split("=") for s in args.spot)
        self.listed = {(u.token, u.oracle): name for name, u in TESTNET.underlyings.items()}
        self.client: MakerClient | None = None
        self.reader: Reader | None = None

    def __call__(self, rfq: RFQ) -> int | Decline:
        a, s = self.args, rfq.series
        name = self.listed.get((s.underlying, s.oracle))
        if name is None or s.quote != TESTNET.quote_token:
            return Decline("unlisted")
        if rfq.amount > int(a.max_amount * SHARE):
            return Decline("too_large")
        if rfq.side != MAKER_BUYS:
            if not a.sell:
                return Decline("no_inventory")
            try:
                why = self.reader.can_sell(self.client.maker, s, rfq.amount)
            except Exception:  # noqa: BLE001 - no read, no quote
                return Decline("unavailable")
            if why:
                logging.info("close %s declined: %s", rfq.rfq_id, why)
                return Decline("no_inventory")
        if a.mode == "bps":
            theo = notional(rfq.amount, s.strike) * a.bps // 10_000  # quote raw
        else:
            if name not in self.spots:
                return Decline("no_price")
            per_share = black_scholes(float(self.spots[name]), s.strike / USDG, (s.expiry - time.time()) / YEAR,
                                      a.vol, s.is_put)
            theo = int(per_share * USDG * rfq.amount / SHARE)
        edge = 1 - a.edge if rfq.side == MAKER_BUYS else 1 + a.edge  # buy below, sell above
        net = int(theo * edge)  # what we pay all-in (buys) or keep after the fee (sells)
        if net <= 0:
            return Decline("no_price")
        sess = self.client.session
        return premium_for_net(rfq.side, net, rfq.amount, s.strike, sess.fee_bps, sess.notional_fee_bps) or \
            Decline("no_price")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--gateway", default=TESTNET.gateway_ws)
    ap.add_argument("--maker", help="maker address when the key is a delegated hot signer")
    ap.add_argument("--key-file", help="file holding the signer key (default: $HEDGEHOOD_SIGNER_KEY)")
    ap.add_argument("--external", help='externalAccount: an address, or "self" for the maker wallet (default: settler '
                                       "balance)")
    ap.add_argument("--mode", choices=["vol", "bps"], default="bps")
    ap.add_argument("--vol", type=float, default=0.6)
    ap.add_argument("--spot", action="append", default=[], help="TICKER=price, e.g. TSLA=330 (mode vol)")
    ap.add_argument("--bps", type=int, default=50, help="premium in bps of amount x strike (mode bps)")
    ap.add_argument("--edge", type=float, default=0.05, help="fraction below theo to buy / above to sell")
    ap.add_argument("--max-amount", type=float, default=10.0, help="largest request quoted, in whole tokens")
    ap.add_argument("--sell", action="store_true", help="also quote MakerSells (you must hold the longs)")
    ap.add_argument("--life", type=int, default=60, help="quote validity, seconds (<= 115)")
    ap.add_argument("--check-rpc", action="store_true", help="read the maker's epoch from the chain after a "
                                                             "stale_epoch rejection")
    args = ap.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")

    key = open(args.key_file).read().strip() if args.key_file else os.environ.get("HEDGEHOOD_SIGNER_KEY")
    if not key:
        sys.exit("set HEDGEHOOD_SIGNER_KEY or pass --key-file")
    pricer = Pricer(args)
    reader = Reader.for_network(TESTNET) if args.check_rpc or args.sell else None
    client = MakerClient(
        args.gateway, key, pricer, maker=args.maker, quote_life=args.life,
        chain_id=TESTNET.chain_id, settler=TESTNET.settler,
        epoch_reader=(lambda m: asyncio.to_thread(reader.epoch, m)) if args.check_rpc else None,
        on_event=lambda f: logging.info("%s %s", f.type, f.raw) if f.type in ("won", "fill", "rejected") else None,
    )
    if args.external:  # pay / receive premiums in a wallet by allowance instead of the settler balance
        client.external_account = to_checksum_address(client.maker if args.external == "self" else args.external)
    pricer.client, pricer.reader = client, reader
    try:
        asyncio.run(client.run())
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
