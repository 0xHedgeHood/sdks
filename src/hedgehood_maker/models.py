"""Typed models of the settler's structs and the gateway's WebSocket frames, with validation.

JSON on the wire (as the gateway reads and writes it): uint256 fields (strike, amount, minFill, premium, nonce, epoch)
are decimal strings; uint64 / uint8 fields (expiry, validUntil, side, chainId) are JSON numbers; addresses are 0x hex in
any case. The gateway also accepts bare numbers for uint256 fields, but rejects strings for the uint64 ones.
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from typing import Any

from eth_utils import is_address, to_checksum_address

from .constants import MAKER_BUYS, MAKER_SELLS, UINT64_MAX, UINT256_MAX, ZERO_ADDRESS


class ValidationError(ValueError):
    """A value does not fit the settler's types or the gateway's rules."""


def address(value: Any, name: str = "address") -> str:
    if not isinstance(value, str) or not is_address(value):
        raise ValidationError(f"{name}: not an address: {value!r}")
    return to_checksum_address(value)


def uint(value: Any, name: str = "value", bits: int = 256) -> int:
    """An unsigned integer from an int or a decimal string (no floats, no bools, no hex)."""
    if isinstance(value, bool):
        raise ValidationError(f"{name}: bool is not an integer")
    if isinstance(value, int):
        v = value
    elif isinstance(value, str) and value.isdigit():
        v = int(value, 10)
    else:
        raise ValidationError(f"{name}: not an unsigned integer: {value!r}")
    if v < 0 or v > (UINT64_MAX if bits == 64 else UINT256_MAX if bits == 256 else 2**bits - 1):
        raise ValidationError(f"{name}: out of uint{bits} range: {v}")
    return v


def _bool(value: Any, name: str) -> bool:
    if not isinstance(value, bool):
        raise ValidationError(f"{name}: not a bool: {value!r}")
    return value


def _side(value: Any) -> int:
    v = uint(value, "side", 8)
    if v not in (MAKER_BUYS, MAKER_SELLS):
        raise ValidationError(f"side must be 0 (MakerBuys) or 1 (MakerSells), got {v}")
    return v


@dataclass(frozen=True)
class Series:
    """OptionsCore.SeriesParams. Strike is quote raw (USDG 1e6) per whole underlying token."""

    underlying: str
    quote: str
    oracle: str
    expiry: int  # unix seconds, the 16:00 New York close of a listed weekday
    is_put: bool
    strike: int
    physical: bool = False

    def __post_init__(self) -> None:
        object.__setattr__(self, "underlying", address(self.underlying, "series.underlying"))
        object.__setattr__(self, "quote", address(self.quote, "series.quote"))
        object.__setattr__(self, "oracle", address(self.oracle, "series.oracle"))
        object.__setattr__(self, "expiry", uint(self.expiry, "series.expiry", 64))
        object.__setattr__(self, "is_put", _bool(self.is_put, "series.isPut"))
        object.__setattr__(self, "strike", uint(self.strike, "series.strike"))
        object.__setattr__(self, "physical", _bool(self.physical, "series.physical"))

    @property
    def id(self) -> int:
        """OptionsCore.idOf, also the ERC-6909 id of the long."""
        from .eip712 import series_id

        return series_id(self)

    def to_json(self) -> dict[str, Any]:
        return {
            "underlying": self.underlying,
            "quote": self.quote,
            "oracle": self.oracle,
            "expiry": self.expiry,
            "isPut": self.is_put,
            "strike": str(self.strike),
            "physical": self.physical,
        }

    @classmethod
    def from_json(cls, d: dict[str, Any]) -> Series:
        if not isinstance(d, dict):
            raise ValidationError("series: not an object")
        return cls(
            underlying=d.get("underlying"),
            quote=d.get("quote"),
            oracle=d.get("oracle"),
            expiry=d.get("expiry"),
            is_put=d.get("isPut"),
            strike=d.get("strike"),
            physical=d.get("physical", False),
        )


@dataclass(frozen=True)
class Quote:
    """IRfqSettler.Quote: a firm quote the maker (or its delegated signer) signs under EIP-712."""

    maker: str
    signer: str
    taker: str
    series: Series
    side: int
    amount: int  # underlying raw (1e18 = one token)
    min_fill: int
    premium: int  # total quote raw for `amount`, fee excluded
    valid_until: int  # unix seconds
    nonce: int
    epoch: int = 0
    external_account: str = ZERO_ADDRESS  # zero = the maker's settler balance; else that wallet, by allowance

    def __post_init__(self) -> None:
        object.__setattr__(self, "maker", address(self.maker, "maker"))
        object.__setattr__(self, "signer", address(self.signer, "signer"))
        object.__setattr__(self, "taker", address(self.taker, "taker"))
        object.__setattr__(self, "external_account", address(self.external_account, "externalAccount"))
        if not isinstance(self.series, Series):
            raise ValidationError("series: not a Series")
        object.__setattr__(self, "side", _side(self.side))
        object.__setattr__(self, "amount", uint(self.amount, "amount"))
        object.__setattr__(self, "min_fill", uint(self.min_fill, "minFill"))
        object.__setattr__(self, "premium", uint(self.premium, "premium"))
        object.__setattr__(self, "valid_until", uint(self.valid_until, "validUntil", 64))
        object.__setattr__(self, "nonce", uint(self.nonce, "nonce"))
        object.__setattr__(self, "epoch", uint(self.epoch, "epoch"))

    def replace(self, **changes: Any) -> Quote:
        return replace(self, **changes)

    def to_json(self) -> dict[str, Any]:
        return {
            "maker": self.maker,
            "signer": self.signer,
            "taker": self.taker,
            "externalAccount": self.external_account,
            "series": self.series.to_json(),
            "side": self.side,
            "amount": str(self.amount),
            "minFill": str(self.min_fill),
            "premium": str(self.premium),
            "validUntil": self.valid_until,
            "nonce": str(self.nonce),
            "epoch": str(self.epoch),
        }

    @classmethod
    def from_json(cls, d: dict[str, Any]) -> Quote:
        if not isinstance(d, dict):
            raise ValidationError("quote: not an object")
        return cls(
            maker=d.get("maker"),
            signer=d.get("signer"),
            taker=d.get("taker"),
            external_account=d.get("externalAccount") or ZERO_ADDRESS,
            series=Series.from_json(d.get("series")),
            side=d.get("side"),
            amount=d.get("amount"),
            min_fill=d.get("minFill"),
            premium=d.get("premium"),
            valid_until=d.get("validUntil"),
            nonce=d.get("nonce"),
            epoch=d.get("epoch", 0),
        )


# ---------------------------------------------------------------- server -> maker frames


@dataclass(frozen=True)
class Frame:
    raw: dict[str, Any] = field(repr=False, compare=False)

    @property
    def type(self) -> str:
        return str(self.raw.get("type", ""))


@dataclass(frozen=True)
class Challenge(Frame):
    nonce: str = ""
    chain_id: int = 0


@dataclass(frozen=True)
class Ready(Frame):
    maker: str = ZERO_ADDRESS
    settler: str = ZERO_ADDRESS
    fee_bps: int = 0
    notional_fee_bps: int = 0
    epoch: int = 0


@dataclass(frozen=True)
class RFQ(Frame):
    """A taker's request as every maker sees it. Answer before `respond_by_ms` with validUntil >= `min_valid_until`."""

    rfq_id: str = ""
    chain_id: int = 0
    settler: str = ZERO_ADDRESS
    taker: str = ZERO_ADDRESS
    side: int = MAKER_BUYS
    series: Series | None = None
    amount: int = 0
    respond_by_ms: int = 0  # unix milliseconds
    min_valid_until: int = 0  # unix seconds

    @property
    def maker_buys(self) -> bool:
        return self.side == MAKER_BUYS


