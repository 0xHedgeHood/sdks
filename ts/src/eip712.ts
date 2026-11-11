/**
 * EIP-712 of the RfqSettler quote, byte for byte what `RfqSettler.quoteDigest` computes, plus the gateway login.
 * Mirrors hedgehood_maker.eip712.
 *
 * Domain: {name "Hedgehood RFQ Settler", version "2", chainId, verifyingContract = settler proxy}.
 *
 *     Quote(address maker,address signer,address taker,address externalAccount,SeriesParams series,uint8 side,
 *           uint256 amount,uint256 minFill,uint256 premium,uint64 validUntil,uint256 nonce,uint256 epoch)
 *     SeriesParams(address underlying,address quote,address oracle,uint64 expiry,bool isPut,uint256 strike,bool physical)
 *
 * The digest equals the standard EIP-712 digest, so any wallet's signTypedData of `typedData(...)` works too.
 */

import {
  type Address,
  concat,
  encodeAbiParameters,
  type Hex,
  hexToBigInt,
  hexToBytes,
  keccak256,
  type LocalAccount,
  recoverAddress,
  recoverMessageAddress,
  stringToHex,
  toHex,
} from "viem";
import { privateKeyToAccount, sign } from "viem/accounts";
import { DOMAIN_NAME, DOMAIN_VERSION, LOGIN_PREFIX } from "./constants.js";
import { address, type Quote, type Series, ValidationError } from "./models.js";

export const SERIES_FIELDS = ["underlying", "quote", "oracle", "expiry", "isPut", "strike", "physical"] as const;
export const SERIES_TYPES = ["address", "address", "address", "uint64", "bool", "uint256", "bool"] as const;
export const SERIES_TYPE_STRING = `SeriesParams(${SERIES_FIELDS.map((f, i) => `${SERIES_TYPES[i]} ${f}`).join(",")})`;

export const QUOTE_FIELDS = [
  "maker",
  "signer",
  "taker",
  "externalAccount",
  "series",
  "side",
  "amount",
  "minFill",
  "premium",
  "validUntil",
  "nonce",
  "epoch",
] as const;
export const QUOTE_TYPES = [
  "address",
  "address",
  "address",
  "address",
  "SeriesParams",
  "uint8",
  "uint256",
  "uint256",
  "uint256",
  "uint64",
  "uint256",
  "uint256",
] as const;
export const QUOTE_TYPE_STRING = `Quote(${QUOTE_FIELDS.map((f, i) => `${QUOTE_TYPES[i]} ${f}`).join(",")})${SERIES_TYPE_STRING}`;

export const DOMAIN_TYPE_STRING = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)";

export const SERIES_TYPEHASH = keccak256(stringToHex(SERIES_TYPE_STRING));
export const QUOTE_TYPEHASH = keccak256(stringToHex(QUOTE_TYPE_STRING));
export const DOMAIN_TYPEHASH = keccak256(stringToHex(DOMAIN_TYPE_STRING));

/** secp256k1 group order; signatures with s > N/2 are refused (OpenZeppelin ECDSA, the gateway's Recover). */
const N = 0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141n;

/** A signer: a 0x private key or a viem LocalAccount (e.g. privateKeyToAccount). */
export type SignerKey = Hex | LocalAccount;

export function toAccount(key: SignerKey): LocalAccount {
  if (typeof key === "string") {
    if (!/^0x[0-9a-fA-F]{64}$/.test(key)) throw new ValidationError("private key must be 32 bytes of 0x hex");
    return privateKeyToAccount(key);
  }
  if (typeof key !== "object" || key === null || key.type !== "local" || typeof key.sign !== "function") {
    throw new ValidationError("signer must be a 0x private key or a viem LocalAccount with sign()");
  }
  return key;
}

export type SeriesTuple = readonly [Address, Address, Address, bigint, boolean, bigint, boolean];
export type QuoteTuple = readonly [
  Address,
  Address,
  Address,
  Address,
  SeriesTuple,
  number,
  bigint,
  bigint,
  bigint,
  bigint,
  bigint,
  bigint,
];

