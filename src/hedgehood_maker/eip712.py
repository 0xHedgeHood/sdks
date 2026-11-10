"""EIP-712 of the RfqSettler quote, byte for byte what `RfqSettler.quoteDigest` computes, plus the gateway login.

Domain: {name "Hedgehood RFQ Settler", version "2", chainId, verifyingContract = settler proxy}.

    Quote(address maker,address signer,address taker,address externalAccount,SeriesParams series,uint8 side,
          uint256 amount,uint256 minFill,uint256 premium,uint64 validUntil,uint256 nonce,uint256 epoch)
    SeriesParams(address underlying,address quote,address oracle,uint64 expiry,bool isPut,uint256 strike,bool physical)

The digest equals the standard EIP-712 digest, so any wallet's signTypedData of `typed_data(...)` works too.
"""

from __future__ import annotations

from typing import Any

from eth_abi import encode
from eth_account import Account
from eth_account.messages import encode_defunct
from eth_keys import keys
from eth_utils import keccak, to_checksum_address

from .constants import DOMAIN_NAME, DOMAIN_VERSION, LOGIN_PREFIX
from .models import Quote, Series, ValidationError

SERIES_FIELDS = ("underlying", "quote", "oracle", "expiry", "isPut", "strike", "physical")
SERIES_TYPES = ("address", "address", "address", "uint64", "bool", "uint256", "bool")
SERIES_TYPE_STRING = (
    "SeriesParams(" + ",".join(f"{t} {f}" for f, t in zip(SERIES_FIELDS, SERIES_TYPES, strict=True)) + ")"
)

QUOTE_FIELDS = ("maker", "signer", "taker", "externalAccount", "series", "side", "amount", "minFill", "premium",
                "validUntil", "nonce", "epoch")
QUOTE_TYPES = ("address", "address", "address", "address", "SeriesParams", "uint8", "uint256", "uint256", "uint256",
               "uint64", "uint256", "uint256")
QUOTE_TYPE_STRING = (
    "Quote(" + ",".join(f"{t} {f}" for f, t in zip(QUOTE_FIELDS, QUOTE_TYPES, strict=True)) + ")" + SERIES_TYPE_STRING
)

DOMAIN_TYPE_STRING = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"

SERIES_TYPEHASH = keccak(text=SERIES_TYPE_STRING)
QUOTE_TYPEHASH = keccak(text=QUOTE_TYPE_STRING)
DOMAIN_TYPEHASH = keccak(text=DOMAIN_TYPE_STRING)

# secp256k1 group order; signatures with s > N/2 are refused (OpenZeppelin ECDSA, the gateway's Recover)
_N = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141


def series_values(s: Series) -> tuple:
    return (s.underlying, s.quote, s.oracle, s.expiry, s.is_put, s.strike, s.physical)


def quote_values(q: Quote) -> tuple:
    """The quote as the settler's ABI tuple (series nested)."""
    return (q.maker, q.signer, q.taker, q.external_account, series_values(q.series), q.side, q.amount, q.min_fill,
            q.premium, q.valid_until, q.nonce, q.epoch)


def series_id(s: Series) -> int:
    """OptionsCore.idOf: uint256(keccak256(abi.encode(params)))."""
    return int.from_bytes(keccak(encode(list(SERIES_TYPES), list(series_values(s)))), "big")


def domain_separator(chain_id: int, settler: str) -> bytes:
    return keccak(encode(
        ["bytes32", "bytes32", "bytes32", "uint256", "address"],
        [DOMAIN_TYPEHASH, keccak(text=DOMAIN_NAME), keccak(text=DOMAIN_VERSION), int(chain_id),
         to_checksum_address(settler)],
    ))


def series_hash(s: Series) -> bytes:
    return keccak(encode(["bytes32", *SERIES_TYPES], [SERIES_TYPEHASH, *series_values(s)]))


def quote_struct_hash(q: Quote) -> bytes:
    types = ["bytes32"] + ["bytes32" if t == "SeriesParams" else t for t in QUOTE_TYPES]
    values = list(quote_values(q))
    values[QUOTE_FIELDS.index("series")] = series_hash(q.series)
    return keccak(encode(types, [QUOTE_TYPEHASH, *values]))


