package maker

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// ValidationError is a value that does not fit the settler's types or the gateway's rules.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{fmt.Sprintf(format, a...)} }

var maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// MaxUint256 returns 2^256 - 1 (a fresh copy).
func MaxUint256() *big.Int { return new(big.Int).Set(maxUint256) }

// Dec is a uint256 that travels as a decimal string. A bare JSON number is accepted on input, as the gateway does.
type Dec struct{ *big.Int }

// MarshalJSON writes the value as a decimal string (nil = "0").
func (d Dec) MarshalJSON() ([]byte, error) {
	if d.Int == nil {
		return []byte(`"0"`), nil
	}
	return json.Marshal(d.Int.String())
}

// UnmarshalJSON reads a decimal string or a bare number; negative, fractional, hex and > 256-bit values are refused.
func (d *Dec) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	v, err := ParseUint256(s)
	if err != nil {
		return err
	}
	d.Int = v
	return nil
}

// ParseUint256 parses a non-negative decimal integer of at most 256 bits.
func ParseUint256(s string) (*big.Int, error) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return nil, invalid("not a uint256 decimal: %q", s)
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.BitLen() > 256 {
		return nil, invalid("not a uint256 decimal: %q", s)
	}
	return v, nil
}

func nz(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v
}

func eq(a, b *big.Int) bool { return nz(a).Cmp(nz(b)) == 0 }

// Series is OptionsCore.SeriesParams. Strike is quote-token raw (USDG 1e6) per whole underlying token.
type Series struct {
	Underlying common.Address
	Quote      common.Address
	Oracle     common.Address
	Expiry     uint64 // unix seconds, the 16:00 New York close of a listed weekday
	IsPut      bool
	Strike     *big.Int
	Physical   bool
}

// Equal reports whether two series are the same.
func (s Series) Equal(o Series) bool {
	return s.Underlying == o.Underlying && s.Quote == o.Quote && s.Oracle == o.Oracle && s.Expiry == o.Expiry &&
		s.IsPut == o.IsPut && eq(s.Strike, o.Strike) && s.Physical == o.Physical
}

// ID is OptionsCore.idOf, also the ERC-6909 id of the long.
func (s Series) ID() *big.Int { return SeriesID(s) }

// Quote is IRfqSettler.Quote: a firm quote the maker (or its delegated signer) signs under EIP-712.
type Quote struct {
	Maker           common.Address
	Signer          common.Address
	Taker           common.Address
	ExternalAccount common.Address // zero = the maker's settler balance; else that wallet, by allowance
	Series          Series
	Side            uint8
	Amount          *big.Int // underlying raw (1e18 = one token)
	MinFill         *big.Int
	Premium         *big.Int // total quote-token raw for Amount, fee excluded
	ValidUntil      uint64   // unix seconds
	Nonce           *big.Int
	Epoch           *big.Int
}

// ---------------------------------------------------------------- JSON
//
// On the wire uint256 fields (strike, amount, minFill, premium, nonce, epoch) are decimal strings; uint64 / uint8
// fields (expiry, validUntil, side, chainId) are JSON numbers (the gateway rejects strings there). Addresses are 0x
// hex in any case.

type seriesJSON struct {
	Underlying common.Address `json:"underlying"`
	Quote      common.Address `json:"quote"`
	Oracle     common.Address `json:"oracle"`
	Expiry     uint64         `json:"expiry"`
	IsPut      bool           `json:"isPut"`
	Strike     Dec            `json:"strike"`
	Physical   bool           `json:"physical"`
}

type quoteJSON struct {
	Maker           common.Address `json:"maker"`
	Signer          common.Address `json:"signer"`
	Taker           common.Address `json:"taker"`
	ExternalAccount common.Address `json:"externalAccount"`
	Series          seriesJSON     `json:"series"`
	Side            uint8          `json:"side"`
	Amount          Dec            `json:"amount"`
	MinFill         Dec            `json:"minFill"`
	Premium         Dec            `json:"premium"`
	ValidUntil      uint64         `json:"validUntil"`
	Nonce           Dec            `json:"nonce"`
	Epoch           Dec            `json:"epoch"`
}