export function seriesValues(s: Series): SeriesTuple {
  return [s.underlying, s.quote, s.oracle, s.expiry, s.isPut, s.strike, s.physical];
}

/** The quote as the settler's ABI tuple (series nested). */
export function quoteValues(q: Quote): QuoteTuple {
  return [
    q.maker,
    q.signer,
    q.taker,
    q.externalAccount,
    seriesValues(q.series),
    q.side,
    q.amount,
    q.minFill,
    q.premium,
    q.validUntil,
    q.nonce,
    q.epoch,
  ];
}

const SERIES_PARAMS = SERIES_TYPES.map((type) => ({ type }));

/** OptionsCore.idOf: uint256(keccak256(abi.encode(params))), also the ERC-6909 id of the long. */
export function seriesId(s: Series): bigint {
  return hexToBigInt(keccak256(encodeAbiParameters(SERIES_PARAMS, seriesValues(s) as unknown as unknown[])));
}

export function domainSeparator(chainId: number | bigint, settler: string): Hex {
  return keccak256(
    encodeAbiParameters(
      [{ type: "bytes32" }, { type: "bytes32" }, { type: "bytes32" }, { type: "uint256" }, { type: "address" }],
      [
        DOMAIN_TYPEHASH,
        keccak256(stringToHex(DOMAIN_NAME)),
        keccak256(stringToHex(DOMAIN_VERSION)),
        BigInt(chainId),
        address(settler, "settler"),
      ],
    ),
  );
}

export function seriesHash(s: Series): Hex {
  return keccak256(
    encodeAbiParameters([{ type: "bytes32" }, ...SERIES_PARAMS], [SERIES_TYPEHASH, ...seriesValues(s)] as unknown[]),
  );
}

export function quoteStructHash(q: Quote): Hex {
  const types = [{ type: "bytes32" }, ...QUOTE_TYPES.map((t) => ({ type: t === "SeriesParams" ? "bytes32" : t }))];
  const values: unknown[] = [QUOTE_TYPEHASH, ...quoteValues(q)];
  values[1 + QUOTE_FIELDS.indexOf("series")] = seriesHash(q.series);
  return keccak256(encodeAbiParameters(types, values));
}

/** RfqSettler.quoteDigest(q) as 0x hex (lower case, as the gateway's won / fill frames carry it). */
export function quoteDigest(q: Quote, chainId: number | bigint, settler: string): Hex {
  return keccak256(concat(["0x1901", domainSeparator(chainId, settler), quoteStructHash(q)]));
}

/** The EIP712Domain type, for wallets / RPCs that want it inside `types` (viem and ethers derive it themselves). */
export const EIP712_DOMAIN_TYPE = [
  { name: "name", type: "string" },
  { name: "version", type: "string" },
  { name: "chainId", type: "uint256" },
  { name: "verifyingContract", type: "address" },
] as const;

type Zip<F extends readonly string[], T extends readonly string[]> = {
  readonly [K in keyof F]: { readonly name: F[K]; readonly type: K extends keyof T ? T[K] : never };
};

function zip<const F extends readonly string[], const T extends readonly string[]>(f: F, t: T): Zip<F, T> {
  return f.map((name, i) => ({ name, type: t[i] })) as unknown as Zip<F, T>;
}

/** EIP-712 types of the quote (the domain type left out, as viem and ethers want it). */
export const QUOTE_EIP712_TYPES = {
  Quote: zip(QUOTE_FIELDS, QUOTE_TYPES),
  SeriesParams: zip(SERIES_FIELDS, SERIES_TYPES),
} as const;

