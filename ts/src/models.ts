/**
 * Typed models of the settler's structs and the gateway's WebSocket frames, with validation. Mirrors
 * hedgehood_maker.models.
 *
 * JSON on the wire (as the gateway reads and writes it): uint256 fields (strike, amount, minFill, premium, nonce,
 * epoch) are decimal strings; uint64 / uint8 fields (expiry, validUntil, side, chainId) are JSON numbers; addresses
 * are 0x hex in any case. The gateway also accepts bare numbers for uint256 fields, but rejects strings for the uint64
 * ones.
 *
 * In memory every on-chain integer is a bigint. Inputs accept a bigint, a safe-integer number or a decimal string (no
 * floats, no booleans, no hex), like the Python SDK.
 */

import { type Address, getAddress, type Hex } from "viem";
import { MAKER_BUYS, MAKER_SELLS, type Side, UINT64_MAX, UINT256_MAX, ZERO_ADDRESS } from "./constants.js";

/** A value does not fit the settler's types or the gateway's rules. */
export class ValidationError extends Error {
  override name = "ValidationError";
}

/** An unsigned integer as accepted on input. */
export type Uint = bigint | number | string;

const ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const DECIMAL_RE = /^[0-9]+$/;

function show(value: unknown): string {
  if (typeof value === "bigint") return `${value}n`;
  try {
    return JSON.stringify(value) ?? String(value);
  } catch {
    return String(value);
  }
}

/** A checksummed address from 0x hex in any case (like the gateway and the Python SDK, the case is not checked). */
export function address(value: unknown, name = "address"): Address {
  if (typeof value !== "string" || !ADDRESS_RE.test(value)) {
    throw new ValidationError(`${name}: not an address: ${show(value)}`);
  }
  return getAddress(value.toLowerCase());
}

/** An unsigned integer of `bits` bits from a bigint, a safe-integer number or a decimal string. */
export function uint(value: unknown, name = "value", bits = 256): bigint {
  let v: bigint;
  if (typeof value === "bigint") {
    v = value;
  } else if (typeof value === "number") {
    if (!Number.isSafeInteger(value)) {
      throw new ValidationError(`${name}: not an unsigned integer (or not a safe integer): ${show(value)}`);
    }
    v = BigInt(value);
  } else if (typeof value === "string" && DECIMAL_RE.test(value)) {
    v = BigInt(value);
  } else {
    throw new ValidationError(`${name}: not an unsigned integer: ${show(value)}`);
  }
  const max = bits === 256 ? UINT256_MAX : bits === 64 ? UINT64_MAX : (1n << BigInt(bits)) - 1n;
  if (v < 0n || v > max) throw new ValidationError(`${name}: out of uint${bits} range: ${v}`);
  return v;
}

/** An unsigned integer that must also fit a JS number exactly (chain ids, bps, unix ms). */
export function safeUint(value: unknown, name = "value", bits = 64): number {
  const v = uint(value, name, bits);
  if (v > BigInt(Number.MAX_SAFE_INTEGER)) throw new ValidationError(`${name}: too large for a JS number: ${v}`);
  return Number(v);
}

/** A uint64 as a JSON number (the gateway rejects strings there); refuses values a JS number cannot carry exactly. */
function jsonNumber(v: bigint, name: string): number {
  if (v > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new ValidationError(`${name}: ${v} cannot travel as an exact JSON number from JavaScript`);
  }
  return Number(v);
}

function bool(value: unknown, name: string): boolean {
  if (typeof value !== "boolean") throw new ValidationError(`${name}: not a bool: ${show(value)}`);
  return value;
}