def quote_digest_bytes(q: Quote, chain_id: int, settler: str) -> bytes:
    return keccak(b"\x19\x01" + domain_separator(chain_id, settler) + quote_struct_hash(q))


def quote_digest(q: Quote, chain_id: int, settler: str) -> str:
    """RfqSettler.quoteDigest(q) as 0x hex (lower case, as the gateway's won / fill frames carry it)."""
    return "0x" + quote_digest_bytes(q, chain_id, settler).hex()


def typed_data(q: Quote, chain_id: int, settler: str) -> dict[str, Any]:
    """The quote as EIP-712 typed data for a wallet (eth_account encode_typed_data, viem / ethers signTypedData)."""
    return {
        "types": {
            "EIP712Domain": [{"name": "name", "type": "string"}, {"name": "version", "type": "string"},
                             {"name": "chainId", "type": "uint256"}, {"name": "verifyingContract", "type": "address"}],
            "Quote": [{"name": f, "type": t} for f, t in zip(QUOTE_FIELDS, QUOTE_TYPES, strict=True)],
            "SeriesParams": [{"name": f, "type": t} for f, t in zip(SERIES_FIELDS, SERIES_TYPES, strict=True)],
        },
        "primaryType": "Quote",
        "domain": {"name": DOMAIN_NAME, "version": DOMAIN_VERSION, "chainId": int(chain_id),
                   "verifyingContract": to_checksum_address(settler)},
        "message": {
            **dict(zip(QUOTE_FIELDS, quote_values(q), strict=True)),
            "series": dict(zip(SERIES_FIELDS, series_values(q.series), strict=True)),
        },
    }


def _rsv(r: int, s: int, v: int) -> str:
    return "0x" + r.to_bytes(32, "big").hex() + s.to_bytes(32, "big").hex() + bytes([v]).hex()


def sign_digest(digest: bytes, private_key: Any) -> str:
    """65-byte r || s || v (v = 27 / 28) signature of a 32-byte digest."""
    signed = Account.unsafe_sign_hash(digest, private_key)
    return _rsv(signed.r, signed.s, signed.v)


def sign_quote(q: Quote, chain_id: int, settler: str, private_key: Any) -> str:
    """The signature the settler checks (SignatureChecker: ECDSA for an EOA signer)."""
    return sign_digest(quote_digest_bytes(q, chain_id, settler), private_key)


def recover_digest(digest: bytes, signature: str | bytes) -> str:
    """The signer of a 65-byte r || s || v signature (v 27/28 or 0/1); high-s refused as on chain and in the gateway."""
    sig = bytes.fromhex(signature[2:] if signature.startswith("0x") else signature) if isinstance(signature, str) \
        else bytes(signature)
    if len(sig) != 65:
        raise ValidationError("signature must be 65 bytes")
    r, s, v = int.from_bytes(sig[:32], "big"), int.from_bytes(sig[32:64], "big"), sig[64]
    if v >= 27:
        v -= 27
    if v not in (0, 1) or not (0 < r < _N) or not (0 < s <= _N // 2):
        raise ValidationError("bad r, s or v")
    pub = keys.Signature(vrs=(v, r, s)).recover_public_key_from_msg_hash(digest)
    return pub.to_checksum_address()


def recover_quote_signer(q: Quote, chain_id: int, settler: str, signature: str | bytes) -> str:
    return recover_digest(quote_digest_bytes(q, chain_id, settler), signature)


# ---------------------------------------------------------------- gateway login (EIP-191)


def login_message(chain_id: int, maker: str, nonce: str) -> str:
    """What the signer personal_signs to log in: hedgehood-rfq-login:<chainId>:<maker lower-case>:<nonce>."""
    return f"{LOGIN_PREFIX}:{int(chain_id)}:{to_checksum_address(maker).lower()}:{nonce}"


def sign_login(chain_id: int, maker: str, nonce: str, private_key: Any) -> str:
    sig = Account.sign_message(encode_defunct(text=login_message(chain_id, maker, nonce)), private_key).signature
    return "0x" + bytes(sig).hex()


def recover_login(chain_id: int, maker: str, nonce: str, signature: str) -> str:
    return Account.recover_message(encode_defunct(text=login_message(chain_id, maker, nonce)), signature=signature)
