"""MakerClient: the maker's side of the gateway WebSocket (wss://<gateway>/v1/maker/ws).

    async def price(rfq: RFQ) -> int | Quote | Decline | str | None: ...
    client = MakerClient(TESTNET.gateway_ws, signer_key, price, maker=MAKER)
    await client.run()

The client logs in (EIP-191 challenge signed by the signer key), calls the pricer for every request under the
request's deadline, builds and signs the firm EIP-712 quote (fresh nonce, current epoch, minFill = amount), checks it
the way the gateway will, and reconnects with backoff when the socket drops. WebSocket pings from the gateway are
answered by the websockets library; a sync pricer runs in a thread so it never blocks them.

Pricer results: an int is the premium (quote raw for the whole amount, fee excluded); a Quote is sent as is (built
with `client.build_quote`); a Decline, a str (the reason) or None declines. Declining is cheap and keeps you off the
silence breaker (20 unanswered requests in a row pause a maker for 15 minutes).
"""

from __future__ import annotations

import asyncio
import inspect
import json
import logging
import random
import re
import time
from collections import OrderedDict
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any

from eth_account import Account
from eth_account.signers.local import LocalAccount
from eth_utils import to_checksum_address
from websockets.asyncio.client import connect
from websockets.exceptions import ConnectionClosed, WebSocketException

from . import eip712
from .constants import LOGIN_TIMEOUT, MAX_QUOTE_LIFE, ZERO_ADDRESS
from .models import (
    RFQ,
    Challenge,
    Decline,
    ErrorFrame,
    Frame,
    Quote,
    Ready,
    Rejected,
    ValidationError,
    Won,
    decline_frame,
    login_frame,
    parse_frame,
    quote_frame,
)
from .nonces import NonceManager

PricerResult = int | Quote | Decline | str | None
Pricer = Callable[[RFQ], PricerResult | Awaitable[PricerResult]]

LIFE_CAP = MAX_QUOTE_LIFE - 5  # leave a few seconds for clock skew between you, the gateway and the chain
_EPOCH_IN_REASON = re.compile(r"settler epoch (\d+)")

log = logging.getLogger("hedgehood_maker")


class LoginRefused(RuntimeError):
    """The gateway refused the login (not allowlisted, signer not delegated, bad signature). Not retried."""


class ProtocolError(RuntimeError):
    """The gateway sent something the client cannot follow (or a chain/settler other than the one pinned)."""


@dataclass
class Session:
    """What the gateway told us at login."""

    maker: str
    signer: str
    chain_id: int
    settler: str
    epoch: int
    fee_bps: int
    notional_fee_bps: int


