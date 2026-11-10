"""MakerClient against an in-process fake gateway (tests/fakegw.py). Offline; keys are generated per test."""

import asyncio
import json
import time

import pytest
from eth_account import Account
from fakegw import SETTLER, TAKER, FakeGateway, digest_of, recover_quote, rfq_frame

from hedgehood_maker import Decline, Fill, LoginRefused, Lost, MakerClient, ProtocolError, Ready, Won


def run(coro, timeout=20):
    return asyncio.run(asyncio.wait_for(coro, timeout))


async def until(cond, timeout=10):
    end = time.monotonic() + timeout
    while not cond():
        if time.monotonic() > end:
            raise AssertionError("condition not met in time")
        await asyncio.sleep(0.01)


async def drive(gw, client, done):
    task = asyncio.create_task(client.run())
    try:
        await until(lambda: done.get("done") or task.done())
    finally:
        client.stop()
    await asyncio.wait_for(task, 5)  # re-raises what run() raised
    return client


def client_for(gw, key, pricer, **kw):
    kw.setdefault("chain_id", 46630)
    kw.setdefault("settler", SETTLER)
    kw.setdefault("backoff_initial", 0.05)
    return MakerClient(gw.url, key, pricer, **kw)


def test_quote_is_signed_valid_and_followed_to_the_fill():
    acct = Account.create()
    events, out = [], {}

    async def script(gw, ws, maker, n):
        await ws.send(json.dumps(rfq_frame("r1")))
        out["q1"] = await gw.recv(ws)
        await ws.send(json.dumps(rfq_frame("r2", side=1)))
        out["q2"] = await gw.recv(ws)
        d = digest_of(out["q1"])
        out["digest"] = d
        await ws.send(json.dumps({"type": "won", "rfqId": "r1", "digest": d}))
        await ws.send(json.dumps({"type": "lost", "rfqId": "r2"}))
        await ws.send(json.dumps({"type": "fill", "digest": d, "txHash": "0x" + "ab" * 32,
                                  "amount": out["q1"]["quote"]["amount"], "rfqId": "r1"}))
        await until(lambda: len(events) >= 4)
        out["done"] = True
        await ws.wait_closed()

    async def main():
        async with FakeGateway([acct.address], script=script) as gw:
            return await drive(gw, client_for(gw, acct.key, lambda rfq: 2_500_000, on_event=events.append), out)

    c = run(main())
    now = int(time.time())
    for f in (out["q1"], out["q2"]):
        q = f["quote"]
        assert f["type"] == "quote" and recover_quote(f) == acct.address
        assert q["maker"] == q["signer"] == acct.address and q["taker"] == TAKER
        assert q["amount"] == q["minFill"] == "1000000000000000000" and q["premium"] == "2500000"
        assert isinstance(q["validUntil"], int) and now + 50 <= q["validUntil"] <= now + 120
        assert q["epoch"] == "0" and q["externalAccount"] == "0x" + "00" * 20 and len(f["signature"]) == 132
    assert out["q2"]["quote"]["side"] == 1
    assert out["q1"]["quote"]["nonce"] != out["q2"]["quote"]["nonce"]
    assert [type(e) for e in events] == [Ready, Won, Lost, Fill]
    assert events[0].fee_bps == 1250 and events[0].notional_fee_bps == 100
    assert c.quote_for("r1")[0] == out["digest"] == events[1].digest == events[3].digest


def test_delegated_hot_signer_logs_in_and_signs_for_the_maker():
    maker, hot = Account.create().address, Account.create()
    out = {}

    async def script(gw, ws, m, n):
        await ws.send(json.dumps(rfq_frame("r1")))
        out["q"] = await gw.recv(ws)
        out["done"] = True
        await ws.wait_closed()

    async def main():
        async with FakeGateway([maker], delegates=[(maker, hot.address)], script=script) as gw:
            await drive(gw, client_for(gw, hot.key, lambda r: 1_000_000, maker=maker), out)
            return gw

    gw = run(main())
    assert gw.logins[0]["maker"] == maker and gw.logins[0]["signer"] == hot.address
    q = out["q"]["quote"]
    assert q["maker"] == maker and q["signer"] == hot.address and recover_quote(out["q"]) == hot.address


