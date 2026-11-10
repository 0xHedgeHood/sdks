"""Opt-in, read-only checks against the deployed testnet settler (eth_call only; nothing is signed or sent):

    HEDGEHOOD_LIVE_RPC=https://rpc.testnet.chain.robinhood.com pytest tests/test_live.py
"""

import os

import goldens as G
import pytest

from hedgehood_maker import eip712 as E
from hedgehood_maker.networks import TESTNET
from hedgehood_maker.rpc import Reader

RPC = os.environ.get("HEDGEHOOD_LIVE_RPC")
pytestmark = pytest.mark.skipif(not RPC, reason="set HEDGEHOOD_LIVE_RPC to run read-only testnet checks")


def test_testnet_settler_computes_our_digest():
    r = Reader.for_network(TESTNET, rpc_url=RPC)
    assert r.chain_id() == TESTNET.chain_id
    q = G.QUOTE.replace(nonce=12345, epoch=7, min_fill=G.QUOTE.amount)
    assert r.quote_digest(q) == E.quote_digest(q, TESTNET.chain_id, TESTNET.settler)


def test_testnet_fee_parameters_are_readable():
    fee_bps, notional_bps = Reader.for_network(TESTNET, rpc_url=RPC).fees()
    assert 0 <= fee_bps <= 10_000 and 0 <= notional_bps <= 10_000
