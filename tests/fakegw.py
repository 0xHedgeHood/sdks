"""An in-process stand-in for the gateway's maker socket (hub.go): challenge login with the allowlist and delegated
signers, then a per-test script. It checks quotes the way the gateway does, with eth_account's own EIP-712 encoder
(independent of the SDK's encoding)."""

from __future__ import annotations

import asyncio
import json
import secrets
import time

from eth_account import Account
from eth_account.messages import encode_defunct, encode_typed_data
from eth_utils import to_checksum_address
from websockets.asyncio.server import serve

CHAIN = 46630
SETTLER = "0xda43b0933031e41D5B367E1807536FcF4ebdEE7e"
TAKER = "0xa6E328D981E199bD7A9C466AC3684679CF3a6D97"
SERIES = {
    "underlying": "0x6661d20c2e343bb78b42535eb060c3058c744f3f",
    "quote": "0x850de815d41f6983aaf09247f9a465c0aefff04c",
    "oracle": "0x9e6c529caa69d44b63df27483c57f97a42d6e898",
    "expiry": 1794603600,
    "isPut": True,
    "strike": "330000000",
    "physical": False,
}


def rfq_frame(rfq_id="r1", side=0, amount=10**18, deadline_ms=2000, settler=SETTLER, **over):
    now_ms = int(time.time() * 1000)
    f = {"type": "rfq", "rfqId": rfq_id, "chainId": CHAIN, "settler": settler.lower(), "taker": TAKER.lower(),
         "side": side, "series": dict(SERIES), "amount": str(amount), "respondBy": now_ms + deadline_ms,
         "minValidUntil": (now_ms + deadline_ms) // 1000 + 15}
    f.update(over)
    return f


def typed(q: dict, chain=CHAIN, settler=SETTLER) -> dict:
    s = q["series"]
    return {
        "types": {
            "EIP712Domain": [{"name": "name", "type": "string"}, {"name": "version", "type": "string"},
                             {"name": "chainId", "type": "uint256"}, {"name": "verifyingContract", "type": "address"}],
            "SeriesParams": [{"name": "underlying", "type": "address"}, {"name": "quote", "type": "address"},
                             {"name": "oracle", "type": "address"}, {"name": "expiry", "type": "uint64"},
                             {"name": "isPut", "type": "bool"}, {"name": "strike", "type": "uint256"},
                             {"name": "physical", "type": "bool"}],
            "Quote": [{"name": "maker", "type": "address"}, {"name": "signer", "type": "address"},
                      {"name": "taker", "type": "address"}, {"name": "externalAccount", "type": "address"},
                      {"name": "series", "type": "SeriesParams"}, {"name": "side", "type": "uint8"},
                      {"name": "amount", "type": "uint256"}, {"name": "minFill", "type": "uint256"},
                      {"name": "premium", "type": "uint256"}, {"name": "validUntil", "type": "uint64"},
                      {"name": "nonce", "type": "uint256"}, {"name": "epoch", "type": "uint256"}],
        },
        "primaryType": "Quote",
        "domain": {"name": "Hedgehood RFQ Settler", "version": "2", "chainId": chain,
                   "verifyingContract": to_checksum_address(settler)},
        "message": {
            "maker": q["maker"], "signer": q["signer"], "taker": q["taker"], "externalAccount": q["externalAccount"],
            "series": {"underlying": s["underlying"], "quote": s["quote"], "oracle": s["oracle"],
                       "expiry": s["expiry"], "isPut": s["isPut"], "strike": int(s["strike"]),
                       "physical": s["physical"]},
            "side": q["side"], "amount": int(q["amount"]), "minFill": int(q["minFill"]), "premium": int(q["premium"]),
            "validUntil": q["validUntil"], "nonce": int(q["nonce"]), "epoch": int(q["epoch"]),
        },
    }


def recover_quote(frame: dict) -> str:
    return Account.recover_message(encode_typed_data(full_message=typed(frame["quote"])), signature=frame["signature"])


def digest_of(frame: dict) -> str:
    from eth_utils import keccak

    m = encode_typed_data(full_message=typed(frame["quote"]))
    return "0x" + keccak(b"\x19" + m.version + m.header + m.body).hex()


class FakeGateway:
    def __init__(self, makers, *, delegates=None, epoch=0, fee_bps=1250, notional_fee_bps=100, settler=SETTLER,
                 script=None):
        self.makers = {to_checksum_address(m) for m in makers}
        self.delegates = {(to_checksum_address(a), to_checksum_address(b)) for a, b in (delegates or [])}
        self.epoch, self.fee_bps, self.notional_fee_bps, self.settler = epoch, fee_bps, notional_fee_bps, settler
        self.script = script  # async (gateway, ws, maker, connection_number) -> None
        self.connections = 0
        self.logins: list[dict] = []
        self.inbox: asyncio.Queue = asyncio.Queue()

    async def __aenter__(self):
        self._server = await serve(self._handler, "127.0.0.1", 0)
        port = self._server.sockets[0].getsockname()[1]
        self.url = f"ws://127.0.0.1:{port}/v1/maker/ws"
        return self

    async def __aexit__(self, *exc):
        self._server.close()
        await self._server.wait_closed()

    async def recv(self, ws, timeout=5.0) -> dict:
        return json.loads(await asyncio.wait_for(ws.recv(), timeout))

    async def _handler(self, ws):
        self.connections += 1
        n = self.connections
        nonce = secrets.token_hex(16)
        await ws.send(json.dumps({"type": "challenge", "nonce": nonce, "chainId": CHAIN}))
        login = await self.recv(ws, 10)
        self.logins.append(login)
        err = self._check_login(login, nonce)
        if err:
            await ws.send(json.dumps({"type": "error", "code": "login", "reason": err}))
            await ws.close(1008, "login")
            return
        maker = to_checksum_address(login["maker"])
        await ws.send(json.dumps({"type": "ready", "maker": maker, "settler": self.settler, "feeBps": self.fee_bps,
                                  "notionalFeeBps": self.notional_fee_bps, "epoch": str(self.epoch)}))
        if self.script:
            await self.script(self, ws, maker, n)
        else:
            await ws.wait_closed()

    def _check_login(self, login, nonce):
        if login.get("type") != "login":
            return "expected a login frame"
        maker, signer = to_checksum_address(login["maker"]), to_checksum_address(login["signer"])
        if maker not in self.makers:
            return f"maker {maker} is not on the allowlist"
        msg = f"hedgehood-rfq-login:{CHAIN}:{maker.lower()}:{nonce}"
        if Account.recover_message(encode_defunct(text=msg), signature=login["signature"]) != signer:
            return "login signature does not recover to signer"
        if signer != maker and (maker, signer) not in self.delegates:
            return "signer is not a live delegate of the maker on the settler"
        return None
