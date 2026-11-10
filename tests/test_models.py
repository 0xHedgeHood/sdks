import json

import goldens as G
import pytest

from hedgehood_maker import eip712 as E
from hedgehood_maker.models import (
    RFQ,
    Challenge,
    ErrorFrame,
    Fill,
    Lost,
    Paused,
    Quote,
    Ready,
    Rejected,
    Resumed,
    Series,
    Unknown,
    ValidationError,
    Won,
    decline_frame,
    parse_frame,
    quote_frame,
)


def test_quote_json_types_are_what_the_gateway_parses():
    j = G.QUOTE.to_json()
    # uint256 as decimal strings, uint64 / uint8 as numbers (Go: quote.Dec vs uint64)
    assert j["amount"] == "5000000000000000000" and j["premium"] == "5164250" and j["nonce"] == "42"
    assert j["epoch"] == "0" and j["minFill"] == "0" and j["series"]["strike"] == "215000000"
    assert j["validUntil"] == 1793766600 and j["series"]["expiry"] == 1793826000 and j["side"] == 0
    assert j["series"]["physical"] is False and j["externalAccount"] == "0x" + "00" * 20
    back = Quote.from_json(json.loads(json.dumps(j)))
    assert back == G.QUOTE
    assert E.quote_digest(back, G.CHAIN, G.SETTLER) == G.DIGEST


def test_quote_from_json_accepts_bare_numbers_and_lower_case():
    j = G.QUOTE.to_json()
    j.update(amount=5 * 10**18, maker=j["maker"].lower())
    assert Quote.from_json(j) == G.QUOTE


@pytest.mark.parametrize("change", [
    {"side": 2}, {"amount": "-1"}, {"amount": 1.5}, {"premium": "0x10"}, {"maker": "0x1234"},
    {"validUntil": 2**64}, {"nonce": 2**256}, {"amount": True},
])
def test_quote_validation(change):
    j = G.QUOTE.to_json()
    j.update(change)
    with pytest.raises(ValidationError):
        Quote.from_json(j)


def test_series_validation():
    with pytest.raises(ValidationError):
        Series.from_json(dict(G.SERIES.to_json(), isPut="false"))
    assert Series.from_json({k: v for k, v in G.SERIES.to_json().items() if k != "physical"}) == G.SERIES


def test_parse_server_frames():
    rfq = parse_frame({
        "type": "rfq", "rfqId": "19a6c1f2b3c-0011223344556677", "chainId": 46630,
        "settler": "0xda43b0933031e41d5b367e1807536fcf4ebdee7e", "taker": "0x36437b878415eda1a24186cf79afffbc9ecED298",
        "side": 0, "series": {"underlying": "0x6661d20c2e343bb78b42535eb060c3058c744f3f",
                              "quote": "0x850de815d41f6983aaf09247f9a465c0aefff04c",
                              "oracle": "0x9e6c529caa69d44b63df27483c57f97a42d6e898", "expiry": 1794603600,
                              "isPut": True, "strike": "330000000", "physical": False},
        "amount": "1000000000000000000", "respondBy": 1794500000250, "minValidUntil": 1794500015,
    })
    assert isinstance(rfq, RFQ) and rfq.maker_buys and rfq.series.strike == 330_000_000
    assert rfq.settler == "0xda43b0933031e41D5B367E1807536FcF4ebdEE7e" and rfq.amount == 10**18
    ready = parse_frame({"type": "ready", "maker": "0x" + "11" * 20, "settler": "0x" + "22" * 20, "feeBps": 1250,
                         "notionalFeeBps": 100, "epoch": "3"})
    assert isinstance(ready, Ready) and ready.epoch == 3 and ready.notional_fee_bps == 100
    assert isinstance(parse_frame({"type": "challenge", "nonce": "ab", "chainId": 46630}), Challenge)
    won = parse_frame({"type": "won", "rfqId": "r", "digest": "0xABCD"})
    assert isinstance(won, Won) and won.digest == "0xabcd"
    fill = parse_frame({"type": "fill", "digest": "0xab", "txHash": "0xcd", "amount": "5", "rfqId": "r"})
    assert isinstance(fill, Fill) and fill.amount == 5 and fill.rfq_id == "r"
    assert isinstance(parse_frame({"type": "lost", "rfqId": "r"}), Lost)
    rej = parse_frame({"type": "rejected", "rfqId": "r", "code": "stale_epoch", "reason": "x"})
    assert isinstance(rej, Rejected) and rej.code == "stale_epoch"
    assert isinstance(parse_frame({"type": "error", "code": "login", "reason": "x"}), ErrorFrame)
    assert isinstance(parse_frame({"type": "paused"}), Paused)
    assert isinstance(parse_frame({"type": "resumed"}), Resumed)
    assert isinstance(parse_frame({"type": "something-new"}), Unknown)
    with pytest.raises(ValidationError):
        parse_frame({"type": "rfq", "rfqId": "r", "side": 0})


def test_outgoing_frames():
    f = quote_frame("r1", G.QUOTE, G.SIGNATURE)
    assert f["type"] == "quote" and f["quote"]["premium"] == "5164250" and f["signature"] == G.SIGNATURE
    assert decline_frame("r1", "") == {"type": "decline", "rfqId": "r1", "reason": "declined"}
