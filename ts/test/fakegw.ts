/**
 * An in-process stand-in for the gateway's maker socket (rfq-gateway internal/hub/hub.go): challenge login with the
 * allowlist and delegated signers, then a per-test script. It checks quotes the way the gateway does, with viem's own
 * EIP-712 encoder and typed definitions written out here (independent of the SDK's encoding).
 */

import { randomBytes } from "node:crypto";
import type { AddressInfo } from "node:net";
import { type Address, getAddress, type Hex, hashTypedData, recoverAddress, recoverMessageAddress } from "viem";
import { type WebSocket, WebSocketServer } from "ws";

export const CHAIN = 46630;
export const SETTLER = "0xda43b0933031e41D5B367E1807536FcF4ebdEE7e";
export const TAKER = "0xa6E328D981E199bD7A9C466AC3684679CF3a6D97";
export const SERIES = {
  underlying: "0x6661d20c2e343bb78b42535eb060c3058c744f3f",
  quote: "0x850de815d41f6983aaf09247f9a465c0aefff04c",
  oracle: "0x9e6c529caa69d44b63df27483c57f97a42d6e898",
  expiry: 1794603600,
  isPut: true,
  strike: "330000000",
  physical: false,
};

// biome-ignore lint/suspicious/noExplicitAny: wire frames are untyped JSON
export type Json = Record<string, any>;

export function rfqFrame(
  rfqId = "r1",
  { side = 0, amount = 10n ** 18n, deadlineMs = 2000, settler = SETTLER, ...over }: Json = {},
): Json {
  const nowMs = Date.now();
  return {
    type: "rfq",
    rfqId,
    chainId: CHAIN,
    settler: settler.toLowerCase(),
    taker: TAKER.toLowerCase(),
    side,
    series: { ...SERIES },
    amount: amount.toString(),
    respondBy: nowMs + deadlineMs,
    minValidUntil: Math.floor((nowMs + deadlineMs) / 1000) + 15,
    ...over,
  };
}

const TYPES = {
  SeriesParams: [
    { name: "underlying", type: "address" },
    { name: "quote", type: "address" },
    { name: "oracle", type: "address" },
    { name: "expiry", type: "uint64" },
    { name: "isPut", type: "bool" },
    { name: "strike", type: "uint256" },
    { name: "physical", type: "bool" },
  ],
  Quote: [
    { name: "maker", type: "address" },
    { name: "signer", type: "address" },
    { name: "taker", type: "address" },
    { name: "externalAccount", type: "address" },
    { name: "series", type: "SeriesParams" },
    { name: "side", type: "uint8" },
    { name: "amount", type: "uint256" },
    { name: "minFill", type: "uint256" },
    { name: "premium", type: "uint256" },
    { name: "validUntil", type: "uint64" },
    { name: "nonce", type: "uint256" },
    { name: "epoch", type: "uint256" },
  ],
} as const;

export function digestOf(frame: Json, chain = CHAIN, settler = SETTLER): Hex {
  const q = frame.quote;
  const s = q.series;
  return hashTypedData({
    domain: { name: "Hedgehood RFQ Settler", version: "2", chainId: chain, verifyingContract: getAddress(settler) },
    types: TYPES,
    primaryType: "Quote",
    message: {
      maker: q.maker,
      signer: q.signer,
      taker: q.taker,
      externalAccount: q.externalAccount,
      series: {
        underlying: s.underlying,
        quote: s.quote,
        oracle: s.oracle,
        expiry: BigInt(s.expiry),
        isPut: s.isPut,
        strike: BigInt(s.strike),
        physical: s.physical,
      },
      side: q.side,
      amount: BigInt(q.amount),
      minFill: BigInt(q.minFill),
      premium: BigInt(q.premium),
      validUntil: BigInt(q.validUntil),
      nonce: BigInt(q.nonce),
      epoch: BigInt(q.epoch),
    },
  });
}

export function recoverQuote(frame: Json): Promise<Address> {
  return recoverAddress({ hash: digestOf(frame), signature: frame.signature });
}

/** One maker connection as the gateway sees it. */
export class Peer {
  private readonly queue: string[] = [];
  private waiter: ((v: string) => void) | undefined;
  readonly closed: Promise<void>;

  constructor(readonly ws: WebSocket) {
    ws.on("message", (d) => {
      const s = d.toString();
      const w = this.waiter;
      this.waiter = undefined;
      if (w) w(s);
      else this.queue.push(s);
    });
    this.closed = new Promise((resolve) => ws.on("close", () => resolve()));
  }

