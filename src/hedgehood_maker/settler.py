"""Calldata and UNSIGNED transactions for the maker's on-chain setup. Nothing here signs or sends: hand the result to
your own wallet, keystore or multisig (each UnsignedTx also renders as a `cast send` line to run yourself).

Onboarding, in order (docs/MAKER_GUIDE.md):

1. `set_signer(hot_key, expires_at)` from the maker: lets a hot key sign quotes and log in for the maker.
2. Funding, either
   a. settler balance: `approve(usdg, settler, amount)` + `deposit(usdg, maker, amount)`, or
   b. wallet by allowance: `approve(usdg, settler, amount)` from the paying wallet, plus `link_maker(maker, True)` from
      that wallet when it is not the maker (quotes then carry externalAccount = that wallet).
3. For MakerSells quotes (selling longs you hold): `set_operator(settler, True)` on OptionsCore.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from eth_abi import encode
from eth_utils import keccak, to_checksum_address

from .eip712 import QUOTE_TYPES, SERIES_TYPES, quote_values, series_values
from .models import Quote, Series, uint
from .networks import Network

SERIES_TUPLE = "(" + ",".join(SERIES_TYPES) + ")"
QUOTE_TUPLE = "(" + ",".join(SERIES_TUPLE if t == "SeriesParams" else t for t in QUOTE_TYPES) + ")"

# RfqSettler
SET_SIGNER = "setSigner(address,uint64)"
DEPOSIT = "deposit(address,address,uint256)"
REQUEST_WITHDRAW = "requestWithdraw(address,uint256)"
WITHDRAW = "withdraw(address,address)"
INCREASE_EPOCH = "increaseEpoch(address)"
INVALIDATE_NONCES = "invalidateNonces(address,uint256,uint256)"
LINK_MAKER = "linkMaker(address,bool)"
FILL = f"fill({QUOTE_TUPLE},bytes,uint256)"
CLOSE = f"close({QUOTE_TUPLE},bytes,uint256)"
# RfqSettler views
EPOCH = "epoch(address)"
SIGNER_EXPIRY = "signerExpiry(address,address)"
NONCE_USED = "nonceUsed(address,uint256)"
SETTLER_BALANCE = "balanceOf(address,address)"  # (token, account)
PENDING_WITHDRAW = "pendingWithdraw(address,address)"
LINKED = "linked(address,address)"  # (account, maker)
FEE_BPS = "feeBps()"
NOTIONAL_FEE_BPS = "notionalFeeBps()"
QUOTE_DIGEST = f"quoteDigest({QUOTE_TUPLE})"
PREVIEW_FILL = f"previewFill({QUOTE_TUPLE},uint256)"
IS_LISTED = f"isListed({SERIES_TUPLE})"
# OptionsCore (ERC-6909 longs)
SET_OPERATOR = "setOperator(address,bool)"
IS_OPERATOR = "isOperator(address,address)"  # (owner, spender)
LONG_BALANCE = "balanceOf(address,uint256)"  # (owner, id)
REDEEM_LONG = f"redeemLong({SERIES_TUPLE},uint256,address,address)"
# ERC-20
APPROVE = "approve(address,uint256)"
ERC20_BALANCE = "balanceOf(address)"
ALLOWANCE = "allowance(address,address)"


def selector(signature: str) -> bytes:
    return keccak(text=signature)[:4]


def _arg_types(signature: str) -> list[str]:
    """Top-level argument types of a function signature, tuples kept whole."""
    inner = signature[signature.index("(") + 1 : -1]
    out, depth, cur = [], 0, ""
    for ch in inner:
        if ch == "," and depth == 0:
            out.append(cur)
            cur = ""
            continue
        depth += ch == "("
        depth -= ch == ")"
        cur += ch
    if cur:
        out.append(cur)
    return out


def calldata(signature: str, *args: Any) -> str:
    """0x calldata of `signature(args...)`."""
    return "0x" + (selector(signature) + encode(_arg_types(signature), list(args))).hex()


def encode_fill(q: Quote, signature: str, amount: int, close: bool = False) -> str:
    """RfqSettler.fill (or close) calldata: what the taker sends (the gateway returns it as fillCalldata)."""
    return calldata(CLOSE if close else FILL, quote_values(q), bytes.fromhex(signature.removeprefix("0x")), int(amount))


def _cast_arg(v: Any) -> str:
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, (tuple, list)):
        return "(" + ",".join(_cast_arg(x) for x in v) + ")"
    return str(v)


@dataclass(frozen=True)
class UnsignedTx:
    """A transaction to review, sign and send yourself. `sender` is who must send it."""

    chain_id: int
    to: str
    data: str
    description: str
    sender: str | None = None
    function: str = ""
    args: tuple = field(default=())
    value: int = 0

    def to_dict(self) -> dict[str, Any]:
        """Fields for eth_account / web3 (add nonce and gas, then sign with your own key)."""
        d: dict[str, Any] = {"chainId": self.chain_id, "to": self.to, "data": self.data, "value": self.value}
        if self.sender:
            d["from"] = self.sender
        return d

    def cast(self, rpc: str = "$RPC") -> str:
        """The equivalent Foundry command, to run yourself with your own key or keystore."""
        args = " ".join(f'"{_cast_arg(a)}"' for a in self.args)
        return f'cast send {self.to} "{self.function}" {args} --rpc-url {rpc} --account <your-keystore>'.replace(
            "  ", " ")


class SettlerTxs:
    """Builders for one deployment's settler and core."""

    def __init__(self, chain_id: int, settler: str, core: str | None = None):
        self.chain_id = int(chain_id)
        self.settler = to_checksum_address(settler)
        self.core = to_checksum_address(core) if core else None

    @classmethod
    def for_network(cls, n: Network) -> SettlerTxs:
        return cls(n.chain_id, n.settler, n.core)

    def _tx(self, to: str, fn: str, args: tuple, description: str, sender: str | None) -> UnsignedTx:
        return UnsignedTx(self.chain_id, to, calldata(fn, *args), description, sender, fn, args)

    # -------------------------------------------------- settler

    def set_signer(self, signer: str, expires_at: int, maker: str | None = None) -> UnsignedTx:
        """From the maker: `signer` may sign quotes (and log in) for it until `expires_at` (unix s); 0 revokes.
        A quote is accepted only while the signer outlives its validUntil: rotate before expiry."""
        s = to_checksum_address(signer)
        return self._tx(self.settler, SET_SIGNER, (s, uint(expires_at, "expiresAt", 64)),
                        f"setSigner({s}, {expires_at})" + (" -- revoke" if not expires_at else ""), maker)

    def deposit(self, token: str, account: str, amount: int, sender: str | None = None) -> UnsignedTx:
        """Credits `account`'s settler balance (anyone may deposit for anyone; approve the settler first)."""
        t, a = to_checksum_address(token), to_checksum_address(account)
        return self._tx(self.settler, DEPOSIT, (t, a, uint(amount)), f"deposit {amount} raw of {t} for {a}", sender)

    def request_withdraw(self, token: str, amount: int, maker: str | None = None) -> UnsignedTx:
        """Starts the 5 min delay; the requested amount keeps backing fills until `withdraw`."""
        t = to_checksum_address(token)
        return self._tx(self.settler, REQUEST_WITHDRAW, (t, uint(amount)),
                        f"requestWithdraw {amount} raw of {t} (withdraw between 5 min and 1 day + 5 min later)", maker)

    def withdraw(self, token: str, to: str, maker: str | None = None) -> UnsignedTx:
        t, r = to_checksum_address(token), to_checksum_address(to)
        return self._tx(self.settler, WITHDRAW, (t, r), f"withdraw the requested {t} to {r}", maker)

    def increase_epoch(self, maker: str, sender: str | None = None) -> UnsignedTx:
        """Voids every outstanding quote of `maker` (the maker or a live signer sends it). Doing this to back out of a
        quote that already WON counts as a fade on the gateway scorecard."""
        m = to_checksum_address(maker)
        return self._tx(self.settler, INCREASE_EPOCH, (m,), f"increaseEpoch({m}): cancel all quotes", sender or m)

    def invalidate_nonces(self, maker: str, slot: int, mask: int, sender: str | None = None) -> UnsignedTx:
        m = to_checksum_address(maker)
        return self._tx(self.settler, INVALIDATE_NONCES, (m, uint(slot), uint(mask)),
                        f"invalidateNonces({m}, slot {slot}, mask {hex(mask)})", sender or m)

    def link_maker(self, maker: str, linked: bool = True, account: str | None = None) -> UnsignedTx:
        """From a paying wallet that is not the maker: lets the maker's quotes name it as externalAccount."""
        m = to_checksum_address(maker)
        return self._tx(self.settler, LINK_MAKER, (m, bool(linked)), f"linkMaker({m}, {linked})", account)

    # -------------------------------------------------- core

    def set_operator(self, operator: str | None = None, approved: bool = True, maker: str | None = None) -> UnsignedTx:
        """OptionsCore.setOperator(settler, true): lets the settler deliver your longs on MakerSells fills."""
        if not self.core:
            raise ValueError("core address not set")
        op = to_checksum_address(operator or self.settler)
        return self._tx(self.core, SET_OPERATOR, (op, bool(approved)), f"core.setOperator({op}, {approved})", maker)

    def redeem_long(self, series: Series, amount: int, on_behalf: str, receiver: str) -> UnsignedTx:
        """After settlement: burn `amount` long of `on_behalf` and pay `receiver`."""
        if not self.core:
            raise ValueError("core address not set")
        ob, rc = to_checksum_address(on_behalf), to_checksum_address(receiver)
        return self._tx(self.core, REDEEM_LONG, (series_values(series), uint(amount), ob, rc),
                        f"core.redeemLong(series {series.id}, {amount})", ob)

    # -------------------------------------------------- ERC-20

    def approve(self, token: str, amount: int, spender: str | None = None, owner: str | None = None) -> UnsignedTx:
        """ERC-20 approve of the settler (default spender) for `amount` raw."""
        t, sp = to_checksum_address(token), to_checksum_address(spender or self.settler)
        return self._tx(t, APPROVE, (sp, uint(amount)), f"{t}.approve({sp}, {amount})", owner)