/** The quote as EIP-712 typed data for a wallet (viem `signTypedData` / `hashTypedData`, ethers `signTypedData`). */
export function typedData(q: Quote, chainId: number | bigint, settler: string) {
  return {
    domain: {
      name: DOMAIN_NAME,
      version: DOMAIN_VERSION,
      chainId: BigInt(chainId),
      verifyingContract: address(settler, "settler"),
    },
    types: QUOTE_EIP712_TYPES,
    primaryType: "Quote" as const,
    message: {
      maker: q.maker,
      signer: q.signer,
      taker: q.taker,
      externalAccount: q.externalAccount,
      series: {
        underlying: q.series.underlying,
        quote: q.series.quote,
        oracle: q.series.oracle,
        expiry: q.series.expiry,
        isPut: q.series.isPut,
        strike: q.series.strike,
        physical: q.series.physical,
      },
      side: q.side,
      amount: q.amount,
      minFill: q.minFill,
      premium: q.premium,
      validUntil: q.validUntil,
      nonce: q.nonce,
      epoch: q.epoch,
    },
  };
}

function signerSign(key: SignerKey, hash: Hex): Promise<Hex> {
  if (typeof key === "string") return sign({ hash, privateKey: key, to: "hex" });
  return toAccount(key).sign!({ hash });
}

/** 65-byte r || s || v (v = 27 / 28, low s) signature of a 32-byte digest. */
export async function signDigest(digest: Hex, key: SignerKey): Promise<Hex> {
  const sig = await signerSign(key, digest);
  checkSignature(sig);
  return sig.toLowerCase() as Hex;
}

/** The signature the settler checks (SignatureChecker: ECDSA for an EOA signer). */
export function signQuote(q: Quote, chainId: number | bigint, settler: string, key: SignerKey): Promise<Hex> {
  return signDigest(quoteDigest(q, chainId, settler), key);
}

/** Validates a 65-byte r || s || v signature the way the settler and the gateway do; returns it with v = 27 / 28. */
function checkSignature(signature: Hex | Uint8Array): Hex {
  const sig = typeof signature === "string" ? hexToBytes(signature) : signature;
  if (sig.length !== 65) throw new ValidationError("signature must be 65 bytes");
  const r = hexToBigInt(toHex(sig.slice(0, 32)));
  const s = hexToBigInt(toHex(sig.slice(32, 64)));
  let v = sig[64]!;
  if (v >= 27) v -= 27;
  if ((v !== 0 && v !== 1) || !(r > 0n && r < N) || !(s > 0n && s <= N / 2n)) {
    throw new ValidationError("bad r, s or v");
  }
  const out = new Uint8Array(sig);
  out[64] = v + 27;
  return toHex(out);
}

/** The signer of a 65-byte r || s || v signature (v 27/28 or 0/1); high s refused as on chain and in the gateway. */
export async function recoverDigest(digest: Hex, signature: Hex | Uint8Array): Promise<Address> {
  if (typeof signature === "string" && !/^0x([0-9a-fA-F]{2})*$/.test(signature)) {
    throw new ValidationError("signature is not 0x hex");
  }
  return address(await recoverAddress({ hash: digest, signature: checkSignature(signature) }));
}

export function recoverQuoteSigner(
  q: Quote,
  chainId: number | bigint,
  settler: string,
  signature: Hex | Uint8Array,
): Promise<Address> {
  return recoverDigest(quoteDigest(q, chainId, settler), signature);
}

// ---------------------------------------------------------------- gateway login (EIP-191)

/** What the signer personal_signs to log in: hedgehood-rfq-login:<chainId>:<maker lower-case>:<nonce>. */
export function loginMessage(chainId: number | bigint, maker: string, nonce: string): string {
  return `${LOGIN_PREFIX}:${BigInt(chainId)}:${address(maker, "maker").toLowerCase()}:${nonce}`;
}

export async function signLogin(chainId: number | bigint, maker: string, nonce: string, key: SignerKey): Promise<Hex> {
  const sig = await toAccount(key).signMessage({ message: loginMessage(chainId, maker, nonce) });
  return sig.toLowerCase() as Hex;
}

export async function recoverLogin(chainId: number | bigint, maker: string, nonce: string, signature: Hex) {
  return address(
    await recoverMessageAddress({ message: loginMessage(chainId, maker, nonce), signature: checkSignature(signature) }),
  );
}