func (s Series) toJSON() seriesJSON {
	return seriesJSON{s.Underlying, s.Quote, s.Oracle, s.Expiry, s.IsPut, Dec{s.Strike}, s.Physical}
}

func (j seriesJSON) series() Series {
	return Series{j.Underlying, j.Quote, j.Oracle, j.Expiry, j.IsPut, nz(j.Strike.Int), j.Physical}
}

// MarshalJSON writes the series as the gateway reads it.
func (s Series) MarshalJSON() ([]byte, error) { return json.Marshal(s.toJSON()) }

// UnmarshalJSON reads a series as the gateway writes it; every field but physical is required.
func (s *Series) UnmarshalJSON(b []byte) error {
	if err := requireKeys(b, "series", "underlying", "quote", "oracle", "expiry", "isPut", "strike"); err != nil {
		return err
	}
	var j seriesJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return invalid("series: %v", err)
	}
	*s = j.series()
	return nil
}

// MarshalJSON writes the quote as the gateway reads it.
func (q Quote) MarshalJSON() ([]byte, error) {
	return json.Marshal(quoteJSON{q.Maker, q.Signer, q.Taker, q.ExternalAccount, q.Series.toJSON(), q.Side,
		Dec{q.Amount}, Dec{q.MinFill}, Dec{q.Premium}, q.ValidUntil, Dec{q.Nonce}, Dec{q.Epoch}})
}

// UnmarshalJSON reads a quote; externalAccount and epoch default to zero, every other field is required.
func (q *Quote) UnmarshalJSON(b []byte) error {
	if err := requireKeys(b, "quote", "maker", "signer", "taker", "series", "side", "amount", "minFill", "premium",
		"validUntil", "nonce"); err != nil {
		return err
	}
	var j quoteJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return invalid("quote: %v", err)
	}
	if j.Side != MakerBuys && j.Side != MakerSells {
		return invalid("side must be 0 (MakerBuys) or 1 (MakerSells), got %d", j.Side)
	}
	*q = Quote{j.Maker, j.Signer, j.Taker, j.ExternalAccount, j.Series.series(), j.Side, nz(j.Amount.Int),
		nz(j.MinFill.Int), nz(j.Premium.Int), j.ValidUntil, nz(j.Nonce.Int), nz(j.Epoch.Int)}
	return nil
}

func requireKeys(b []byte, what string, keys ...string) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return invalid("%s: not an object", what)
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok || string(v) == "null" {
			return invalid("%s: missing %s", what, k)
		}
	}
	return nil
}

// ---------------------------------------------------------------- server -> maker frames

// Frame is a frame from the gateway. Raw holds the bytes as received.
type Frame interface {
	Type() string
	Raw() []byte
}

type base struct {
	T   string          `json:"type"`
	raw json.RawMessage `json:"-"`
}

func (b base) Type() string { return b.T }
func (b base) Raw() []byte  { return b.raw }

// Challenge opens the login.
type Challenge struct {
	base
	Nonce   string `json:"nonce"`
	ChainID uint64 `json:"chainId"`
}

// Ready confirms the login with the deployment and the maker's current epoch.
type Ready struct {
	base
	Maker          common.Address `json:"maker"`
	Settler        common.Address `json:"settler"`
	FeeBps         uint64         `json:"feeBps"`
	NotionalFeeBps uint64         `json:"notionalFeeBps"`
	Epoch          Dec            `json:"epoch"`
}

// RFQ is a taker's request as every maker sees it. Answer before RespondBy (unix ms) with validUntil >= MinValidUntil.
type RFQ struct {
	base
	ID            string         `json:"rfqId"`
	ChainID       uint64         `json:"chainId"`
	Settler       common.Address `json:"settler"`
	Taker         common.Address `json:"taker"`
	Side          uint8          `json:"side"`
	Series        Series         `json:"series"`
	Amount        Dec            `json:"amount"`
	RespondBy     int64          `json:"respondBy"`     // unix milliseconds
	MinValidUntil uint64         `json:"minValidUntil"` // unix seconds
}

// MakerBuys reports whether the taker writes and the maker buys the long.
func (r RFQ) MakerBuys() bool { return r.Side == MakerBuys }

