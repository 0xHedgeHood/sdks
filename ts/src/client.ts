/**
 * MakerClient: the maker's side of the gateway WebSocket (wss://<gateway>/v1/maker/ws). Mirrors
 * hedgehood_maker.client.
 *
 *     const client = new MakerClient({ url: TESTNET.gatewayWs, signer: hotKey, pricer, maker: MAKER });
 *     client.on("won", (f) => console.log("won", f.rfqId));
 *     await client.run();
 *
 * The client logs in (EIP-191 challenge signed by the signer key), calls the pricer for every request under the
 * request's deadline, builds and signs the firm EIP-712 quote (fresh nonce, current epoch, minFill = amount), checks
 * it the way the gateway will, and reconnects with backoff when the socket drops. Pings from the gateway are answered
 * by the ws library; the client also pings the gateway and drops a connection that stops answering.
 *
 * Pricer results: a bigint (or safe-integer number) is the premium (quote raw for the whole amount, fee excluded); a
 * Quote (e.g. from `client.buildQuote`) is sent as is after the local checks; a Decline, a string (the reason), null
 * or undefined declines. Declining is cheap and keeps you off the silence breaker (20 unanswered requests in a row
 * pause a maker for 15 minutes). The pricer runs on the event loop: keep it asynchronous or quick.
 */

import type { Address, Hex, LocalAccount } from "viem";
import WebSocket, { type ClientOptions } from "ws";
import { LOGIN_TIMEOUT, MAX_FRAME, MAX_QUOTE_LIFE, ZERO_ADDRESS } from "./constants.js";
import { quoteDigest, type SignerKey, signLogin, signQuote, toAccount } from "./eip712.js";
import {
  address,
  type Challenge,
  Decline,
  declineFrame,
  type ErrorFrame,
  type Fill,
  type Lost,
  loginFrame,
  type MakerFrame,
  makeQuote,
  type Paused,
  parseFrame,
  type Quote,
  type QuoteInput,
  quoteFrame,
  type Ready,
  type Rejected,
  type Resumed,
  type RFQ,
  type ServerFrame,
  seriesEquals,
  type Unknown,
  ValidationError,
  type Won,
} from "./models.js";
import { NonceManager } from "./nonces.js";

export type PricerResult = bigint | number | Quote | QuoteInput | Decline | string | null | undefined;

export interface PriceContext {
  /** Milliseconds the pricer has before the client declines with "timeout". */
  readonly budgetMs: number;
  /** Aborted when the budget runs out or the connection drops. */
  readonly signal: AbortSignal;
}

export type Pricer = (rfq: RFQ, ctx: PriceContext) => PricerResult | Promise<PricerResult>;

/** Leave a few seconds for clock skew between you, the gateway and the chain. */
export const LIFE_CAP = MAX_QUOTE_LIFE - 5;
const EPOCH_IN_REASON = /settler epoch (\d+)/;

/** The gateway refused the login (not allowlisted, signer not delegated, bad signature). Not retried. */
export class LoginRefused extends Error {
  override name = "LoginRefused";
}

/** The gateway sent something the client cannot follow (or a chain/settler other than the one pinned). */
export class ProtocolError extends Error {
  override name = "ProtocolError";
}

class ConnectionClosed extends Error {
  override name = "ConnectionClosed";
}

class Timeout extends Error {
  override name = "Timeout";
}

/** What the gateway told us at login. */
export interface Session {
  maker: Address;
  signer: Address;
  chainId: number;
  settler: Address;
  epoch: bigint;
  feeBps: number;
  notionalFeeBps: number;
}

export interface Logger {
  debug(msg: string, ...args: unknown[]): void;
  info(msg: string, ...args: unknown[]): void;
  warn(msg: string, ...args: unknown[]): void;
  error(msg: string, ...args: unknown[]): void;
}

/** Warnings and errors to the console, info and debug dropped. */
export const defaultLogger: Logger = {
  debug() {},
  info() {},
  warn: (msg, ...args) => console.warn(`hedgehood-maker: ${msg}`, ...args),
  error: (msg, ...args) => console.error(`hedgehood-maker: ${msg}`, ...args),
};

export const silentLogger: Logger = { debug() {}, info() {}, warn() {}, error() {} };

/** Events after login. `frame` sees every server frame except requests (which go to the pricer). */
export interface MakerEvents {
  challenge: Challenge;
  ready: Ready;
  won: Won;
  lost: Lost;
  fill: Fill;
  rejected: Rejected;
  error: ErrorFrame;
  paused: Paused;
  resumed: Resumed;
  unknown: Unknown;
  frame: Exclude<ServerFrame, RFQ>;
}