function side(value: unknown): Side {
  const v = uint(value, "side", 8);
  if (v !== BigInt(MAKER_BUYS) && v !== BigInt(MAKER_SELLS)) {
    throw new ValidationError(`side must be 0 (MakerBuys) or 1 (MakerSells), got ${v}`);
  }
  return Number(v) as Side;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function str(value: unknown): string {
  return value === undefined || value === null ? "" : String(value);
}

// ---------------------------------------------------------------- Series

/** OptionsCore.SeriesParams. Strike is quote raw (USDG 1e6) per whole underlying token. */
export interface Series {
  readonly underlying: Address;
  readonly quote: Address;
  readonly oracle: Address;
  /** Unix seconds, the 16:00 New York close of a listed weekday. */
  readonly expiry: bigint;
  readonly isPut: boolean;
  readonly strike: bigint;
  readonly physical: boolean;
}

export interface SeriesInput {
  underlying: string;
  quote: string;
  oracle: string;
  expiry: Uint;
  isPut: boolean;
  strike: Uint;
  physical?: boolean;
}

export interface SeriesJson {
  underlying: Address;
  quote: Address;
  oracle: Address;
  expiry: number;
  isPut: boolean;
  strike: string;
  physical: boolean;
}

/** A validated, frozen Series. */
export function makeSeries(s: SeriesInput): Series {
  if (!isRecord(s)) throw new ValidationError("series: not an object");
  return Object.freeze({
    underlying: address(s.underlying, "series.underlying"),
    quote: address(s.quote, "series.quote"),
    oracle: address(s.oracle, "series.oracle"),
    expiry: uint(s.expiry, "series.expiry", 64),
    isPut: bool(s.isPut, "series.isPut"),
    strike: uint(s.strike, "series.strike"),
    physical: bool(s.physical ?? false, "series.physical"),
  });
}

export function seriesEquals(a: Series, b: Series): boolean {
  return (
    a.underlying === b.underlying &&
    a.quote === b.quote &&
    a.oracle === b.oracle &&
    a.expiry === b.expiry &&
    a.isPut === b.isPut &&
    a.strike === b.strike &&
    a.physical === b.physical
  );
}

export function seriesToJson(s: Series): SeriesJson {
  return {
    underlying: s.underlying,
    quote: s.quote,
    oracle: s.oracle,
    expiry: jsonNumber(s.expiry, "series.expiry"),
    isPut: s.isPut,
    strike: s.strike.toString(),
    physical: s.physical,
  };
}

export function seriesFromJson(d: unknown): Series {
  if (!isRecord(d)) throw new ValidationError("series: not an object");
  return makeSeries({
    underlying: d.underlying as string,
    quote: d.quote as string,
    oracle: d.oracle as string,
    expiry: d.expiry as Uint,
    isPut: d.isPut as boolean,
    strike: d.strike as Uint,
    physical: (d.physical ?? false) as boolean,
  });
}

// ---------------------------------------------------------------- Quote

/** IRfqSettler.Quote: a firm quote the maker (or its delegated signer) signs under EIP-712. */
export interface Quote {
  readonly maker: Address;
  readonly signer: Address;
  readonly taker: Address;
  /** Zero = the maker's settler balance; else that wallet, by allowance. */
  readonly externalAccount: Address;
  readonly series: Series;
  readonly side: Side;
  /** Underlying raw (1e18 = one token). */
  readonly amount: bigint;
  readonly minFill: bigint;
  /** Total quote raw for `amount`, fee excluded. */
  readonly premium: bigint;
  /** Unix seconds. */
  readonly validUntil: bigint;
  readonly nonce: bigint;
  readonly epoch: bigint;
}

export interface QuoteInput {
  maker: string;
  signer: string;
  taker: string;
  externalAccount?: string;
  series: Series | SeriesInput;
  side: number | bigint | string;
  amount: Uint;
  minFill: Uint;
  premium: Uint;
  validUntil: Uint;
  nonce: Uint;
  epoch?: Uint;
}

export interface QuoteJson {
  maker: Address;
  signer: Address;
  taker: Address;
  externalAccount: Address;
  series: SeriesJson;
  side: number;
  amount: string;
  minFill: string;
  premium: string;
  validUntil: number;
  nonce: string;
  epoch: string;
}

/** A validated, frozen Quote. */
export function makeQuote(q: QuoteInput): Quote {
  if (!isRecord(q)) throw new ValidationError("quote: not an object");
  return Object.freeze({
    maker: address(q.maker, "maker"),
    signer: address(q.signer, "signer"),
    taker: address(q.taker, "taker"),
    externalAccount: address(q.externalAccount ?? ZERO_ADDRESS, "externalAccount"),
    series: makeSeries(q.series as SeriesInput),
    side: side(q.side),
    amount: uint(q.amount, "amount"),
    minFill: uint(q.minFill, "minFill"),
    premium: uint(q.premium, "premium"),
    validUntil: uint(q.validUntil, "validUntil", 64),
    nonce: uint(q.nonce, "nonce"),
    epoch: uint(q.epoch ?? 0n, "epoch"),
  });
}

/** A copy of `q` with some fields changed (validated again). */
export function replaceQuote(q: Quote, changes: Partial<QuoteInput>): Quote {
  return makeQuote({ ...q, ...changes });
}

export function quoteEquals(a: Quote, b: Quote): boolean {
  return (
    a.maker === b.maker &&
    a.signer === b.signer &&
    a.taker === b.taker &&
    a.externalAccount === b.externalAccount &&
    seriesEquals(a.series, b.series) &&
    a.side === b.side &&
    a.amount === b.amount &&
    a.minFill === b.minFill &&
    a.premium === b.premium &&
    a.validUntil === b.validUntil &&
    a.nonce === b.nonce &&
    a.epoch === b.epoch
  );
}

export function quoteToJson(q: Quote): QuoteJson {
  return {
    maker: q.maker,
    signer: q.signer,
    taker: q.taker,
    externalAccount: q.externalAccount,
    series: seriesToJson(q.series),
    side: q.side,
    amount: q.amount.toString(),
    minFill: q.minFill.toString(),
    premium: q.premium.toString(),
    validUntil: jsonNumber(q.validUntil, "validUntil"),
    nonce: q.nonce.toString(),
    epoch: q.epoch.toString(),
  };
}

export function quoteFromJson(d: unknown): Quote {
  if (!isRecord(d)) throw new ValidationError("quote: not an object");
  return makeQuote({
    maker: d.maker as string,
    signer: d.signer as string,
    taker: d.taker as string,
    externalAccount: (d.externalAccount || ZERO_ADDRESS) as string,
    series: seriesFromJson(d.series),
    side: d.side as number,
    amount: d.amount as Uint,
    minFill: d.minFill as Uint,
    premium: d.premium as Uint,
    validUntil: d.validUntil as Uint,
    nonce: d.nonce as Uint,
    epoch: (d.epoch ?? 0) as Uint,
  });
}

// ---------------------------------------------------------------- server -> maker frames

type Raw = Readonly<Record<string, unknown>>;

export interface Challenge {
  readonly type: "challenge";
  readonly raw: Raw;
  readonly nonce: string;
  readonly chainId: number;
}

export interface Ready {
  readonly type: "ready";
  readonly raw: Raw;
  readonly maker: Address;
  readonly settler: Address;
  readonly feeBps: number;
  readonly notionalFeeBps: number;
  readonly epoch: bigint;
}

/** A taker's request as every maker sees it. Answer before `respondBy` (unix ms) with validUntil >= `minValidUntil`. */
export interface RFQ {
  readonly type: "rfq";
  readonly raw: Raw;
  readonly rfqId: string;
  readonly chainId: number;
  readonly settler: Address;
  readonly taker: Address;
  readonly side: Side;
  readonly series: Series;
  readonly amount: bigint;
  /** Unix milliseconds. */
  readonly respondBy: number;
  /** Unix seconds. */
  readonly minValidUntil: bigint;
}

export interface Won {
  readonly type: "won";
  readonly raw: Raw;
  readonly rfqId: string;
  /** Lower-case 0x hex. */
  readonly digest: string;
}

export interface Lost {
  readonly type: "lost";
  readonly raw: Raw;
  readonly rfqId: string;
}

export interface Fill {
  readonly type: "fill";
  readonly raw: Raw;
  /** Lower-case 0x hex. */
  readonly digest: string;
  readonly txHash: string;
  readonly amount: bigint;
  readonly rfqId: string;
}

export interface Rejected {
  readonly type: "rejected";
  readonly raw: Raw;
  readonly rfqId: string;
  readonly code: string;
  readonly reason: string;
}

export interface ErrorFrame {
  readonly type: "error";
  readonly raw: Raw;
  readonly code: string;
  readonly reason: string;
  readonly rfqId: string;
}

export interface Paused {
  readonly type: "paused";
  readonly raw: Raw;
}

export interface Resumed {
  readonly type: "resumed";
  readonly raw: Raw;
}

/** A frame type this SDK does not know (`raw.type` holds what the gateway sent). */
export interface Unknown {
  readonly type: "unknown";
  readonly raw: Raw;
}

export type ServerFrame =
  | Challenge
  | Ready
  | RFQ
  | Won
  | Lost
  | Fill
  | Rejected
  | ErrorFrame
  | Paused
  | Resumed
  | Unknown;

/** A server frame as its model. Throws ValidationError on a malformed known frame. */
export function parseFrame(d: unknown): ServerFrame {
  if (!isRecord(d)) throw new ValidationError("frame: not an object");
  const raw: Raw = Object.freeze({ ...d });
  const rfqId = str(d.rfqId);
  switch (d.type) {
    case "challenge":
      return { type: "challenge", raw, nonce: str(d.nonce), chainId: safeUint(d.chainId, "chainId", 64) };
    case "ready":
      return {
        type: "ready",
        raw,
        maker: address(d.maker, "maker"),
        settler: address(d.settler, "settler"),
        feeBps: safeUint(d.feeBps ?? 0, "feeBps", 256),
        notionalFeeBps: safeUint(d.notionalFeeBps ?? 0, "notionalFeeBps", 256),
        epoch: uint(d.epoch ?? 0, "epoch"),
      };
    case "rfq":
      if (!rfqId) throw new ValidationError("rfq: missing rfqId");
      return {
        type: "rfq",
        raw,
        rfqId,
        chainId: safeUint(d.chainId, "chainId", 64),
        settler: address(d.settler, "settler"),
        taker: address(d.taker, "taker"),
        side: side(d.side),
        series: seriesFromJson(d.series),
        amount: uint(d.amount, "amount"),
        respondBy: safeUint(d.respondBy, "respondBy", 64),
        minValidUntil: uint(d.minValidUntil, "minValidUntil", 64),
      };
    case "won":
      return { type: "won", raw, rfqId, digest: str(d.digest).toLowerCase() };
    case "lost":
      return { type: "lost", raw, rfqId };
    case "fill":
      return {
        type: "fill",
        raw,
        digest: str(d.digest).toLowerCase(),
        txHash: str(d.txHash),
        amount: uint(d.amount ?? 0, "amount"),
        rfqId,
      };
    case "rejected":
      return { type: "rejected", raw, rfqId, code: str(d.code), reason: str(d.reason) };
    case "error":
      return { type: "error", raw, code: str(d.code), reason: str(d.reason), rfqId };
    case "paused":
      return { type: "paused", raw };
    case "resumed":
      return { type: "resumed", raw };
    default:
      return { type: "unknown", raw };
  }
}

// ---------------------------------------------------------------- maker -> server frames

/** A pricer's refusal; `reason` is a short code the gateway records (e.g. "no_price", "no_inventory"). */
export class Decline {
  constructor(readonly reason: string = "no_price") {}
}

export interface LoginFrame {
  type: "login";
  maker: Address;
  signer: Address;
  signature: Hex;
}

export interface QuoteFrame {
  type: "quote";
  rfqId: string;
  quote: QuoteJson;
  signature: Hex;
}

export interface DeclineFrame {
  type: "decline";
  rfqId: string;
  reason: string;
}

export interface ControlFrame {
  type: "pause" | "resume";
}

export type MakerFrame = LoginFrame | QuoteFrame | DeclineFrame | ControlFrame;

export function loginFrame(maker: string, signer: string, signature: Hex): LoginFrame {
  return { type: "login", maker: address(maker, "maker"), signer: address(signer, "signer"), signature };
}

export function quoteFrame(rfqId: string, q: Quote, signature: Hex): QuoteFrame {
  return { type: "quote", rfqId, quote: quoteToJson(q), signature };
}

export function declineFrame(rfqId: string, reason: string): DeclineFrame {
  return { type: "decline", rfqId, reason: reason || "declined" };
}