def test_refused_login_raises_and_is_not_retried():
    maker, stranger = Account.create().address, Account.create()

    async def main():
        async with FakeGateway([maker]) as gw:
            with pytest.raises(LoginRefused, match="not a live delegate"):
                await drive(gw, client_for(gw, stranger.key, lambda r: 1, maker=maker), {})
            with pytest.raises(LoginRefused, match="allowlist"):
                await drive(gw, client_for(gw, stranger.key, lambda r: 1), {})
            return gw

    assert run(main()).connections == 2


def test_pinned_deployment_is_enforced():
    acct = Account.create()

    async def main():
        async with FakeGateway([acct.address]) as gw:
            with pytest.raises(ProtocolError, match="settler"):
                await drive(gw, client_for(gw, acct.key, lambda r: 1, settler="0x" + "11" * 20), {})
            with pytest.raises(ProtocolError, match="chain"):
                await drive(gw, client_for(gw, acct.key, lambda r: 1, chain_id=1), {})

    run(main())


def test_declines():
    acct = Account.create()
    out, box = {}, {}

    def pricer(rfq):
        if rfq.rfq_id == "boom":
            raise RuntimeError("model blew up")
        if rfq.rfq_id == "bad":  # a quote that does not match the request must never be sent
            return box["c"].build_quote(rfq, 5).replace(taker="0x" + "11" * 20)
        if not rfq.maker_buys:
            return Decline("no_inventory")
        if rfq.amount > 10**18:
            return "too_big"
        return None

    async def script(gw, ws, m, n):
        cases = [rfq_frame("sell", side=1), rfq_frame("big", amount=10**19), rfq_frame("none"),
                 rfq_frame("other", settler="0x" + "22" * 20), rfq_frame("late", deadline_ms=-5),
                 rfq_frame("boom"), rfq_frame("bad"), {"type": "rfq", "rfqId": "broken", "side": 0}]
        for c in cases:
            await ws.send(json.dumps(c))
        got = [await gw.recv(ws) for _ in cases]
        out["reasons"] = {f["rfqId"]: (f["type"], f["reason"]) for f in got}
        out["done"] = True
        await ws.wait_closed()

    async def main():
        async with FakeGateway([acct.address], script=script) as gw:
            box["c"] = client_for(gw, acct.key, pricer)
            await drive(gw, box["c"], out)

    run(main())
    assert out["reasons"] == {
        "sell": ("decline", "no_inventory"), "big": ("decline", "too_big"), "none": ("decline", "no_price"),
        "other": ("decline", "wrong_settler"), "late": ("decline", "late"), "boom": ("decline", "maker_error"),
        "bad": ("decline", "maker_error"), "broken": ("decline", "invalid_rfq"),
    }


def test_slow_async_pricer_declines_before_the_deadline():
    acct = Account.create()
    out = {}

    async def pricer(rfq):
        if rfq.rfq_id == "slow":
            await asyncio.sleep(5)
        return 7_000_000

    async def script(gw, ws, m, n):
        f = rfq_frame("slow", deadline_ms=600)
        await ws.send(json.dumps(f))
        out["slow"] = await gw.recv(ws)
        out["slow_at"] = int(time.time() * 1000)
        out["respond_by"] = f["respondBy"]
        await ws.send(json.dumps(rfq_frame("fast")))
        out["fast"] = await gw.recv(ws)
        out["done"] = True
        await ws.wait_closed()

    async def main():
        async with FakeGateway([acct.address], script=script) as gw:
            await drive(gw, client_for(gw, acct.key, pricer), out)

    run(main())
    assert out["slow"] == {"type": "decline", "rfqId": "slow", "reason": "timeout"}
    assert out["slow_at"] <= out["respond_by"]
    assert out["fast"]["type"] == "quote" and out["fast"]["quote"]["premium"] == "7000000"