type Listener<T> = (payload: T) => unknown;

export interface MakerClientOptions {
  /** The gateway's maker socket, e.g. TESTNET.gatewayWs. */
  url: string;
  /** The maker's key, or a hot key the maker delegated with setSigner (0x private key or a viem LocalAccount). */
  signer: SignerKey;
  pricer: Pricer;
  /** The maker account (default: the signer's address). */
  maker?: string;
  /** externalAccount on every built quote (default: zero = the maker's settler balance). */
  externalAccount?: string;
  /** Seconds a quote stays valid (default 60, capped at 115; the gateway needs >= 15 s left after the auction). */
  quoteLife?: number;
  /** Pin the deployment: the client refuses a gateway that announces another chain ... */
  chainId?: number;
  /** ... or another settler. */
  settler?: string;
  /** Called with every frame after login (Ready, Won, Lost, Fill, Rejected, ErrorFrame, Paused, ...). */
  onEvent?: (frame: Exclude<ServerFrame, RFQ>) => unknown;
  nonces?: NonceManager;
  /** maker -> settler.epoch(maker) (e.g. reader.epoch), used after a stale_epoch rejection. */
  epochReader?: (maker: Address) => bigint | number | Promise<bigint | number>;
  /** Milliseconds kept back from the request's deadline (default 150). */
  answerMarginMs?: number;
  backoffInitialMs?: number;
  backoffMaxMs?: number;
  /** Unix milliseconds (default Date.now). */
  clock?: () => number;
  openTimeoutMs?: number;
  /** The client pings the gateway this often and drops a connection whose pong has not arrived by the next ping. */
  pingIntervalMs?: number;
  logger?: Logger;
  /** Extra options for the ws client (headers, agent, ...). */
  wsOptions?: ClientOptions;
}

/** One WebSocket connection: an async queue of text frames, liveness pings and a send that never throws. */
class Connection {
  readonly abort = new AbortController();
  private readonly queue: string[] = [];
  private waiter: ((v: string | null) => void) | undefined;
  private closed = false;
  private alive = true;
  private pinger: ReturnType<typeof setInterval> | undefined;
  error: Error | undefined;

  constructor(
    readonly ws: WebSocket,
    private readonly log: Logger,
  ) {
    ws.on("message", (data, isBinary) => {
      if (isBinary) return;
      this.push(data.toString());
    });
    ws.on("pong", () => {
      this.alive = true;
    });
    ws.on("error", (err) => {
      this.error = err;
    });
    ws.on("close", () => {
      this.closed = true;
      if (this.pinger) clearInterval(this.pinger);
      this.abort.abort();
      const w = this.waiter;
      this.waiter = undefined;
      w?.(null);
    });
  }

  opened(timeoutMs: number): Promise<void> {
    return new Promise((resolve, reject) => {
      if (this.ws.readyState === WebSocket.OPEN) return resolve();
      const t = setTimeout(() => {
        this.ws.terminate();
        reject(new Timeout("opening the connection timed out"));
      }, timeoutMs);
      this.ws.once("open", () => {
        clearTimeout(t);
        resolve();
      });
      this.ws.once("close", () => {
        clearTimeout(t);
        reject(this.error ?? new ConnectionClosed("connection closed before it opened"));
      });
    });
  }

  startPings(intervalMs: number): void {
    if (intervalMs <= 0) return;
    this.pinger = setInterval(() => {
      if (!this.alive) {
        this.log.warn("gateway stopped answering pings; dropping the connection");
        this.ws.terminate();
        return;
      }
      this.alive = false;
      try {
        this.ws.ping();
      } catch {
        // closing
      }
    }, intervalMs);
    this.pinger.unref?.();
  }

  private push(raw: string): void {
    const w = this.waiter;
    if (w) {
      this.waiter = undefined;
      w(raw);
    } else {
      this.queue.push(raw);
    }
  }

  /** The next text frame, or null once the socket has closed. */
  next(timeoutMs?: number): Promise<string | null> {
    const queued = this.queue.shift();
    if (queued !== undefined) return Promise.resolve(queued);
    if (this.closed) return Promise.resolve(null);
    return new Promise((resolve, reject) => {
      let t: ReturnType<typeof setTimeout> | undefined;
      this.waiter = (v) => {
        if (t) clearTimeout(t);
        resolve(v);
      };
      if (timeoutMs !== undefined) {
        t = setTimeout(() => {
          this.waiter = undefined;
          reject(new Timeout("the gateway did not answer in time"));
        }, timeoutMs);
      }
    });
  }