class MakerClient:
    def __init__(
        self,
        url: str,
        signer_key: Any,
        pricer: Pricer,
        *,
        maker: str | None = None,
        external_account: str | None = None,
        quote_life: int = 60,
        chain_id: int | None = None,
        settler: str | None = None,
        on_event: Callable[[Frame], Any] | None = None,
        nonces: NonceManager | None = None,
        epoch_reader: Callable[[str], int | Awaitable[int]] | None = None,
        answer_margin_ms: int = 150,
        backoff_initial: float = 1.0,
        backoff_max: float = 30.0,
        clock: Callable[[], float] = time.time,
        open_timeout: float = 10.0,
    ):
        """
        signer_key: the maker's key, or a hot key the maker delegated with setSigner (hex str, bytes or LocalAccount).
        maker: the maker account (default: the signer's address).
        external_account: externalAccount on every built quote (None = the maker's settler balance).
        quote_life: seconds a quote stays valid (capped at 115; the gateway needs >= 15 s left after the auction).
        chain_id / settler: pin the deployment; the client refuses a gateway that announces another one.
        on_event: called with every frame after login (Ready, Won, Lost, Fill, Rejected, ErrorFrame, Paused, ...).
        epoch_reader: maker -> settler.epoch(maker) (e.g. rpc.Reader.epoch), used after a stale_epoch rejection.
        """
        self.url = url
        self._account: LocalAccount = (
            signer_key if isinstance(signer_key, LocalAccount) else Account.from_key(signer_key))
        self.signer = self._account.address
        self.maker = to_checksum_address(maker) if maker else self.signer
        self.external_account = to_checksum_address(external_account) if external_account else ZERO_ADDRESS
        if quote_life <= 0:
            raise ValueError("quote_life must be positive")
        self.quote_life = min(int(quote_life), LIFE_CAP)
        self.expected_chain_id = chain_id
        self.expected_settler = to_checksum_address(settler) if settler else None
        self.pricer = pricer
        self.on_event = on_event
        self.nonces = nonces or NonceManager()
        self.epoch_reader = epoch_reader
        self.answer_margin_ms = answer_margin_ms
        self.backoff_initial, self.backoff_max = backoff_initial, backoff_max
        self.clock = clock
        self.open_timeout = open_timeout

        self.session: Session | None = None
        self._ws = None
        self._stopping = asyncio.Event()
        self._want_paused = False
        self._tasks: set[asyncio.Task] = set()
        self._sent: OrderedDict[str, tuple[str, Quote]] = OrderedDict()  # rfqId -> (digest, quote)

    # ------------------------------------------------------------------ lifecycle

    async def run(self) -> None:
        """Connect, log in and answer until `stop()`; reconnect with exponential backoff and jitter. A refused login
        raises LoginRefused; a gateway on another chain or settler than pinned raises ProtocolError."""
        delay = self.backoff_initial
        while not self._stopping.is_set():
            logged_in = False
            try:
                logged_in = await self.run_once()
            except (LoginRefused, ProtocolError):
                raise
            except (OSError, WebSocketException, asyncio.TimeoutError, ValidationError, ValueError) as e:
                log.warning("gateway connection ended: %s: %s", type(e).__name__, e)
            if self._stopping.is_set():
                break
            if logged_in:
                delay = self.backoff_initial
            wait = delay * random.uniform(0.8, 1.2)
            log.info("reconnecting in %.1f s", wait)
            try:
                await asyncio.wait_for(self._stopping.wait(), timeout=wait)
            except asyncio.TimeoutError:
                pass
            delay = min(delay * 2, self.backoff_max)

    async def run_once(self) -> bool:
        """One connection: login, then answer until the socket closes. Returns whether the login succeeded."""
        logged_in = False
        async with connect(self.url, open_timeout=self.open_timeout, max_size=1 << 20, ping_interval=20,
                           ping_timeout=20) as ws:
            self._ws = ws
            try:
                await self._login(ws)
                logged_in = True
                if self._want_paused:
                    await ws.send(json.dumps({"type": "pause"}))
                async for raw in ws:
                    await self._dispatch(ws, raw)
            except ConnectionClosed:
                pass
            finally:
                self._ws = None
                for t in list(self._tasks):
                    t.cancel()
                self._tasks.clear()
        return logged_in

    def stop(self) -> None:
        """Ends `run()` after closing the socket."""
        self._stopping.set()
        if self._ws is not None:
            asyncio.ensure_future(self._ws.close())

    async def pause(self) -> None:
        """Stop receiving requests (kept across reconnects until `resume`)."""
        self._want_paused = True
        if self._ws is not None:
            await self._ws.send(json.dumps({"type": "pause"}))

    async def resume(self) -> None:
        self._want_paused = False
        if self._ws is not None:
            await self._ws.send(json.dumps({"type": "resume"}))

    def set_epoch(self, epoch: int) -> None:
        """After your own increaseEpoch: sign future quotes under the new epoch."""
        if self.session is None:
            raise RuntimeError("not logged in")
        self.session.epoch = int(epoch)

    def quote_for(self, rfq_id: str) -> tuple[str, Quote] | None:
        """(digest, quote) this client sent for a request (the last 1024 kept)."""
        return self._sent.get(rfq_id)

    # ------------------------------------------------------------------ login

    async def _login(self, ws) -> None:
        ch = parse_frame(json.loads(await asyncio.wait_for(ws.recv(), timeout=LOGIN_TIMEOUT + 5)))
        if not isinstance(ch, Challenge):
            raise ProtocolError(f"expected a challenge, got {ch.raw}")
        if self.expected_chain_id is not None and ch.chain_id != self.expected_chain_id:
            raise ProtocolError(f"gateway is on chain {ch.chain_id}, expected {self.expected_chain_id}")
        sig = eip712.sign_login(ch.chain_id, self.maker, ch.nonce, self._account.key)
        await ws.send(json.dumps(login_frame(self.maker, self.signer, sig)))
        first = parse_frame(json.loads(await asyncio.wait_for(ws.recv(), timeout=LOGIN_TIMEOUT + 5)))
        if isinstance(first, ErrorFrame):
            raise LoginRefused(f"{first.code}: {first.reason}")
        if not isinstance(first, Ready):
            raise ProtocolError(f"expected ready, got {first.raw}")
        if first.maker != self.maker:
            raise ProtocolError(f"gateway logged us in as {first.maker}, expected {self.maker}")
        if self.expected_settler is not None and first.settler != self.expected_settler:
            raise ProtocolError(f"gateway settler {first.settler}, expected {self.expected_settler}")
        self.session = Session(self.maker, self.signer, ch.chain_id, first.settler, first.epoch, first.fee_bps,
                               first.notional_fee_bps)
        log.info("logged in: maker %s signer %s settler %s epoch %d fee %d bps (cap %d bps of notional)",
                 self.maker, self.signer, first.settler, first.epoch, first.fee_bps, first.notional_fee_bps)
        await self._emit(first)

    # ------------------------------------------------------------------ frames

    async def _dispatch(self, ws, raw: str | bytes) -> None:
        try:
            d = json.loads(raw)
        except ValueError:
            log.warning("gateway sent a frame that is not JSON")
            return
        try:
            frame = parse_frame(d)
        except ValidationError as e:
            log.warning("bad frame from gateway: %s", e)
            if isinstance(d, dict) and d.get("type") == "rfq" and d.get("rfqId"):
                await self._send(ws, decline_frame(str(d["rfqId"]), "invalid_rfq"))
            return
        if isinstance(frame, RFQ):
            task = asyncio.create_task(self._answer(ws, frame))
            self._tasks.add(task)
            task.add_done_callback(self._tasks.discard)
            return
        if isinstance(frame, Rejected):
            log.info("rejected %s: %s %s", frame.rfq_id, frame.code, frame.reason)
            if frame.code == "stale_epoch":
                await self._refresh_epoch(frame)
        elif isinstance(frame, Won):
            log.info("won %s digest %s", frame.rfq_id, frame.digest)
        await self._emit(frame)

    async def _answer(self, ws, rfq: RFQ) -> None:
        reply = await self.respond(rfq)
        await self._send(ws, reply)

    async def _send(self, ws, frame: dict[str, Any]) -> None:
        try:
            await ws.send(json.dumps(frame))
        except ConnectionClosed:
            pass

    async def _emit(self, frame: Frame) -> None:
        if self.on_event is None:
            return
        try:
            out = self.on_event(frame)
            if inspect.isawaitable(out):
                await out
        except Exception:  # noqa: BLE001 - a handler bug must not drop the connection
            log.exception("on_event handler failed")

    async def _refresh_epoch(self, rej: Rejected) -> None:
        if self.session is None:
            return
        epoch = None
        if self.epoch_reader is not None:
            try:
                out = self.epoch_reader(self.maker)
                epoch = int(await out if inspect.isawaitable(out) else out)
            except Exception as e:  # noqa: BLE001
                log.warning("epoch_reader failed: %s", e)
        if epoch is None:
            m = _EPOCH_IN_REASON.search(rej.reason)
            epoch = int(m.group(1)) if m else self.session.epoch + 1
        log.info("epoch %d -> %d after stale_epoch", self.session.epoch, epoch)
        self.session.epoch = epoch

    # ------------------------------------------------------------------ quoting

    def _now_ms(self) -> int:
        return int(self.clock() * 1000)

    async def respond(self, rfq: RFQ) -> dict[str, Any]:
        """The frame answering `rfq`: a signed quote or a decline (never raises)."""
        s = self.session
        if s is None:
            return decline_frame(rfq.rfq_id, "not_ready")
        if rfq.chain_id != s.chain_id or rfq.settler != s.settler:
            return decline_frame(rfq.rfq_id, "wrong_settler")
        budget = (rfq.respond_by_ms - self._now_ms() - self.answer_margin_ms) / 1000
        if budget <= 0:
            return decline_frame(rfq.rfq_id, "late")
        try:
            result = await asyncio.wait_for(self._price(rfq), timeout=budget)
        except asyncio.TimeoutError:
            log.warning("pricer timed out on %s", rfq.rfq_id)
            return decline_frame(rfq.rfq_id, "timeout")
        except Exception:  # noqa: BLE001
            log.exception("pricer failed on %s", rfq.rfq_id)
            return decline_frame(rfq.rfq_id, "maker_error")

        if result is None:
            return decline_frame(rfq.rfq_id, "no_price")
        if isinstance(result, Decline):
            return decline_frame(rfq.rfq_id, result.reason)
        if isinstance(result, str):
            return decline_frame(rfq.rfq_id, result)
        try:
            if isinstance(result, Quote):
                q = result
            elif isinstance(result, int) and not isinstance(result, bool):
                if result <= 0:
                    return decline_frame(rfq.rfq_id, "no_price")
                q = self.build_quote(rfq, result)
            else:
                raise ValidationError(f"pricer returned {type(result).__name__}")
            self.check_quote(rfq, q)
        except ValidationError as e:
            log.error("not sending an invalid quote for %s: %s", rfq.rfq_id, e)
            return decline_frame(rfq.rfq_id, "maker_error")
        if self._now_ms() > rfq.respond_by_ms:
            return decline_frame(rfq.rfq_id, "late")  # never hand out a firm quote the auction will not read
        sig = eip712.sign_quote(q, s.chain_id, s.settler, self._account.key)
        self._sent[rfq.rfq_id] = (eip712.quote_digest(q, s.chain_id, s.settler), q)
        while len(self._sent) > 1024:
            self._sent.popitem(last=False)
        return quote_frame(rfq.rfq_id, q, sig)

    async def _price(self, rfq: RFQ) -> PricerResult:
        p = self.pricer
        if inspect.iscoroutinefunction(p) or inspect.iscoroutinefunction(type(p).__call__):
            return await p(rfq)
        out = await asyncio.to_thread(p, rfq)
        if inspect.isawaitable(out):
            return await out
        return out

    def build_quote(self, rfq: RFQ, premium: int, *, valid_for: int | None = None,
                    external_account: str | None = None, nonce: int | None = None) -> Quote:
        """The firm quote for `rfq` at `premium` (quote raw for the whole amount, fee excluded), unsigned."""
        if self.session is None:
            raise RuntimeError("not logged in")
        now = int(self.clock())
        life = min(int(valid_for or self.quote_life), LIFE_CAP)
        return Quote(
            maker=self.maker,
            signer=self.signer,
            taker=rfq.taker,
            external_account=to_checksum_address(external_account) if external_account else self.external_account,
            series=rfq.series,
            side=rfq.side,
            amount=rfq.amount,
            min_fill=rfq.amount,
            premium=premium,
            valid_until=max(now + life, rfq.min_valid_until),
            nonce=self.nonces.next() if nonce is None else nonce,
            epoch=self.session.epoch,
        )

    def check_quote(self, rfq: RFQ, q: Quote) -> None:
        """The gateway's static checks (docs/RFQ_GATEWAY.md section 7), so a bad quote never leaves the process."""
        if q.maker != self.maker:
            raise ValidationError(f"quote maker {q.maker} is not the logged-in maker {self.maker}")
        if q.signer != self.signer:
            raise ValidationError(f"quote signer {q.signer} is not this client's key {self.signer}")
        if q.taker != rfq.taker or q.side != rfq.side or q.amount != rfq.amount or q.series != rfq.series:
            raise ValidationError("taker, side, amount and series must equal the request")
        if q.min_fill != rfq.amount:
            raise ValidationError("minFill must equal the amount")
        if q.premium <= 0:
            raise ValidationError("premium must be positive")
        now = int(self.clock())
        if q.valid_until < rfq.min_valid_until or q.valid_until > now + MAX_QUOTE_LIFE:
            raise ValidationError(f"validUntil {q.valid_until} outside [{rfq.min_valid_until}, {now + MAX_QUOTE_LIFE}]")