// Won tells the maker its quote won the auction.
type Won struct {
	base
	RFQID  string      `json:"rfqId"`
	Digest common.Hash `json:"digest"`
}

// Lost tells the maker another quote won (or none did).
type Lost struct {
	base
	RFQID string `json:"rfqId"`
}

// Fill reports the on-chain fill of a winning quote.
type Fill struct {
	base
	Digest common.Hash `json:"digest"`
	TxHash common.Hash `json:"txHash"`
	Amount Dec         `json:"amount"`
	RFQID  string      `json:"rfqId"`
}

// Rejected is why a quote of this maker cannot win (see the maker guide's rejection codes).
type Rejected struct {
	base
	RFQID  string `json:"rfqId"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// ErrorFrame is a gateway error (code "login" on a refused login, then the socket closes).
type ErrorFrame struct {
	base
	Code   string `json:"code"`
	Reason string `json:"reason"`
	RFQID  string `json:"rfqId"`
}

// Paused acknowledges a pause.
type Paused struct{ base }

// Resumed acknowledges a resume.
type Resumed struct{ base }

// Unknown is a frame type this package does not know.
type Unknown struct{ base }

// ParseFrame decodes one gateway frame. A malformed known frame is a *ValidationError.
func ParseFrame(b []byte) (Frame, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, invalid("frame: not a JSON object")
	}
	bs := base{T: head.Type, raw: append(json.RawMessage(nil), b...)}
	var f Frame
	var req []string
	switch head.Type {
	case "challenge":
		f, req = &Challenge{base: bs}, []string{"nonce", "chainId"}
	case "ready":
		f, req = &Ready{base: bs}, []string{"maker", "settler"}
	case "rfq":
		f, req = &RFQ{base: bs}, []string{"rfqId", "chainId", "settler", "taker", "side", "series", "amount",
			"respondBy", "minValidUntil"}
	case "won":
		f = &Won{base: bs}
	case "lost":
		f = &Lost{base: bs}
	case "fill":
		f = &Fill{base: bs}
	case "rejected":
		f = &Rejected{base: bs}
	case "error":
		f = &ErrorFrame{base: bs}
	case "paused":
		return Paused{bs}, nil
	case "resumed":
		return Resumed{bs}, nil
	default:
		return Unknown{bs}, nil
	}
	if err := requireKeys(b, head.Type, req...); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, f); err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			return nil, err
		}
		return nil, invalid("%s: %v", head.Type, err)
	}
	switch v := f.(type) {
	case *Ready:
		v.Epoch.Int = nz(v.Epoch.Int)
		return *v, nil
	case *RFQ:
		if v.ID == "" {
			return nil, invalid("rfq: empty rfqId")
		}
		if v.Side != MakerBuys && v.Side != MakerSells {
			return nil, invalid("rfq: side must be 0 or 1, got %d", v.Side)
		}
		return *v, nil
	case *Challenge:
		return *v, nil
	case *Won:
		return *v, nil
	case *Lost:
		return *v, nil
	case *Fill:
		v.Amount.Int = nz(v.Amount.Int)
		return *v, nil
	case *Rejected:
		return *v, nil
	case *ErrorFrame:
		return *v, nil
	}
	return f, nil
}

// ---------------------------------------------------------------- maker -> server frames

// LoginFrame is the answer to the challenge.
type LoginFrame struct {
	Type      string         `json:"type"` // "login"
	Maker     common.Address `json:"maker"`
	Signer    common.Address `json:"signer"`
	Signature string         `json:"signature"`
}

// QuoteFrame answers a request with a signed quote.
type QuoteFrame struct {
	Type      string `json:"type"` // "quote"
	RFQID     string `json:"rfqId"`
	Quote     Quote  `json:"quote"`
	Signature string `json:"signature"`
}

// DeclineFrame answers a request with a refusal (a decline counts as an answer for the silence breaker).
type DeclineFrame struct {
	Type   string `json:"type"` // "decline"
	RFQID  string `json:"rfqId"`
	Reason string `json:"reason"`
}

// NewDecline is the decline frame for a request ("declined" when reason is empty).
func NewDecline(rfqID, reason string) DeclineFrame {
	if reason == "" {
		reason = "declined"
	}
	return DeclineFrame{"decline", rfqID, reason}
}