def test_custom_quote_from_build_quote():
    acct, treasury = Account.create(), Account.create().address
    out, box = {}, {}

    def pricer(rfq):
        return box["c"].build_quote(rfq, 3_000_000, valid_for=30, external_account=treasury)

    async def script(gw, ws, m, n):
        f = rfq_frame("r1")
        await ws.send(json.dumps(f))
        out["q"], out["min"] = await gw.recv(ws), f["minValidUntil"]
        out["done"] = True
        await ws.wait_closed()

    async def main():
        async with FakeGateway([acct.address], script=script) as gw:
            box["c"] = client_for(gw, acct.key, pricer)
            await drive(gw, box["c"], out)

    run(main())
    q = out["q"]["quote"]
    assert q["externalAccount"] == treasury and recover_quote(out["q"]) == acct.address
    assert out["min"] <= q["validUntil"] <= int(time.time()) + 31


@pytest.mark.parametrize("reader", [None, lambda maker: 9])
def test_stale_epoch_rejection_moves_the_epoch(reader):
    acct = Account.create()
    out = {}

    async def script(gw, ws, m, n):
        await ws.send(json.dumps(rfq_frame("r1")))
        out["q1"] = await gw.recv(ws)
        await ws.send(json.dumps({"type": "rejected", "rfqId": "r1", "code": "stale_epoch",
                                  "reason": "quote epoch 0, settler epoch 4"}))
        await ws.send(json.dumps(rfq_frame("r2")))
        out["q2"] = await gw.recv(ws)
        out["done"] = True
        await ws.wait_closed()

    async def main():
        async with FakeGateway([acct.address], script=script) as gw:
            await drive(gw, client_for(gw, acct.key, lambda r: 1_000_000, epoch_reader=reader), out)

    run(main())
    assert out["q1"]["quote"]["epoch"] == "0"
    assert out["q2"]["quote"]["epoch"] == ("4" if reader is None else "9")


def test_reconnects_and_keeps_pause_across_connections():
    acct = Account.create()
    out = {}

    async def script(gw, ws, m, n):
        if n == 1:
            out["first"] = await gw.recv(ws)  # the pause the test sends once logged in
            await ws.send(json.dumps({"type": "paused"}))
            await ws.close()  # the gateway restarts
            return
        out["second"] = await gw.recv(ws)  # re-sent on the new connection
        await ws.send(json.dumps({"type": "paused"}))
        await ws.send(json.dumps(rfq_frame("r1")))
        out["q"] = await gw.recv(ws)
        out["done"] = True
        await ws.wait_closed()

    async def main():
        async with FakeGateway([acct.address], script=script) as gw:
            c = client_for(gw, acct.key, lambda r: 1_000_000)
            task = asyncio.create_task(c.run())
            await until(lambda: c.session is not None)
            await c.pause()
            await until(lambda: out.get("done"))
            c.stop()
            await asyncio.wait_for(task, 5)
            return gw

    gw = run(main())
    assert out["first"] == out["second"] == {"type": "pause"}
    assert gw.connections == 2 and out["q"]["type"] == "quote"


def test_answers_the_gateways_ping():
    acct = Account.create()
    out = {}

    async def script(gw, ws, m, n):
        pong = await ws.ping()
        out["latency"] = await asyncio.wait_for(pong, 5)  # the hub drops a maker that does not pong within 5 s
        out["done"] = True
        await ws.wait_closed()

    async def main():
        async with FakeGateway([acct.address], script=script) as gw:
            await drive(gw, client_for(gw, acct.key, lambda r: 1), out)

    run(main())
    assert out["latency"] < 5
