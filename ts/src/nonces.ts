/**
 * Quote nonces. The settler keeps a Permit2-style bitmap per maker: slot = nonce >> 8, bit = nonce & 0xff. A fully
 * filled quote burns its bit; `invalidateNonces(maker, slot, mask)` burns bits in bulk. The first fill also binds the
 * nonce to that quote's digest, so quotes sharing a nonce are mutually exclusive. Give every quote its own nonce unless
 * you mean them to exclude each other. Mirrors hedgehood_maker.nonces.
 */

import { webcrypto } from "node:crypto";

/** {slot, bit} of a nonce. */
export function split(nonce: bigint): { slot: bigint; bit: number } {
  return { slot: nonce >> 8n, bit: Number(nonce & 0xffn) };
}

/** slot -> mask to pass to invalidateNonces for these nonces (one transaction per slot). */
export function masks(nonces: Iterable<bigint>): Map<bigint, bigint> {
  const out = new Map<bigint, bigint>();
  for (const n of nonces) {
    const { slot, bit } = split(n);
    out.set(slot, (out.get(slot) ?? 0n) | (1n << BigInt(bit)));
  }
  return out;
}

function random64(): bigint {
  const b = new BigUint64Array(1);
  webcrypto.getRandomValues(b);
  return b[0]!;
}

/**
 * Hands out fresh nonces: a random 64-bit start slot (so two processes for one maker do not collide), then every bit
 * of the slot, then the next slot. Remembers the last `keep` nonces it issued for `invalidateAll`.
 */
export class NonceManager {
  private slot: bigint;
  private bit = 0;
  private readonly _issued: bigint[] = [];

  constructor(
    startSlot?: bigint | number,
    private readonly keep = 4096,
  ) {
    this.slot = startSlot === undefined ? random64() : BigInt(startSlot);
  }

  next(): bigint {
    if (this.bit > 0xff) {
      this.slot += 1n;
      this.bit = 0;
    }
    const nonce = (this.slot << 8n) | BigInt(this.bit);
    this.bit += 1;
    this._issued.push(nonce);
    if (this._issued.length > this.keep) this._issued.splice(0, this._issued.length - this.keep);
    return nonce;
  }

  get issued(): bigint[] {
    return [...this._issued];
  }

  /** slot -> mask burning every nonce issued so far (prefer increaseEpoch to void everything at once). */
  invalidateAll(): Map<bigint, bigint> {
    return masks(this._issued);
  }
}
