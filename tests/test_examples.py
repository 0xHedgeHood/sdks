"""The examples stay runnable: the pricer's arithmetic and the onboarding output (offline)."""

import importlib.util
import json
import sys
from argparse import Namespace
from pathlib import Path

import pytest

from hedgehood_maker import TESTNET, Decline
from hedgehood_maker.client import Session
from hedgehood_maker.fees import maker_cash
from hedgehood_maker.models import RFQ, Series

EXAMPLES = Path(__file__).resolve().parents[1] / "examples"


def load(name):
    spec = importlib.util.spec_from_file_location(name, EXAMPLES / f"{name}.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def rfq(side=0, amount=10**18, underlying=TESTNET.underlyings["TSLA"].token):
    s = Series(underlying, TESTNET.quote_token, TESTNET.underlyings["TSLA"].oracle, 1794603600, True, 330_000_000)
    return RFQ({}, rfq_id="r", chain_id=46630, settler=TESTNET.settler, taker="0x" + "11" * 20, side=side, series=s,
               amount=amount, respond_by_ms=0, min_valid_until=0)


@pytest.fixture
def pricer():
    m = load("simple_maker")

    def make(**kw):
        args = Namespace(mode="bps", bps=50, vol=0.6, spot=["TSLA=330"], edge=0.05, max_amount=10.0, sell=False)
        for k, v in kw.items():
            setattr(args, k, v)
        p = m.Pricer(args)
        p.client = Namespace(session=Session("0x" + "aa" * 20, "0x" + "aa" * 20, 46630, TESTNET.settler, 0, 1250, 100),
                             maker="0x" + "aa" * 20)
        return p

    return make


def test_bps_pricer_pays_at_most_theo_less_edge_all_in(pricer):
    premium = pricer()(rfq())
    theo = 330_000_000 * 50 // 10_000  # 1 token x 330 x 50 bps
    assert -maker_cash(0, premium, 10**18, 330_000_000, 1250, 100) <= int(theo * 0.95)


def test_vol_pricer_and_declines(pricer):
    assert isinstance(pricer(mode="vol")(rfq()), int)
    assert pricer()(rfq(side=1)) == Decline("no_inventory")
    assert pricer()(rfq(amount=11 * 10**18)) == Decline("too_large")
    assert pricer()(rfq(underlying="0x" + "22" * 20)) == Decline("unlisted")
    assert pricer(mode="vol", spot=[])(rfq()) == Decline("no_price")


def test_sell_quotes_only_what_the_maker_holds(pricer):
    asked = []

    class Reader:
        def __init__(self, why):
            self.why = why

        def can_sell(self, maker, series, amount):
            asked.append((maker, series.strike, amount))
            if self.why == "down":
                raise OSError("rpc down")
            return self.why

    p = pricer(sell=True)
    p.reader = Reader(None)
    assert isinstance(p(rfq(side=1)), int)                      # holds the long: quotes the close
    assert asked == [("0x" + "aa" * 20, 330_000_000, 10**18)]
    p.reader = Reader("holds 0 of 1000000000000000000")
    assert p(rfq(side=1)) == Decline("no_inventory")             # holds nothing: declines, never rejected
    p.reader = Reader("down")
    assert p(rfq(side=1)) == Decline("unavailable")


def test_onboard_prints_unsigned_transactions(monkeypatch, capsys):
    m = load("onboard")
    maker = "0x36437b878415EdA1a24186CF79AFffBc9ecEd298"
    monkeypatch.setattr(sys, "argv", ["onboard", "--maker", maker, "--signer", "0x" + "11" * 20, "--deposit", "100",
                                      "--external", "0x" + "22" * 20, "--allowance", "5", "--sell", "--json"])
    assert m.main() == 0
    txs = json.loads(capsys.readouterr().out)
    assert [t["data"][:10] for t in txs] == ["0x07843c87", "0x095ea7b3", "0x8340f549", "0x095ea7b3", "0x6aeac35f",
                                            "0x558a7297"]
    assert all("nonce" not in t and "gas" not in t for t in txs)  # unsigned, unsent