@dataclass(frozen=True)
class Won(Frame):
    rfq_id: str = ""
    digest: str = ""


@dataclass(frozen=True)
class Lost(Frame):
    rfq_id: str = ""


@dataclass(frozen=True)
class Fill(Frame):
    digest: str = ""
    tx_hash: str = ""
    amount: int = 0
    rfq_id: str = ""


@dataclass(frozen=True)
class Rejected(Frame):
    rfq_id: str = ""
    code: str = ""
    reason: str = ""


@dataclass(frozen=True)
class ErrorFrame(Frame):
    code: str = ""
    reason: str = ""
    rfq_id: str = ""


@dataclass(frozen=True)
class Paused(Frame):
    pass


@dataclass(frozen=True)
class Resumed(Frame):
    pass


@dataclass(frozen=True)
class Unknown(Frame):
    pass


def parse_frame(d: dict[str, Any]) -> Frame:
    """A server frame as its model. Raises ValidationError on a malformed known frame."""
    if not isinstance(d, dict):
        raise ValidationError("frame: not an object")
    t = d.get("type")
    rid = str(d.get("rfqId", ""))
    if t == "challenge":
        return Challenge(d, nonce=str(d.get("nonce", "")), chain_id=uint(d.get("chainId"), "chainId", 64))
    if t == "ready":
        return Ready(
            d,
            maker=address(d.get("maker"), "maker"),
            settler=address(d.get("settler"), "settler"),
            fee_bps=uint(d.get("feeBps", 0), "feeBps"),
            notional_fee_bps=uint(d.get("notionalFeeBps", 0), "notionalFeeBps"),
            epoch=uint(d.get("epoch", 0), "epoch"),
        )
    if t == "rfq":
        if not rid:
            raise ValidationError("rfq: missing rfqId")
        return RFQ(
            d,
            rfq_id=rid,
            chain_id=uint(d.get("chainId"), "chainId", 64),
            settler=address(d.get("settler"), "settler"),
            taker=address(d.get("taker"), "taker"),
            side=_side(d.get("side")),
            series=Series.from_json(d.get("series")),
            amount=uint(d.get("amount"), "amount"),
            respond_by_ms=uint(d.get("respondBy"), "respondBy", 64),
            min_valid_until=uint(d.get("minValidUntil"), "minValidUntil", 64),
        )
    if t == "won":
        return Won(d, rfq_id=rid, digest=str(d.get("digest", "")).lower())
    if t == "lost":
        return Lost(d, rfq_id=rid)
    if t == "fill":
        return Fill(
            d,
            digest=str(d.get("digest", "")).lower(),
            tx_hash=str(d.get("txHash", "")),
            amount=uint(d.get("amount", 0), "amount"),
            rfq_id=rid,
        )
    if t == "rejected":
        return Rejected(d, rfq_id=rid, code=str(d.get("code", "")), reason=str(d.get("reason", "")))
    if t == "error":
        return ErrorFrame(d, code=str(d.get("code", "")), reason=str(d.get("reason", "")), rfq_id=rid)
    if t == "paused":
        return Paused(d)
    if t == "resumed":
        return Resumed(d)
    return Unknown(d)


# ---------------------------------------------------------------- maker -> server frames


@dataclass(frozen=True)
class Decline:
    """A pricer's refusal; `reason` is a short code the gateway records (e.g. "no_price", "no_inventory")."""

    reason: str = "no_price"


def login_frame(maker: str, signer: str, signature: str) -> dict[str, Any]:
    return {"type": "login", "maker": address(maker, "maker"), "signer": address(signer, "signer"),
            "signature": signature}


def quote_frame(rfq_id: str, quote: Quote, signature: str) -> dict[str, Any]:
    return {"type": "quote", "rfqId": rfq_id, "quote": quote.to_json(), "signature": signature}


def decline_frame(rfq_id: str, reason: str) -> dict[str, Any]:
    return {"type": "decline", "rfqId": rfq_id, "reason": reason or "declined"}
