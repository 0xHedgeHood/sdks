package maker

import (
	"errors"
	"math/big"
	"strings"
)

// The settler's money math (RfqSettler.previewFill) in integers, rounding exactly as the contract does.
//
// The maker pays the protocol fee: fee = min(premium * feeBps, notional * notionalFeeBps) / 10 000 with notional =
// amount * strike / unit (unit = one whole underlying token, 1e18), every step rounded down; notionalFeeBps = 0 means
// no cap. MakerBuys: the maker pays premium + fee. MakerSells: the maker receives premium - fee. The relayer's gas fee
// (for a gasless taker) is the taker's and never changes the maker's side.

var (
	// Share is one whole stock token (18 decimals).
	Share = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	// USDG is one USDG (6 decimals).
	USDG = big.NewInt(1_000_000)
	bps  = big.NewInt(BPS)
)

func unitOr(unit *big.Int) *big.Int {
	if unit == nil || unit.Sign() == 0 {
		return Share
	}
	return unit
}

// Notional is amount * strike / unit in quote raw: the base of the notional fee cap (unit nil = 1e18).
func Notional(amount, strike, unit *big.Int) *big.Int {
	n := new(big.Int).Mul(nz(amount), nz(strike))
	return n.Quo(n, unitOr(unit))
}

// Fee is what the settler charges the maker for a fill of `amount` at `premium` (unit nil = 1e18).
func Fee(premium, amount, strike *big.Int, feeBps, notionalFeeBps uint64, unit *big.Int) *big.Int {
	f := new(big.Int).Mul(nz(premium), new(big.Int).SetUint64(feeBps))
	f.Quo(f, bps)
	if notionalFeeBps == 0 || f.Sign() == 0 {
		return f
	}
	c := Notional(amount, strike, unit)
	c.Mul(c, new(big.Int).SetUint64(notionalFeeBps))
	c.Quo(c, bps)
	if c.Cmp(f) < 0 {
		return c
	}
	return f
}

// PreviewFill is (premium, fee) for filling `amount` of q: the premium pro rata, rounded down when the maker pays and
// up when it receives.
func PreviewFill(q Quote, amount *big.Int, feeBps, notionalFeeBps uint64, unit *big.Int) (premium, fee *big.Int, err error) {
	if amount == nil || amount.Sign() <= 0 || amount.Cmp(nz(q.Amount)) > 0 {
		return nil, nil, errors.New("amount must be in (0, quote.amount]")
	}
	num := new(big.Int).Mul(nz(q.Premium), amount)
	premium = new(big.Int)
	if q.Side == MakerBuys {
		premium.Quo(num, q.Amount)
	} else {
		premium.Add(num, new(big.Int).Sub(q.Amount, big.NewInt(1)))
		premium.Quo(premium, q.Amount)
	}
	return premium, Fee(premium, amount, q.Series.Strike, feeBps, notionalFeeBps, unit), nil
}

// MakerCash is the maker's signed quote-token flow for a whole fill: -(premium + fee) when it buys, premium - fee
// when it sells.
func MakerCash(side uint8, premium, amount, strike *big.Int, feeBps, notionalFeeBps uint64, unit *big.Int) *big.Int {
	fee := Fee(premium, amount, strike, feeBps, notionalFeeBps, unit)
	if side == MakerBuys {
		return new(big.Int).Neg(new(big.Int).Add(nz(premium), fee))
	}
	return new(big.Int).Sub(nz(premium), fee)
}

