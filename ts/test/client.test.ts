/** MakerClient against an in-process fake gateway (test/fakegw.ts). Offline; keys are generated per test. */

import { type Address, type Hex, zeroAddress } from "viem";
import { generatePrivateKey, privateKeyToAccount } from "viem/accounts";
import { afterEach, describe, expect, it } from "vitest";
import {
  Decline,
  LoginRefused,
  MakerClient,
  type MakerClientOptions,
  type PricerResult,
  ProtocolError,
  replaceQuote,
  type ServerFrame,
  silentLogger,
} from "../src/index.js";
import {
  digestOf,
  FakeGateway,
  type Json,
  type Peer,
  recoverQuote,
  rfqFrame,
  type Script,
  SETTLER,
  TAKER,
} from "./fakegw.js";

const gateways: FakeGateway[] = [];
afterEach(async () => {
  await Promise.all(gateways.splice(0).map((g) => g.close()));
});

async function gateway(makers: string[], opts: ConstructorParameters<typeof FakeGateway>[1] = {}) {
  const gw = await new FakeGateway(makers, opts).start();
  gateways.push(gw);
  return gw;
}

async function until(cond: () => unknown, timeoutMs = 10_000): Promise<void> {
  const end = Date.now() + timeoutMs;
  while (!cond()) {
    if (Date.now() > end) throw new Error("condition not met in time");
    await new Promise((r) => setTimeout(r, 10));
  }
}

/** Runs the client until `done.done` (or until run() ends), stops it, and re-throws what run() threw. */
async function drive(client: MakerClient, done: Json): Promise<MakerClient> {
  let finished = false;
  const task = client.run().finally(() => {
    finished = true;
  });
  task.catch(() => {});
  try {
    await until(() => done.done || finished);
  } finally {
    client.stop();
  }
  await task;
  return client;
}

function key() {
  const k = generatePrivateKey();
  return { key: k, address: privateKeyToAccount(k).address };
}

function clientFor(
  gw: FakeGateway,
  signer: Hex,
  pricer: MakerClientOptions["pricer"],
  opts: Partial<MakerClientOptions> = {},
) {
  return new MakerClient({
    url: gw.url,
    signer,
    pricer,
    chainId: 46630,
    settler: SETTLER,
    backoffInitialMs: 50,
    logger: silentLogger,
    ...opts,
  });
}

