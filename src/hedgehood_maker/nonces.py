"""Quote nonces. The settler keeps a Permit2-style bitmap per maker: slot = nonce >> 8, bit = nonce & 0xff. A fully
filled quote burns its bit; `invalidateNonces(maker, slot, mask)` burns bits in bulk. The first fill also binds the
nonce to that quote's digest, so quotes sharing a nonce are mutually exclusive. Give every quote its own nonce unless
you mean them to exclude each other.
"""

from __future__ import annotations

import secrets
import threading
from collections.abc import Iterable


def split(nonce: int) -> tuple[int, int]:
    """(slot, bit) of a nonce."""
    return nonce >> 8, nonce & 0xFF


def masks(nonces: Iterable[int]) -> dict[int, int]:
    """{slot: mask} to pass to invalidateNonces for these nonces (one transaction per slot)."""
    out: dict[int, int] = {}
    for n in nonces:
        slot, bit = split(int(n))
        out[slot] = out.get(slot, 0) | (1 << bit)
    return out


class NonceManager:
    """Hands out fresh nonces: a random 64-bit start slot (so two processes for one maker do not collide), then every
    bit of the slot, then the next slot. Thread-safe. Remembers the nonces it issued for `invalidate_all`."""

    def __init__(self, start_slot: int | None = None, keep: int = 4096):
        self._slot = secrets.randbits(64) if start_slot is None else int(start_slot)
        self._bit = 0
        self._lock = threading.Lock()
        self._issued: list[int] = []
        self._keep = keep

    def next(self) -> int:
        with self._lock:
            if self._bit > 0xFF:
                self._slot += 1
                self._bit = 0
            nonce = (self._slot << 8) | self._bit
            self._bit += 1
            self._issued.append(nonce)
            if len(self._issued) > self._keep:
                del self._issued[: len(self._issued) - self._keep]
            return nonce

    @property
    def issued(self) -> list[int]:
        with self._lock:
            return list(self._issued)

    def invalidate_all(self) -> dict[int, int]:
        """{slot: mask} burning every nonce issued so far (prefer increaseEpoch to void everything at once)."""
        return masks(self.issued)
