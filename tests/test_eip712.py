import random

import goldens as G
import pytest
from eth_account import Account
from eth_account.messages import encode_defunct, encode_typed_data
from eth_utils import keccak

from hedgehood_maker import eip712 as E
from hedgehood_maker.models import Quote, Series, ValidationError
from hedgehood_maker.settler import encode_fill

N = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141


def typed_hash(td):
    m = encode_typed_data(full_message=td)
    return "0x" + keccak(b"\x19" + m.version + m.header + m.body).hex()


def test_type_strings_are_the_settlers():
    assert E.SERIES_TYPE_STRING == G.SOLIDITY_SERIES_TYPE
    assert E.QUOTE_TYPE_STRING == G.SOLIDITY_QUOTE_TYPE


def test_digest_matches_solidity_go_and_desk_goldens():
    assert E.quote_digest(G.QUOTE, G.CHAIN, G.SETTLER) == G.DIGEST


def test_digest_equals_the_standard_typed_data_hash():
    assert typed_hash(E.typed_data(G.QUOTE, G.CHAIN, G.SETTLER)) == G.DIGEST


def test_series_id_golden():
    assert E.series_id(G.SERIES) == G.SERIES_ID == G.SERIES.id


def test_signature_golden_and_recovery():
    assert E.sign_quote(G.QUOTE, G.CHAIN, G.SETTLER, G.KEY_7) == G.SIGNATURE
    assert E.recover_quote_signer(G.QUOTE, G.CHAIN, G.SETTLER, G.SIGNATURE) == Account.from_key(G.KEY_7).address


def test_high_s_twin_is_refused():
    sig = bytes.fromhex(G.SIGNATURE[2:])
    s = int.from_bytes(sig[32:64], "big")
    twin = sig[:32] + (N - s).to_bytes(32, "big") + bytes([sig[64] ^ 1])
    with pytest.raises(ValidationError):
        E.recover_quote_signer(G.QUOTE, G.CHAIN, G.SETTLER, "0x" + twin.hex())


def test_fill_and_close_calldata_match_go_and_desk():
    fill = encode_fill(G.QUOTE, G.SIGNATURE, 5 * 10**18)
    close = encode_fill(G.QUOTE, G.SIGNATURE, 10**18, close=True)
    assert "0x" + keccak(bytes.fromhex(fill[2:])).hex() == G.FILL_CALLDATA_HASH
    assert "0x" + keccak(bytes.fromhex(close[2:])).hex() == G.CLOSE_CALLDATA_HASH


def test_testnet_domain_separator():
    assert "0x" + E.domain_separator(46630, "0xda43b0933031e41D5B367E1807536FcF4ebdEE7e").hex() == \
        G.TESTNET_DOMAIN_SEPARATOR


def _addr(rng):
    return "0x" + rng.randbytes(20).hex()


@pytest.mark.parametrize("seed", range(8))
def test_random_quotes_hash_like_eth_account(seed):
    rng = random.Random(seed)
    q = Quote(
        maker=_addr(rng), signer=_addr(rng), taker=_addr(rng),
        external_account=rng.choice(["0x" + "00" * 20, _addr(rng)]),
        series=Series(_addr(rng), _addr(rng), _addr(rng), rng.getrandbits(64), rng.random() < 0.5,
                      rng.getrandbits(256), rng.random() < 0.5),
        side=rng.randint(0, 1), amount=rng.getrandbits(256), min_fill=rng.getrandbits(128),
        premium=rng.getrandbits(200), valid_until=rng.getrandbits(64), nonce=rng.getrandbits(256),
        epoch=rng.getrandbits(32),
    )
    settler, chain = _addr(rng), rng.randint(1, 2**32)
    assert E.quote_digest(q, chain, settler) == typed_hash(E.typed_data(q, chain, settler))
    key = Account.create().key
    sig = E.sign_quote(q, chain, settler, key)
    assert E.recover_quote_signer(q, chain, settler, sig) == Account.from_key(key).address
    # signing the typed data with a wallet gives the same signer
    wallet_sig = Account.sign_typed_data(key, full_message=E.typed_data(q, chain, settler)).signature
    assert E.recover_quote_signer(q, chain, settler, wallet_sig) == Account.from_key(key).address


def test_login_message_matches_the_gateway():
    maker = "0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308"
    assert E.login_message(46630, maker, "ab12") == f"hedgehood-rfq-login:46630:{maker.lower()}:ab12"
    acct = Account.create()
    sig = E.sign_login(46630, maker, "ab12", acct.key)
    msg = encode_defunct(text=f"hedgehood-rfq-login:46630:{maker.lower()}:ab12")
    assert Account.recover_message(msg, signature=sig) == acct.address
    assert E.recover_login(46630, maker.lower(), "ab12", sig) == acct.address
