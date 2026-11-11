"""Unsigned transaction builders and read-only calls. Nothing is signed or sent: the RPC transport is a fake."""

import goldens as G
import pytest
from eth_abi import decode, encode

from hedgehood_maker import settler as S
from hedgehood_maker.eip712 import quote_values
from hedgehood_maker.networks import TESTNET
from hedgehood_maker.nonces import NonceManager, masks, split
from hedgehood_maker.rpc import Reader, RPCError

MAKER = "0x36437b878415EdA1a24186CF79AFffBc9ecEd298"
HOT = "0xa6E328D981E199bD7A9C466AC3684679CF3a6D97"


@pytest.mark.parametrize("name,signature", [
    ("setSigner", S.SET_SIGNER), ("deposit", S.DEPOSIT), ("requestWithdraw", S.REQUEST_WITHDRAW),
    ("withdraw", S.WITHDRAW), ("increaseEpoch", S.INCREASE_EPOCH), ("invalidateNonces", S.INVALIDATE_NONCES),
    ("linkMaker", S.LINK_MAKER), ("fill", S.FILL), ("close", S.CLOSE), ("epoch", S.EPOCH),
    ("signerExpiry", S.SIGNER_EXPIRY), ("nonceUsed", S.NONCE_USED), ("settlerBalanceOf", S.SETTLER_BALANCE),
    ("pendingWithdraw", S.PENDING_WITHDRAW), ("linked", S.LINKED), ("feeBps", S.FEE_BPS),
    ("notionalFeeBps", S.NOTIONAL_FEE_BPS), ("quoteDigest", S.QUOTE_DIGEST), ("previewFill", S.PREVIEW_FILL),
    ("isListed", S.IS_LISTED), ("core.setOperator", S.SET_OPERATOR), ("core.isOperator", S.IS_OPERATOR),
    ("core.balanceOf", S.LONG_BALANCE), ("core.redeemLong", S.REDEEM_LONG), ("erc20.approve", S.APPROVE),
    ("erc20.allowance", S.ALLOWANCE), ("erc20.balanceOf", S.ERC20_BALANCE),
])
def test_selectors_match_the_compiled_contracts(name, signature):
    assert S.selector(signature).hex() == G.SELECTORS[name]


def args_of(tx):
    return decode(S._arg_types(tx.function), bytes.fromhex(tx.data[10:]))


def test_onboarding_transactions():
    b = S.SettlerTxs.for_network(TESTNET)
    usdg = TESTNET.quote_token

    t = b.set_signer(HOT, 1_800_000_000, maker=MAKER)
    assert t.to == TESTNET.settler and t.data.startswith("0x07843c87") and t.sender == MAKER
    assert args_of(t) == (HOT.lower(), 1_800_000_000)
    assert t.to_dict() == {"chainId": 46630, "to": TESTNET.settler, "data": t.data, "value": 0, "from": MAKER}
    assert t.cast().startswith(f'cast send {TESTNET.settler} "setSigner(address,uint64)" "{HOT}" "1800000000"')

    t = b.approve(usdg, 1000 * 10**6, owner=MAKER)
    assert t.to == usdg and args_of(t) == (TESTNET.settler.lower(), 1000 * 10**6)
    t = b.deposit(usdg, MAKER, 1000 * 10**6)
    assert args_of(t) == (usdg.lower(), MAKER.lower(), 1000 * 10**6)
    assert args_of(b.request_withdraw(usdg, 5)) == (usdg.lower(), 5)
    assert args_of(b.withdraw(usdg, MAKER)) == (usdg.lower(), MAKER.lower())
    assert args_of(b.increase_epoch(MAKER)) == (MAKER.lower(),)
    assert args_of(b.invalidate_nonces(MAKER, 7, 0b101)) == (MAKER.lower(), 7, 5)
    assert args_of(b.link_maker(MAKER, True, account=HOT)) == (MAKER.lower(), True)
    t = b.set_operator(maker=MAKER)
    assert t.to == TESTNET.core and args_of(t) == (TESTNET.settler.lower(), True)
    t = b.redeem_long(G.SERIES, 10**18, MAKER, MAKER)
    assert t.to == TESTNET.core and t.data.startswith("0xf8ad1b36")