describe("MakerClient", () => {
  it("signs a valid quote and follows it to the fill", async () => {
    const acct = key();
    const events: ServerFrame[] = [];
    const listened: string[] = [];
    const out: Json = {};
    const script: Script = async (_gw, peer) => {
      peer.send(rfqFrame("r1"));
      out.q1 = await peer.recv();
      peer.send(rfqFrame("r2", { side: 1 }));
      out.q2 = await peer.recv();
      const d = digestOf(out.q1);
      out.digest = d;
      peer.send({ type: "won", rfqId: "r1", digest: d });
      peer.send({ type: "lost", rfqId: "r2" });
      peer.send({ type: "fill", digest: d, txHash: `0x${"ab".repeat(32)}`, amount: out.q1.quote.amount, rfqId: "r1" });
      await until(() => events.length >= 4);
      out.done = true;
      await peer.closed;
    };
    const gw = await gateway([acct.address], { script });
    const c = clientFor(gw, acct.key, () => 2_500_000n, { onEvent: (f) => void events.push(f) });
    for (const e of ["won", "lost", "fill"] as const) c.on(e, (f) => void listened.push(f.type));
    await drive(c, out);

    const now = Math.floor(Date.now() / 1000);
    for (const f of [out.q1, out.q2]) {
      const q = f.quote;
      expect(f.type).toBe("quote");
      expect(await recoverQuote(f)).toBe(acct.address);
      expect(q.maker).toBe(acct.address);
      expect(q.signer).toBe(acct.address);
      expect(q.taker).toBe(TAKER);
      expect(q.amount).toBe("1000000000000000000");
      expect(q.minFill).toBe("1000000000000000000");
      expect(q.premium).toBe("2500000");
      expect(typeof q.validUntil).toBe("number");
      expect(q.validUntil).toBeGreaterThanOrEqual(now + 50);
      expect(q.validUntil).toBeLessThanOrEqual(now + 120);
      expect(typeof q.series.expiry).toBe("number");
      expect(q.epoch).toBe("0");
      expect(q.externalAccount).toBe(zeroAddress);
      expect(f.signature).toHaveLength(132);
    }
    expect(out.q2.quote.side).toBe(1);
    expect(out.q1.quote.nonce).not.toBe(out.q2.quote.nonce);
    expect(events.map((e) => e.type)).toEqual(["ready", "won", "lost", "fill"]);
    expect(listened).toEqual(["won", "lost", "fill"]);
    const ready = events[0] as Extract<ServerFrame, { type: "ready" }>;
    expect(ready.feeBps).toBe(1250);
    expect(ready.notionalFeeBps).toBe(100);
    expect(c.quoteFor("r1")?.digest).toBe(out.digest);
    expect((events[1] as Extract<ServerFrame, { type: "won" }>).digest).toBe(out.digest);
    expect((events[3] as Extract<ServerFrame, { type: "fill" }>).digest).toBe(out.digest);
  });

  it("logs in with a delegated hot signer and signs for the maker", async () => {
    const maker = key().address;
    const hot = key();
    const out: Json = {};
    const gw = await gateway([maker], {
      delegates: [[maker, hot.address]],
      script: async (_gw, peer) => {
        peer.send(rfqFrame("r1"));
        out.q = await peer.recv();
        out.done = true;
        await peer.closed;
      },
    });
    await drive(
      clientFor(gw, hot.key, () => 1_000_000, { maker }),
      out,
    );
    expect(gw.logins[0]?.maker).toBe(maker);
    expect(gw.logins[0]?.signer).toBe(hot.address);
    expect(out.q.quote.maker).toBe(maker);
    expect(out.q.quote.signer).toBe(hot.address);
    expect(await recoverQuote(out.q)).toBe(hot.address);
  });

  it("raises a refused login and does not retry it", async () => {
    const maker = key().address;
    const stranger = key();
    const gw = await gateway([maker]);
    await expect(
      drive(
        clientFor(gw, stranger.key, () => 1n, { maker }),
        {},
      ),
    ).rejects.toThrow(/not a live delegate/);
    await expect(
      drive(
        clientFor(gw, stranger.key, () => 1n),
        {},
      ),
    ).rejects.toThrow(LoginRefused);
    expect(gw.logins.at(-1)?.maker).toBe(stranger.address);
    expect(gw.connections).toBe(2);
  });

  it("enforces the pinned deployment", async () => {
    const acct = key();
    const gw = await gateway([acct.address]);
    await expect(
      drive(
        clientFor(gw, acct.key, () => 1n, { settler: `0x${"11".repeat(20)}` }),
        {},
      ),
    ).rejects.toThrow(/settler/);
    const wrongChain = drive(
      clientFor(gw, acct.key, () => 1n, { chainId: 1 }),
      {},
    );
    await expect(wrongChain).rejects.toThrow(ProtocolError);
    await expect(wrongChain).rejects.toThrow(/chain/);
  });

  it("declines", async () => {
    const acct = key();
    const out: Json = {};
    const box: { c?: MakerClient } = {};
    const pricer = (rfq: Parameters<MakerClientOptions["pricer"]>[0]): PricerResult => {
      if (rfq.rfqId === "boom") throw new Error("model blew up");
      if (rfq.rfqId === "bad") return replaceQuote(box.c!.buildQuote(rfq, 5n), { taker: `0x${"11".repeat(20)}` });
      if (rfq.rfqId === "float") return 1.5;
      if (rfq.side !== 0) return new Decline("no_inventory");
      if (rfq.amount > 10n ** 18n) return "too_big";
      return null;
    };
    const cases = [
      rfqFrame("sell", { side: 1 }),
      rfqFrame("big", { amount: 10n ** 19n }),
      rfqFrame("none"),
      rfqFrame("other", { settler: `0x${"22".repeat(20)}` }),
      rfqFrame("late", { deadlineMs: -5 }),
      rfqFrame("boom"),
      rfqFrame("bad"),
      rfqFrame("float"),
      { type: "rfq", rfqId: "broken", side: 0 },
    ];
    const gw = await gateway([acct.address], {
      script: async (_gw, peer) => {
        for (const f of cases) peer.send(f);
        const got: Json[] = [];
        for (const _ of cases) got.push(await peer.recv());
        out.reasons = Object.fromEntries(got.map((f) => [f.rfqId, [f.type, f.reason]]));
        out.done = true;
        await peer.closed;
      },
    });
    box.c = clientFor(gw, acct.key, pricer);
    await drive(box.c, out);
    expect(out.reasons).toEqual({
      sell: ["decline", "no_inventory"],
      big: ["decline", "too_big"],
      none: ["decline", "no_price"],
      other: ["decline", "wrong_settler"],
      late: ["decline", "late"],
      boom: ["decline", "maker_error"],
      bad: ["decline", "maker_error"],
      float: ["decline", "maker_error"],
      broken: ["decline", "invalid_rfq"],
    });
  });

  it("declines a slow async pricer before the deadline", async () => {
    const acct = key();
    const out: Json = {};
    let aborted = false;
    const pricer = async (rfq: { rfqId: string }, ctx: { signal: AbortSignal; budgetMs: number }) => {
      if (rfq.rfqId === "slow") {
        expect(ctx.budgetMs).toBeLessThanOrEqual(600 - 150);
        ctx.signal.addEventListener("abort", () => {
          aborted = true;
        });
        await new Promise((r) => setTimeout(r, 5000));
      }
      return 7_000_000n;
    };
    const gw = await gateway([acct.address], {
      script: async (_gw, peer) => {
        const f = rfqFrame("slow", { deadlineMs: 600 });
        peer.send(f);
        out.slow = await peer.recv();
        out.slowAt = Date.now();
        out.respondBy = f.respondBy;
        peer.send(rfqFrame("fast"));
        out.fast = await peer.recv();
        out.done = true;
        await peer.closed;
      },
    });
    await drive(clientFor(gw, acct.key, pricer), out);
    expect(out.slow).toEqual({ type: "decline", rfqId: "slow", reason: "timeout" });
    expect(out.slowAt).toBeLessThanOrEqual(out.respondBy);
    expect(aborted).toBe(true);
    expect(out.fast.type).toBe("quote");
    expect(out.fast.quote.premium).toBe("7000000");
  });

  it("sends a custom quote from buildQuote", async () => {
    const acct = key();
    const treasury = key().address;
    const out: Json = {};
    const box: { c?: MakerClient } = {};
    const gw = await gateway([acct.address], {
      script: async (_gw, peer) => {
        const f = rfqFrame("r1");
        peer.send(f);
        out.q = await peer.recv();
        out.min = f.minValidUntil;
        out.done = true;
        await peer.closed;
      },
    });
    box.c = clientFor(gw, acct.key, (rfq) =>
      box.c!.buildQuote(rfq, 3_000_000n, { validFor: 30, externalAccount: treasury }),
    );
    await drive(box.c, out);
    const q = out.q.quote;
    expect(q.externalAccount).toBe(treasury);
    expect(await recoverQuote(out.q)).toBe(acct.address);
    expect(q.validUntil).toBeGreaterThanOrEqual(out.min);
    expect(q.validUntil).toBeLessThanOrEqual(Math.floor(Date.now() / 1000) + 31);
  });

  for (const [label, reader, want] of [
    ["from the rejection's reason", undefined, "4"],
    ["from the epoch reader", (_m: Address) => 9n, "9"],
  ] as const) {
    it(`moves the epoch after a stale_epoch rejection (${label})`, async () => {
      const acct = key();
      const out: Json = {};
      const gw = await gateway([acct.address], {
        script: async (_gw, peer) => {
          peer.send(rfqFrame("r1"));
          out.q1 = await peer.recv();
          peer.send({ type: "rejected", rfqId: "r1", code: "stale_epoch", reason: "quote epoch 0, settler epoch 4" });
          peer.send(rfqFrame("r2"));
          out.q2 = await peer.recv();
          out.done = true;
          await peer.closed;
        },
      });
      await drive(
        clientFor(gw, acct.key, () => 1_000_000n, reader ? { epochReader: reader } : {}),
        out,
      );
      expect(out.q1.quote.epoch).toBe("0");
      expect(out.q2.quote.epoch).toBe(want);
    });
  }

  it("signs under the epoch the gateway announces", async () => {
    const acct = key();
    const out: Json = {};
    const gw = await gateway([acct.address], {
      epoch: 12n,
      script: async (_gw, peer) => {
        peer.send(rfqFrame("r1"));
        out.q = await peer.recv();
        out.done = true;
        await peer.closed;
      },
    });
    const c = await drive(
      clientFor(gw, acct.key, () => 1_000_000n),
      out,
    );
    expect(out.q.quote.epoch).toBe("12");
    expect(c.session?.epoch).toBe(12n);
  });

  it("reconnects and keeps the pause across connections", async () => {
    const acct = key();
    const out: Json = {};
    const gw = await gateway([acct.address], {
      script: async (_gw, peer: Peer, _m, n) => {
        if (n === 1) {
          out.first = await peer.recv(); // the pause the test sends once logged in
          peer.send({ type: "paused" });
          peer.close(); // the gateway restarts
          return;
        }
        out.second = await peer.recv(); // re-sent on the new connection
        peer.send({ type: "paused" });
        peer.send(rfqFrame("r1"));
        out.q = await peer.recv();
        out.done = true;
        await peer.closed;
      },
    });
    const paused: string[] = [];
    const c = clientFor(gw, acct.key, () => 1_000_000n);
    c.on("paused", (f) => void paused.push(f.type));
    const task = c.run();
    await until(() => c.session !== undefined);
    c.pause();
    await until(() => out.done);
    c.stop();
    await task;
    expect(out.first).toEqual({ type: "pause" });
    expect(out.second).toEqual({ type: "pause" });
    expect(gw.connections).toBe(2);
    expect(out.q.type).toBe("quote");
    expect(paused).toEqual(["paused", "paused"]);
  });

  it("answers the gateway's ping", async () => {
    const acct = key();
    const out: Json = {};
    const gw = await gateway([acct.address], {
      script: async (_gw, peer) => {
        out.latency = await peer.ping(); // the hub drops a maker that does not pong within 5 s
        out.done = true;
        await peer.closed;
      },
    });
    await drive(
      clientFor(gw, acct.key, () => 1n),
      out,
    );
    expect(out.latency).toBeLessThan(5000);
  });

  it("drops a gateway that stops answering pings, and reconnects", async () => {
    const acct = key();
    const out: Json = {};
    const gw = await gateway([acct.address], {
      autoPong: false,
      script: async (g, peer) => {
        if (g.connections >= 2) out.done = true;
        await peer.closed;
      },
    });
    await drive(
      clientFor(gw, acct.key, () => 1n, { pingIntervalMs: 100 }),
      out,
    );
    expect(gw.connections).toBeGreaterThanOrEqual(2);
  });

  it("keeps the connection when an event handler throws", async () => {
    const acct = key();
    const out: Json = {};
    const gw = await gateway([acct.address], {
      script: async (_gw, peer) => {
        peer.send({ type: "lost", rfqId: "x" });
        peer.send(rfqFrame("r1"));
        out.q = await peer.recv();
        out.done = true;
        await peer.closed;
      },
    });
    const c = clientFor(gw, acct.key, () => 1_000_000n);
    c.on("lost", () => {
      throw new Error("handler bug");
    });
    c.on("lost", async () => {
      throw new Error("async handler bug");
    });
    await drive(c, out);
    expect(out.q.type).toBe("quote");
    expect(gw.connections).toBe(1);
  });
});