  send(frame: MakerFrame): boolean {
    if (this.closed || this.ws.readyState !== WebSocket.OPEN) return false;
    const text = JSON.stringify(frame);
    if (Buffer.byteLength(text) > MAX_FRAME) {
      this.log.error(
        `not sending a ${frame.type} frame of ${Buffer.byteLength(text)} bytes (gateway limit ${MAX_FRAME})`,
      );
      return false;
    }
    this.ws.send(text, (err) => {
      if (err) this.log.debug(`send failed: ${err.message}`);
    });
    return true;
  }

  close(): void {
    if (this.pinger) clearInterval(this.pinger);
    if (this.ws.readyState === WebSocket.CONNECTING) this.ws.terminate();
    else if (this.ws.readyState === WebSocket.OPEN) this.ws.close(1000);
  }
}

export class MakerClient {
  readonly url: string;
  readonly signer: Address;
  readonly maker: Address;
  /** externalAccount on built quotes; may be changed between requests. */
  externalAccount: Address;
  readonly quoteLife: number;
  readonly expectedChainId: number | undefined;
  readonly expectedSettler: Address | undefined;
  pricer: Pricer;
  readonly nonces: NonceManager;
  session: Session | undefined;

  private readonly account: LocalAccount;
  private readonly key: SignerKey;
  private readonly opts: MakerClientOptions;
  private readonly log: Logger;
  private readonly clock: () => number;
  private readonly listeners = new Map<keyof MakerEvents, Set<Listener<never>>>();
  private readonly sent = new Map<string, { digest: Hex; quote: Quote }>();
  private conn: Connection | undefined;
  private stopping = false;
  private wakeups = new Set<() => void>();
  private wantPaused = false;

  constructor(opts: MakerClientOptions) {
    this.opts = opts;
    this.url = opts.url;
    this.key = opts.signer;
    this.account = toAccount(opts.signer);
    this.signer = address(this.account.address, "signer");
    this.maker = opts.maker ? address(opts.maker, "maker") : this.signer;
    this.externalAccount = opts.externalAccount ? address(opts.externalAccount, "externalAccount") : ZERO_ADDRESS;
    const life = opts.quoteLife ?? 60;
    if (!(life > 0)) throw new ValidationError("quoteLife must be positive");
    this.quoteLife = Math.min(Math.floor(life), LIFE_CAP);
    this.expectedChainId = opts.chainId;
    this.expectedSettler = opts.settler ? address(opts.settler, "settler") : undefined;
    this.pricer = opts.pricer;
    this.nonces = opts.nonces ?? new NonceManager();
    this.log = opts.logger ?? defaultLogger;
    this.clock = opts.clock ?? Date.now;
  }

  // ------------------------------------------------------------------ events

  on<K extends keyof MakerEvents>(event: K, fn: Listener<MakerEvents[K]>): this {
    let set = this.listeners.get(event);
    if (!set) {
      set = new Set();
      this.listeners.set(event, set);
    }
    set.add(fn as Listener<never>);
    return this;
  }

  off<K extends keyof MakerEvents>(event: K, fn: Listener<MakerEvents[K]>): this {
    this.listeners.get(event)?.delete(fn as Listener<never>);
    return this;
  }

  once<K extends keyof MakerEvents>(event: K, fn: Listener<MakerEvents[K]>): this {
    const wrap: Listener<MakerEvents[K]> = (p) => {
      this.off(event, wrap);
      return fn(p);
    };
    return this.on(event, wrap);
  }

  /** onEvent, the frame's own event and `frame`; a handler that throws or rejects never drops the connection. */
  private async emit(frame: Exclude<ServerFrame, RFQ>): Promise<void> {
    const calls: Array<() => unknown> = [];
    if (this.opts.onEvent) {
      const cb = this.opts.onEvent;
      calls.push(() => cb(frame));
    }
    for (const event of [frame.type, "frame"] as const) {
      for (const fn of this.listeners.get(event) ?? []) calls.push(() => (fn as Listener<typeof frame>)(frame));
    }
    for (const call of calls) {
      try {
        await call();
      } catch (e) {
        this.log.error(`event handler failed on ${frame.type}`, e);
      }
    }
  }