def test_builders_validate():
    b = S.SettlerTxs.for_network(TESTNET)
    with pytest.raises(ValueError):
        b.set_signer(HOT, 2**64)
    with pytest.raises(ValueError):
        b.deposit(TESTNET.quote_token, "0x1234", 1)
    with pytest.raises(ValueError):
        S.SettlerTxs(46630, TESTNET.settler).set_operator()


def test_nonces_are_unique_and_roll_slots():
    m = NonceManager(start_slot=5)
    got = [m.next() for _ in range(300)]
    assert len(set(got)) == 300
    assert split(got[0]) == (5, 0) and split(got[255]) == (5, 255) and split(got[256]) == (6, 0)
    assert masks(got) == {5: 2**256 - 1, 6: 2**44 - 1}
    assert NonceManager().next() != NonceManager().next()  # random start slots


class FakeRPC:
    """Answers eth_call by selector; records requests."""

    def __init__(self, answers):
        self.answers, self.requests = answers, []

    def __call__(self, payload):
        self.requests.append(payload)
        if payload["method"] == "eth_chainId":
            return {"result": "0xb626"}
        sel = payload["params"][0]["data"][2:10]
        if sel not in self.answers:
            return {"error": {"code": -32000, "message": "execution reverted"}}
        return {"result": "0x" + self.answers[sel].hex()}


def test_reader_decodes_settler_state():
    rpc = FakeRPC({
        "8b810c36": encode(["uint256"], [3]),
        "24a9d853": encode(["uint256"], [1250]),
        "1faa35a9": encode(["uint256"], [100]),
        "e57def0c": encode(["uint64"], [1_800_000_000]),
        "1d971f5d": bytes.fromhex(G.DIGEST[2:]),
        "f59a193d": encode(["uint256", "uint256"], [3_098_550, 154_927]),
        "b6363cf2": encode(["bool"], [True]),
    })
    r = Reader.for_network(TESTNET, transport=rpc)
    assert r.chain_id() == 46630
    assert r.epoch(MAKER) == 3 and r.fees() == (1250, 100) and r.signer_expiry(MAKER, HOT) == 1_800_000_000
    assert r.quote_digest(G.QUOTE) == G.DIGEST
    assert r.preview_fill(G.QUOTE, 3 * 10**18) == (3_098_550, 154_927)
    assert r.is_operator(MAKER) is True
    call = rpc.requests[-3]["params"][0]
    assert call["to"] == TESTNET.settler and call["data"] == S.calldata(S.QUOTE_DIGEST, quote_values(G.QUOTE))
    assert all(p["method"] in ("eth_call", "eth_chainId") for p in rpc.requests)  # reads only
    with pytest.raises(RPCError):
        r.nonce_used(MAKER, 1)


def test_can_sell_needs_the_long_and_the_operator():
    from hedgehood_maker.eip712 import series_id
    series = G.QUOTE.series
    held = {"value": 2 * 10**18}
    operator = {"value": True}

    class Rpc(FakeRPC):
        def __call__(self, payload):
            sel = payload["params"][0]["data"][2:10] if payload["method"] == "eth_call" else ""
            if sel == S.selector(S.LONG_BALANCE).hex():
                self.requests.append(payload)
                return {"result": "0x" + encode(["uint256"], [held["value"]]).hex()}
            if sel == S.selector(S.IS_OPERATOR).hex():
                return {"result": "0x" + encode(["bool"], [operator["value"]]).hex()}
            return super().__call__(payload)

    rpc = Rpc({})
    r = Reader.for_network(TESTNET, transport=rpc)
    assert r.can_sell(MAKER, series, 2 * 10**18) is None
    data = rpc.requests[-1]["params"][0]["data"]
    assert data == S.calldata(S.LONG_BALANCE, MAKER, series_id(series))
    assert "holds" in r.can_sell(MAKER, series, 3 * 10**18)
    operator["value"] = False
    assert "operator" in r.can_sell(MAKER, series, 10**18)