// PremiumForNet is the premium to quote so the fee lands on your side of `net` (quote raw, >= 0):
//
//   - MakerBuys: the largest premium whose premium + fee <= net (what you are willing to pay all-in);
//   - MakerSells: the smallest premium whose premium - fee >= net (what you want to keep).
func PremiumForNet(side uint8, net, amount, strike *big.Int, feeBps, notionalFeeBps uint64, unit *big.Int) (*big.Int, error) {
	if net == nil || net.Sign() < 0 {
		return nil, errors.New("net must be >= 0")
	}
	one := big.NewInt(1)
	cost := func(p *big.Int) *big.Int {
		return new(big.Int).Add(p, Fee(p, amount, strike, feeBps, notionalFeeBps, unit))
	}
	kept := func(p *big.Int) *big.Int {
		return new(big.Int).Sub(p, Fee(p, amount, strike, feeBps, notionalFeeBps, unit))
	}
	if side == MakerBuys { // cost() is strictly increasing: the last p with cost(p) <= net
		lo, hi := new(big.Int), new(big.Int).Set(net)
		for lo.Cmp(hi) < 0 {
			mid := new(big.Int).Add(lo, hi)
			mid.Add(mid, one).Rsh(mid, 1)
			if cost(mid).Cmp(net) <= 0 {
				lo = mid
			} else {
				hi = mid.Sub(mid, one)
			}
		}
		return lo, nil
	}
	// kept() is non-decreasing and kept(p) >= p * (1 - feeBps / 1e4): the first p with kept(p) >= net
	if feeBps >= BPS && notionalFeeBps == 0 {
		return nil, errors.New("a 100 % fee leaves nothing to keep")
	}
	lo, hi := new(big.Int).Set(net), new(big.Int).Set(net)
	if hi.Sign() == 0 {
		hi.SetInt64(1)
	}
	for kept(hi).Cmp(net) < 0 {
		hi.Lsh(hi, 1)
	}
	for lo.Cmp(hi) < 0 {
		mid := new(big.Int).Add(lo, hi)
		mid.Rsh(mid, 1)
		if kept(mid).Cmp(net) >= 0 {
			hi = mid
		} else {
			lo = mid.Add(mid, one)
		}
	}
	return lo, nil
}

// LongPayout is what `amount` long pays at settlement price `price` (quote raw per token), rounded down as
// OptionsCore does: a call pays amount * (P - K) / P underlying, a put amount * (K - P) / unit quote.
func LongPayout(s Series, amount, price, unit *big.Int) *big.Int {
	k, p := nz(s.Strike), nz(price)
	if s.IsPut {
		if p.Cmp(k) >= 0 {
			return new(big.Int)
		}
		out := new(big.Int).Mul(nz(amount), new(big.Int).Sub(k, p))
		return out.Quo(out, unitOr(unit))
	}
	if p.Cmp(k) <= 0 {
		return new(big.Int)
	}
	out := new(big.Int).Mul(nz(amount), new(big.Int).Sub(p, k))
	return out.Quo(out, p)
}

// ParseUnits turns a decimal string ("1000", "12.5") into raw units of a token with `decimals` decimals, exactly
// (no floating point). More fractional digits than `decimals` is an error.
func ParseUnits(s string, decimals int) (*big.Int, error) {
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	if whole+frac == "" {
		return nil, invalid("empty amount")
	}
	if whole == "" {
		whole = "0"
	}
	if len(frac) > decimals {
		return nil, invalid("%q has more than %d decimals", s, decimals)
	}
	v, err := ParseUint256(whole + frac + strings.Repeat("0", decimals-len(frac)))
	if err != nil {
		return nil, invalid("not a non-negative decimal amount: %q", s)
	}
	return v, nil
}

// FormatUnits renders raw units with `decimals` decimals, trailing zeros trimmed ("unlimited" at >= 2^255).
func FormatUnits(raw *big.Int, decimals int) string {
	v := nz(raw)
	if v.BitLen() >= 256 {
		return "unlimited"
	}
	s := v.String()
	if decimals == 0 {
		return s
	}
	if len(s) <= decimals {
		s = strings.Repeat("0", decimals-len(s)+1) + s
	}
	whole, frac := s[:len(s)-decimals], strings.TrimRight(s[len(s)-decimals:], "0")
	if frac == "" {
		return whole
	}
	return whole + "." + frac
}
