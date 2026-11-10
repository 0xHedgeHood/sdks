import goldens as G
import pytest

from hedgehood_maker.constants import MAKER_BUYS, MAKER_SELLS
from hedgehood_maker.fees import fee_of, long_payout, maker_cash, premium_for_net, preview_fill
from hedgehood_maker.models import Series

E18 = 10**18
K = 220_000_000


def test_fee_goldens_from_the_settler_go_and_desk():
    # RfqSettler.t.sol test_feeIsCappedAtTheNotional / Go quote.TestFee: 12.5 % of the premium, at most 1 % of 10 x 220
    assert fee_of(30 * 10**6, 10 * E18, K, 1250, 100) == 3_750_000
    assert fee_of(300 * 10**6, 10 * E18, K, 1250, 100) == 22 * 10**6
    assert fee_of(150 * 10**6, 5 * E18, K, 1250, 100) == 11 * 10**6
    assert fee_of(300 * 10**6, 10 * E18, K, 1250) == 37_500_000  # notionalFeeBps 0 = no cap
    assert fee_of(2_185_069, E18, K, 500) == 109_253  # the testnet E2E fill


def test_preview_fill_prorates_in_the_makers_favour():
    assert preview_fill(G.QUOTE, 3 * E18, 500) == (3_098_550, 154_927)
    sells = G.QUOTE.replace(side=MAKER_SELLS, premium=1_000_001, amount=3)
    assert preview_fill(sells, 1, 500)[0] == 333_334
    with pytest.raises(ValueError):
        preview_fill(G.QUOTE, 0, 500)


def test_maker_cash():
    assert maker_cash(MAKER_BUYS, 30 * 10**6, 10 * E18, K, 1250, 100) == -33_750_000
    assert maker_cash(MAKER_SELLS, 300 * 10**6, 10 * E18, K, 1250, 100) == 278_000_000


@pytest.mark.parametrize("net", [0, 1, 7, 1_000_000, 33_750_000, 250_000_000, 10**12])
@pytest.mark.parametrize("fees", [(1250, 100), (1250, 0), (500, 0), (0, 0)])
def test_premium_for_net(net, fees):
    amount = 10 * E18
    p = premium_for_net(MAKER_BUYS, net, amount, K, *fees)
    assert -maker_cash(MAKER_BUYS, p, amount, K, *fees) <= net
    assert -maker_cash(MAKER_BUYS, p + 1, amount, K, *fees) > net
    p = premium_for_net(MAKER_SELLS, net, amount, K, *fees)
    assert maker_cash(MAKER_SELLS, p, amount, K, *fees) >= net
    assert p == 0 or maker_cash(MAKER_SELLS, p - 1, amount, K, *fees) < net


def test_long_payout_rounds_down():
    call = Series("0x" + "11" * 20, "0x" + "22" * 20, "0x" + "33" * 20, 1, False, 215_000_000)
    put = Series("0x" + "11" * 20, "0x" + "22" * 20, "0x" + "33" * 20, 1, True, 195_000_000)
    p = 227_227_000
    assert long_payout(call, 3 * E18, p) == 3 * E18 * (p - 215_000_000) // p
    assert long_payout(put, 2 * E18, p) == 0
    assert long_payout(put, 2 * E18, 150_000_000) == 90_000_000