  send(frame: Json): void {
    this.ws.send(JSON.stringify(frame));
  }

  recv(timeoutMs = 5000): Promise<Json> {
    const q = this.queue.shift();
    if (q !== undefined) return Promise.resolve(JSON.parse(q));
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("fake gateway: no frame in time")), timeoutMs);
      this.waiter = (s) => {
        clearTimeout(t);
        resolve(JSON.parse(s));
      };
    });
  }

  /** Round-trip time of a WebSocket ping, ms (the hub drops a maker that does not pong within 5 s). */
  ping(timeoutMs = 5000): Promise<number> {
    const t0 = Date.now();
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("no pong")), timeoutMs);
      this.ws.once("pong", () => {
        clearTimeout(t);
        resolve(Date.now() - t0);
      });
      this.ws.ping();
    });
  }

  close(code?: number, reason?: string): void {
    this.ws.close(code, reason);
  }
}

export type Script = (gw: FakeGateway, peer: Peer, maker: Address, n: number) => Promise<void>;

export class FakeGateway {
  readonly makers: Set<Address>;
  readonly delegates: Set<string>;
  epoch = 0n;
  feeBps = 1250;
  notionalFeeBps = 100;
  settler = SETTLER;
  script: Script | undefined;
  connections = 0;
  readonly logins: Json[] = [];
  url = "";
  private server: WebSocketServer | undefined;

  readonly autoPong: boolean;

  constructor(
    makers: string[],
    opts: {
      delegates?: Array<[string, string]>;
      script?: Script;
      settler?: string;
      epoch?: bigint;
      /** false: never answer the maker's pings (a hung gateway). */
      autoPong?: boolean;
    } = {},
  ) {
    this.makers = new Set(makers.map((m) => getAddress(m)));
    this.delegates = new Set((opts.delegates ?? []).map(([m, s]) => `${getAddress(m)}|${getAddress(s)}`));
    this.script = opts.script;
    if (opts.settler) this.settler = opts.settler;
    if (opts.epoch !== undefined) this.epoch = opts.epoch;
    this.autoPong = opts.autoPong ?? true;
  }

  async start(): Promise<this> {
    const server = new WebSocketServer({ host: "127.0.0.1", port: 0, autoPong: this.autoPong });
    await new Promise<void>((resolve) => server.once("listening", () => resolve()));
    server.on("connection", (ws) => void this.handle(new Peer(ws)));
    this.server = server;
    this.url = `ws://127.0.0.1:${(server.address() as AddressInfo).port}/v1/maker/ws`;
    return this;
  }

  async close(): Promise<void> {
    const server = this.server;
    if (!server) return;
    for (const c of server.clients) c.terminate();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }

  private async handle(peer: Peer): Promise<void> {
    this.connections += 1;
    const n = this.connections;
    const nonce = randomBytes(16).toString("hex");
    peer.send({ type: "challenge", nonce, chainId: CHAIN });
    let login: Json;
    try {
      login = await peer.recv(10_000);
    } catch {
      peer.close(1008, "login");
      return;
    }
    this.logins.push(login);
    const err = await this.checkLogin(login, nonce);
    if (err) {
      peer.send({ type: "error", code: "login", reason: err });
      peer.close(1008, "login");
      return;
    }
    const maker = getAddress(login.maker);
    peer.send({
      type: "ready",
      maker,
      settler: this.settler,
      feeBps: this.feeBps,
      notionalFeeBps: this.notionalFeeBps,
      epoch: this.epoch.toString(),
    });
    try {
      if (this.script) await this.script(this, peer, maker, n);
      else await peer.closed;
    } catch (e) {
      console.error("fake gateway script failed", e);
      peer.close();
    }
  }

  private async checkLogin(login: Json, nonce: string): Promise<string | undefined> {
    if (login.type !== "login") return "expected a login frame";
    const maker = getAddress(login.maker);
    const signer = getAddress(login.signer);
    if (!this.makers.has(maker)) return `maker ${maker} is not on the allowlist`;
    const msg = `hedgehood-rfq-login:${CHAIN}:${maker.toLowerCase()}:${nonce}`;
    if ((await recoverMessageAddress({ message: msg, signature: login.signature })) !== signer) {
      return "login signature does not recover to signer";
    }
    if (signer !== maker && !this.delegates.has(`${maker}|${signer}`)) {
      return "signer is not a live delegate of the maker on the settler";
    }
    return undefined;
  }
}
