"""Read-only JSON-RPC (eth_call) over the standard library: the settler and core state a maker checks before and
while quoting. No method here signs or sends anything. Synchronous: from async code use asyncio.to_thread.
"""

from __future__ import annotations

import itertools
import json
import urllib.request
from typing import Any

from eth_abi import decode
from eth_utils import to_checksum_address

from . import settler as S
from .eip712 import quote_values, series_id, series_values
from .models import Quote, Series
from .networks import Network


class RPCError(RuntimeError):
    pass


class Reader:
    """eth_call reads of one deployment. `transport(payload) -> response` can be swapped (tests)."""

    def __init__(self, rpc_url: str, settler: str, core: str | None = None, timeout: float = 10.0, transport=None):
        self.url = rpc_url
        self.settler = to_checksum_address(settler)
        self.core = to_checksum_address(core) if core else None
        self.timeout = timeout
        self._ids = itertools.count(1)
        self._transport = transport or self._http

    @classmethod
    def for_network(cls, n: Network, rpc_url: str | None = None, **kw: Any) -> Reader:
        return cls(rpc_url or n.rpc, n.settler, n.core, **kw)

    def _http(self, payload: dict[str, Any]) -> dict[str, Any]:
        req = urllib.request.Request(self.url, data=json.dumps(payload).encode(),
                                     headers={"content-type": "application/json",
                                              "user-agent": "hedgehood-maker/0.1"})
        with urllib.request.urlopen(req, timeout=self.timeout) as r:  # noqa: S310 (caller-supplied RPC URL)
            return json.loads(r.read())

    def request(self, method: str, params: list[Any]) -> Any:
        out = self._transport({"jsonrpc": "2.0", "id": next(self._ids), "method": method, "params": params})
        if out.get("error"):
            raise RPCError(f"{method}: {out['error']}")
        return out.get("result")

    def call(self, to: str, signature: str, args: tuple, returns: list[str], block: str = "latest") -> tuple:
        data = S.calldata(signature, *args)
        raw = self.request("eth_call", [{"to": to, "data": data}, block])
        return decode(returns, bytes.fromhex(raw[2:]))

    def chain_id(self) -> int:
        return int(self.request("eth_chainId", []), 16)

    # -------------------------------------------------- settler

    def epoch(self, maker: str) -> int:
        return self.call(self.settler, S.EPOCH, (to_checksum_address(maker),), ["uint256"])[0]

    def signer_expiry(self, maker: str, signer: str) -> int:
        return self.call(self.settler, S.SIGNER_EXPIRY, (to_checksum_address(maker), to_checksum_address(signer)),
                         ["uint64"])[0]

    def nonce_used(self, maker: str, nonce: int) -> bool:
        return self.call(self.settler, S.NONCE_USED, (to_checksum_address(maker), int(nonce)), ["bool"])[0]

    def settler_balance(self, token: str, account: str) -> int:
        return self.call(self.settler, S.SETTLER_BALANCE, (to_checksum_address(token), to_checksum_address(account)),
                         ["uint256"])[0]

    def pending_withdraw(self, token: str, account: str) -> tuple[int, int]:
        return self.call(self.settler, S.PENDING_WITHDRAW, (to_checksum_address(token), to_checksum_address(account)),
                         ["uint256", "uint256"])

    def linked(self, account: str, maker: str) -> bool:
        return self.call(self.settler, S.LINKED, (to_checksum_address(account), to_checksum_address(maker)),
                         ["bool"])[0]

    def fees(self) -> tuple[int, int]:
        """(feeBps, notionalFeeBps)."""
        return (self.call(self.settler, S.FEE_BPS, (), ["uint256"])[0],
                self.call(self.settler, S.NOTIONAL_FEE_BPS, (), ["uint256"])[0])

    def quote_digest(self, q: Quote) -> str:
        """The settler's own digest: compare with eip712.quote_digest to catch domain or schema drift."""
        return "0x" + self.call(self.settler, S.QUOTE_DIGEST, (quote_values(q),), ["bytes32"])[0].hex()

    def preview_fill(self, q: Quote, amount: int) -> tuple[int, int]:
        return self.call(self.settler, S.PREVIEW_FILL, (quote_values(q), int(amount)), ["uint256", "uint256"])

    def is_listed(self, series: Series) -> bool:
        return self.call(self.settler, S.IS_LISTED, (series_values(series),), ["bool"])[0]

    # -------------------------------------------------- core and tokens

    def is_operator(self, owner: str, spender: str | None = None) -> bool:
        if not self.core:
            raise ValueError("core address not set")
        return self.call(self.core, S.IS_OPERATOR, (to_checksum_address(owner),
                                                    to_checksum_address(spender or self.settler)), ["bool"])[0]

    def long_balance(self, owner: str, series_id: int) -> int:
        if not self.core:
            raise ValueError("core address not set")
        return self.call(self.core, S.LONG_BALANCE, (to_checksum_address(owner), int(series_id)), ["uint256"])[0]

    def can_sell(self, maker: str, series: Series, amount: int) -> str | None:
        """Why the settler could not deliver `amount` of `series` from `maker` (None: it can). The gateway rejects a
        close quote it cannot fill (`inventory`): check before quoting one. The settler moves the long as the maker's
        OptionsCore operator (`setOperator`, the SDK's setup path)."""
        held = self.long_balance(maker, series_id(series))
        if held < int(amount):
            return f"holds {held} of {int(amount)}"
        if not self.is_operator(maker):
            return "the settler is not the maker's operator on OptionsCore (setOperator)"
        return None

    def token_balance(self, token: str, account: str) -> int:
        return self.call(to_checksum_address(token), S.ERC20_BALANCE, (to_checksum_address(account),), ["uint256"])[0]

    def allowance(self, token: str, owner: str, spender: str | None = None) -> int:
        return self.call(to_checksum_address(token), S.ALLOWANCE,
                         (to_checksum_address(owner), to_checksum_address(spender or self.settler)), ["uint256"])[0]
