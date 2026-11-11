package maker

import (
	"crypto/rand"
	"encoding/binary"
	"math/big"
	"sync"
)

// Quote nonces. The settler keeps a Permit2-style bitmap per maker: slot = nonce >> 8, bit = nonce & 0xff. A fully
// filled quote burns its bit; invalidateNonces(maker, slot, mask) burns bits in bulk. The first fill also binds the
// nonce to that quote's digest, so quotes sharing a nonce are mutually exclusive. Give every quote its own nonce
// unless you mean them to exclude each other.

// SplitNonce is (slot, bit) of a nonce.
func SplitNonce(nonce *big.Int) (slot *big.Int, bit uint) {
	n := nz(nonce)
	return new(big.Int).Rsh(n, 8), uint(new(big.Int).And(n, big.NewInt(0xff)).Uint64())
}

// NonceMasks is {slot: mask} to pass to invalidateNonces for these nonces (one transaction per slot). Slots are
// decimal strings.
func NonceMasks(nonces []*big.Int) map[string]*big.Int {
	out := map[string]*big.Int{}
	for _, n := range nonces {
		slot, bit := SplitNonce(n)
		k := slot.String()
		if out[k] == nil {
			out[k] = new(big.Int)
		}
		out[k].SetBit(out[k], int(bit), 1)
	}
	return out
}

// NonceManager hands out fresh nonces: a random 64-bit start slot (so two processes for one maker do not collide),
// then every bit of the slot, then the next slot. Safe for concurrent use. It remembers the last `Keep` nonces it
// issued for InvalidateAll.
type NonceManager struct {
	mu     sync.Mutex
	slot   *big.Int
	bit    uint
	issued []*big.Int
	keep   int
}

// NewNonceManager starts at a random 64-bit slot.
func NewNonceManager() *NonceManager {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return NewNonceManagerAt(new(big.Int).SetUint64(binary.BigEndian.Uint64(b[:])))
}

// NewNonceManagerAt starts at a chosen slot (tests, or a slot range you reserve per process).
func NewNonceManagerAt(slot *big.Int) *NonceManager {
	return &NonceManager{slot: new(big.Int).Set(nz(slot)), keep: 4096}
}

// Next is a nonce never handed out before by this manager.
func (m *NonceManager) Next() *big.Int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bit > 0xff {
		m.slot.Add(m.slot, big.NewInt(1))
		m.bit = 0
	}
	n := new(big.Int).Lsh(m.slot, 8)
	n.Or(n, big.NewInt(int64(m.bit)))
	m.bit++
	m.issued = append(m.issued, n)
	if len(m.issued) > m.keep {
		m.issued = append([]*big.Int(nil), m.issued[len(m.issued)-m.keep:]...)
	}
	return new(big.Int).Set(n)
}

// Issued lists the remembered nonces, oldest first.
func (m *NonceManager) Issued() []*big.Int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*big.Int, len(m.issued))
	for i, n := range m.issued {
		out[i] = new(big.Int).Set(n)
	}
	return out
}

// InvalidateAll is {slot: mask} burning every remembered nonce (prefer increaseEpoch to void everything at once).
func (m *NonceManager) InvalidateAll() map[string]*big.Int { return NonceMasks(m.Issued()) }