  // ------------------------------------------------------------------ lifecycle

  /**
   * Connect, log in and answer until `stop()`; reconnect with exponential backoff and jitter. A refused login rejects
   * with LoginRefused; a gateway on another chain or settler than pinned rejects with ProtocolError.
   */
  async run(): Promise<void> {
    const initial = this.opts.backoffInitialMs ?? 1_000;
    const max = this.opts.backoffMaxMs ?? 30_000;
    let delay = initial;
    while (!this.stopping) {
      let loggedIn = false;
      try {
        loggedIn = await this.runOnce();
      } catch (e) {
        if (e instanceof LoginRefused || e instanceof ProtocolError) throw e;
        this.log.warn(`gateway connection ended: ${e instanceof Error ? `${e.name}: ${e.message}` : String(e)}`);
      }
      if (this.stopping) break;
      if (loggedIn) delay = initial;
      const wait = delay * (0.8 + Math.random() * 0.4);
      this.log.info(`reconnecting in ${(wait / 1000).toFixed(1)} s`);
      await this.sleep(wait);
      delay = Math.min(delay * 2, max);
    }
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      const done = () => {
        clearTimeout(t);
        this.wakeups.delete(done);
        resolve();
      };
      const t = setTimeout(done, ms);
      this.wakeups.add(done);
    });
  }

  /** One connection: login, then answer until the socket closes. Resolves to whether the login succeeded. */
  async runOnce(): Promise<boolean> {
    const ws = new WebSocket(this.url, {
      handshakeTimeout: this.opts.openTimeoutMs ?? 10_000,
      maxPayload: 1 << 20,
      ...this.opts.wsOptions,
    });
    const conn = new Connection(ws, this.log);
    this.conn = conn;
    let loggedIn = false;
    try {
      await conn.opened(this.opts.openTimeoutMs ?? 10_000);
      if (this.stopping) return false;
      conn.startPings(this.opts.pingIntervalMs ?? 20_000);
      await this.login(conn);
      loggedIn = true;
      if (this.wantPaused) conn.send({ type: "pause" });
      for (;;) {
        const raw = await conn.next();
        if (raw === null) break;
        await this.dispatch(conn, raw);
      }
    } finally {
      if (this.conn === conn) this.conn = undefined;
      conn.abort.abort();
      conn.close();
    }
    return loggedIn;
  }

  /** Ends `run()` after closing the socket. */
  stop(): void {
    this.stopping = true;
    this.conn?.close();
    for (const wake of [...this.wakeups]) wake();
  }

  /** Stop receiving requests (kept across reconnects until `resume`). */
  pause(): void {
    this.wantPaused = true;
    this.conn?.send({ type: "pause" });
  }

  resume(): void {
    this.wantPaused = false;
    this.conn?.send({ type: "resume" });
  }

  /** After your own increaseEpoch: sign future quotes under the new epoch. */
  setEpoch(epoch: bigint | number): void {
    if (!this.session) throw new Error("not logged in");
    this.session.epoch = BigInt(epoch);
  }

  /** {digest, quote} this client sent for a request (the last 1024 kept). */
  quoteFor(rfqId: string): { digest: Hex; quote: Quote } | undefined {
    return this.sent.get(rfqId);
  }

  // ------------------------------------------------------------------ login

  private async recvFrame(conn: Connection): Promise<ServerFrame> {
    const raw = await conn.next((LOGIN_TIMEOUT + 5) * 1000);
    if (raw === null) throw conn.error ?? new ConnectionClosed("the gateway closed the connection during login");
    return parseFrame(JSON.parse(raw));
  }

  private async login(conn: Connection): Promise<void> {
    const ch = await this.recvFrame(conn);
    if (ch.type !== "challenge") throw new ProtocolError(`expected a challenge, got ${JSON.stringify(ch.raw)}`);
    if (this.expectedChainId !== undefined && ch.chainId !== this.expectedChainId) {
      throw new ProtocolError(`gateway is on chain ${ch.chainId}, expected ${this.expectedChainId}`);
    }
    const sig = await signLogin(ch.chainId, this.maker, ch.nonce, this.key);
    conn.send(loginFrame(this.maker, this.signer, sig));
    const first = await this.recvFrame(conn);
    if (first.type === "error") throw new LoginRefused(`${first.code}: ${first.reason}`);
    if (first.type !== "ready") throw new ProtocolError(`expected ready, got ${JSON.stringify(first.raw)}`);
    if (first.maker !== this.maker) {
      throw new ProtocolError(`gateway logged us in as ${first.maker}, expected ${this.maker}`);
    }
    if (this.expectedSettler !== undefined && first.settler !== this.expectedSettler) {
      throw new ProtocolError(`gateway settler ${first.settler}, expected ${this.expectedSettler}`);
    }
    this.session = {
      maker: this.maker,
      signer: this.signer,
      chainId: ch.chainId,
      settler: first.settler,
      epoch: first.epoch,
      feeBps: first.feeBps,
      notionalFeeBps: first.notionalFeeBps,
    };
    this.log.info(
      `logged in: maker ${this.maker} signer ${this.signer} settler ${first.settler} epoch ${first.epoch} ` +
        `fee ${first.feeBps} bps (cap ${first.notionalFeeBps} bps of notional)`,
    );
    await this.emit(first);
  }

  // ------------------------------------------------------------------ frames

  private async dispatch(conn: Connection, raw: string): Promise<void> {
    let d: unknown;
    try {
      d = JSON.parse(raw);
    } catch {
      this.log.warn("gateway sent a frame that is not JSON");
      return;
    }
    let frame: ServerFrame;
    try {
      frame = parseFrame(d);
    } catch (e) {
      this.log.warn(`bad frame from gateway: ${e instanceof Error ? e.message : String(e)}`);
      const r = d as Record<string, unknown> | null;
      if (r && typeof r === "object" && r.type === "rfq" && r.rfqId) {
        conn.send(declineFrame(String(r.rfqId), "invalid_rfq"));
      }
      return;
    }
    if (frame.type === "rfq") {
      void this.respond(frame, conn.abort.signal).then((reply) => conn.send(reply));
      return;
    }
    if (frame.type === "rejected") {
      this.log.info(`rejected ${frame.rfqId}: ${frame.code} ${frame.reason}`);
      if (frame.code === "stale_epoch") await this.refreshEpoch(frame);
    } else if (frame.type === "won") {
      this.log.info(`won ${frame.rfqId} digest ${frame.digest}`);
    }
    await this.emit(frame);
  }

  private async refreshEpoch(rej: Rejected): Promise<void> {
    const s = this.session;
    if (!s) return;
    let epoch: bigint | undefined;
    if (this.opts.epochReader) {
      try {
        epoch = BigInt(await this.opts.epochReader(this.maker));
      } catch (e) {
        this.log.warn(`epochReader failed: ${e instanceof Error ? e.message : String(e)}`);
      }
    }
    if (epoch === undefined) {
      const m = EPOCH_IN_REASON.exec(rej.reason);
      epoch = m ? BigInt(m[1]!) : s.epoch + 1n;
    }
    this.log.info(`epoch ${s.epoch} -> ${epoch} after stale_epoch`);
    s.epoch = epoch;
  }

  // ------------------------------------------------------------------ quoting

  /** The frame answering `rfq`: a signed quote or a decline (never rejects). */
  async respond(rfq: RFQ, signal?: AbortSignal) {
    try {
      return await this.respondInner(rfq, signal);
    } catch (e) {
      this.log.error(`answering ${rfq.rfqId} failed`, e);
      return declineFrame(rfq.rfqId, "maker_error");
    }
  }

  private async respondInner(rfq: RFQ, parent?: AbortSignal) {
    const s = this.session;
    if (!s) return declineFrame(rfq.rfqId, "not_ready");
    if (rfq.chainId !== s.chainId || rfq.settler !== s.settler) return declineFrame(rfq.rfqId, "wrong_settler");
    const budgetMs = rfq.respondBy - this.clock() - (this.opts.answerMarginMs ?? 150);
    if (budgetMs <= 0) return declineFrame(rfq.rfqId, "late");

    const ac = new AbortController();
    const onParentAbort = () => ac.abort(parent?.reason);
    parent?.addEventListener("abort", onParentAbort, { once: true });
    const TIMEOUT = Symbol("timeout");
    let timer: ReturnType<typeof setTimeout> | undefined;
    let result: PricerResult | typeof TIMEOUT;
    try {
      result = await Promise.race([
        Promise.resolve().then(() => this.pricer(rfq, { budgetMs, signal: ac.signal })),
        new Promise<typeof TIMEOUT>((resolve) => {
          timer = setTimeout(() => resolve(TIMEOUT), budgetMs);
        }),
      ]);
    } catch (e) {
      this.log.error(`pricer failed on ${rfq.rfqId}`, e);
      return declineFrame(rfq.rfqId, "maker_error");
    } finally {
      clearTimeout(timer);
      parent?.removeEventListener("abort", onParentAbort);
    }
    if (result === TIMEOUT) {
      ac.abort(new Timeout("pricer budget exhausted"));
      this.log.warn(`pricer timed out on ${rfq.rfqId}`);
      return declineFrame(rfq.rfqId, "timeout");
    }

    if (result === null || result === undefined) return declineFrame(rfq.rfqId, "no_price");
    if (result instanceof Decline) return declineFrame(rfq.rfqId, result.reason);
    if (typeof result === "string") return declineFrame(rfq.rfqId, result);
    let q: Quote;
    try {
      if (typeof result === "bigint" || typeof result === "number") {
        if (typeof result === "number" && !Number.isSafeInteger(result)) {
          throw new ValidationError(`pricer returned a non-integer premium ${result}`);
        }
        const premium = BigInt(result);
        if (premium <= 0n) return declineFrame(rfq.rfqId, "no_price");
        q = this.buildQuote(rfq, premium);
      } else if (typeof result === "object") {
        q = makeQuote(result as QuoteInput);
      } else {
        throw new ValidationError(`pricer returned ${typeof result}`);
      }
      this.checkQuote(rfq, q);
    } catch (e) {
      if (!(e instanceof ValidationError)) throw e;
      this.log.error(`not sending an invalid quote for ${rfq.rfqId}: ${e.message}`);
      return declineFrame(rfq.rfqId, "maker_error");
    }
    if (this.clock() > rfq.respondBy) return declineFrame(rfq.rfqId, "late"); // never hand out an unread firm quote
    const sig = await signQuote(q, s.chainId, s.settler, this.key);
    this.sent.set(rfq.rfqId, { digest: quoteDigest(q, s.chainId, s.settler), quote: q });
    while (this.sent.size > 1024) this.sent.delete(this.sent.keys().next().value as string);
    return quoteFrame(rfq.rfqId, q, sig);
  }

  /** The firm quote for `rfq` at `premium` (quote raw for the whole amount, fee excluded), unsigned. */
  buildQuote(
    rfq: RFQ,
    premium: bigint | number | string,
    opts: { validFor?: number; externalAccount?: string; nonce?: bigint } = {},
  ): Quote {
    const s = this.session;
    if (!s) throw new Error("not logged in");
    const now = BigInt(Math.floor(this.clock() / 1000));
    const life = BigInt(Math.min(Math.floor(opts.validFor || this.quoteLife), LIFE_CAP));
    let validUntil = now + life;
    if (validUntil < rfq.minValidUntil) validUntil = rfq.minValidUntil; // bigints: no Math.max
    return makeQuote({
      maker: this.maker,
      signer: this.signer,
      taker: rfq.taker,
      externalAccount: opts.externalAccount ? address(opts.externalAccount, "externalAccount") : this.externalAccount,
      series: rfq.series,
      side: rfq.side,
      amount: rfq.amount,
      minFill: rfq.amount,
      premium,
      validUntil,
      nonce: opts.nonce ?? this.nonces.next(),
      epoch: s.epoch,
    });
  }

  /** The gateway's static checks (auction.Static), so a bad quote never leaves the process. Throws ValidationError. */
  checkQuote(rfq: RFQ, q: Quote): void {
    if (q.maker !== this.maker) {
      throw new ValidationError(`quote maker ${q.maker} is not the logged-in maker ${this.maker}`);
    }
    if (q.signer !== this.signer) {
      throw new ValidationError(`quote signer ${q.signer} is not this client's key ${this.signer}`);
    }
    if (
      q.taker !== rfq.taker ||
      q.side !== rfq.side ||
      q.amount !== rfq.amount ||
      !seriesEquals(q.series, rfq.series)
    ) {
      throw new ValidationError("taker, side, amount and series must equal the request");
    }
    if (q.minFill !== rfq.amount) throw new ValidationError("minFill must equal the amount");
    if (q.premium <= 0n) throw new ValidationError("premium must be positive");
    const now = BigInt(Math.floor(this.clock() / 1000));
    const max = now + BigInt(MAX_QUOTE_LIFE);
    if (q.validUntil < rfq.minValidUntil || q.validUntil > max) {
      throw new ValidationError(`validUntil ${q.validUntil} outside [${rfq.minValidUntil}, ${max}]`);
    }
  }
}
