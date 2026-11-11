package maker

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// The parts of the RfqSettler, OptionsCore and ERC-20 ABIs a maker uses, built from the same type list the EIP-712
// encoding uses. The selectors are pinned against the compiled contracts in the tests.

var seriesComponents = []abi.ArgumentMarshaling{
	{Name: "underlying", Type: "address"}, {Name: "quote", Type: "address"}, {Name: "oracle", Type: "address"},
	{Name: "expiry", Type: "uint64"}, {Name: "isPut", Type: "bool"}, {Name: "strike", Type: "uint256"},
	{Name: "physical", Type: "bool"},
}

var quoteComponents = []abi.ArgumentMarshaling{
	{Name: "maker", Type: "address"}, {Name: "signer", Type: "address"}, {Name: "taker", Type: "address"},
	{Name: "externalAccount", Type: "address"}, {Name: "series", Type: "tuple", Components: seriesComponents},
	{Name: "side", Type: "uint8"}, {Name: "amount", Type: "uint256"}, {Name: "minFill", Type: "uint256"},
	{Name: "premium", Type: "uint256"}, {Name: "validUntil", Type: "uint64"}, {Name: "nonce", Type: "uint256"},
	{Name: "epoch", Type: "uint256"},
}

func abiType(t string) abi.Type {
	var (
		ty  abi.Type
		err error
	)
	switch t {
	case "series":
		ty, err = abi.NewType("tuple", "", seriesComponents)
	case "quote":
		ty, err = abi.NewType("tuple", "", quoteComponents)
	default:
		ty, err = abi.NewType(t, "", nil)
	}
	if err != nil {
		panic(err)
	}
	return ty
}

func args(types ...string) abi.Arguments {
	out := make(abi.Arguments, len(types))
	for i, t := range types {
		out[i] = abi.Argument{Name: fmt.Sprintf("a%d", i), Type: abiType(t)}
	}
	return out
}

func method(name string, ins []string, outs ...string) abi.Method {
	mut := "nonpayable"
	if len(outs) > 0 {
		mut = "view"
	}
	return abi.NewMethod(name, name, abi.Function, mut, false, false, args(ins...), args(outs...))
}

func in(types ...string) []string { return types }

// Methods, keyed as the goldens name them ("core." / "erc20." prefixes for the other contracts).
var Methods = map[string]abi.Method{
	// RfqSettler
	"setSigner":        method("setSigner", in("address", "uint64")),
	"deposit":          method("deposit", in("address", "address", "uint256")),
	"requestWithdraw":  method("requestWithdraw", in("address", "uint256")),
	"withdraw":         method("withdraw", in("address", "address")),
	"increaseEpoch":    method("increaseEpoch", in("address")),
	"invalidateNonces": method("invalidateNonces", in("address", "uint256", "uint256")),
	"linkMaker":        method("linkMaker", in("address", "bool")),
	"fill":             method("fill", in("quote", "bytes", "uint256")),
	"close":            method("close", in("quote", "bytes", "uint256")),
	"epoch":            method("epoch", in("address"), "uint256"),
	"signerExpiry":     method("signerExpiry", in("address", "address"), "uint64"),
	"nonceUsed":        method("nonceUsed", in("address", "uint256"), "bool"),
	"settlerBalanceOf": method("balanceOf", in("address", "address"), "uint256"), // (token, account)
	"pendingWithdraw":  method("pendingWithdraw", in("address", "address"), "uint256", "uint256"),
	"linked":           method("linked", in("address", "address"), "bool"), // (account, maker)
	"feeBps":           method("feeBps", nil, "uint256"),
	"notionalFeeBps":   method("notionalFeeBps", nil, "uint256"),
	"quoteDigest":      method("quoteDigest", in("quote"), "bytes32"),
	"previewFill":      method("previewFill", in("quote", "uint256"), "uint256", "uint256"),
	"isListed":         method("isListed", in("series"), "bool"),
	"DOMAIN_SEPARATOR": method("DOMAIN_SEPARATOR", nil, "bytes32"),
	// OptionsCore (ERC-6909 longs)
	"core.setOperator": method("setOperator", in("address", "bool")),
	"core.isOperator":  method("isOperator", in("address", "address"), "bool"),   // (owner, spender)
	"core.balanceOf":   method("balanceOf", in("address", "uint256"), "uint256"), // (owner, id)
	"core.redeemLong":  method("redeemLong", in("series", "uint256", "address", "address")),
	// ERC-20
	"erc20.approve":   method("approve", in("address", "uint256")),
	"erc20.balanceOf": method("balanceOf", in("address"), "uint256"),
	"erc20.allowance": method("allowance", in("address", "address"), "uint256"),
}

// Pack is the calldata of a method of Methods.
func Pack(name string, a ...any) ([]byte, error) {
	m, ok := Methods[name]
	if !ok {
		return nil, fmt.Errorf("unknown method %q", name)
	}
	enc, err := m.Inputs.Pack(a...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return append(append([]byte(nil), m.ID...), enc...), nil
}

type abiSeries struct {
	Underlying common.Address
	Quote      common.Address
	Oracle     common.Address
	Expiry     uint64
	IsPut      bool
	Strike     *big.Int
	Physical   bool
}

type abiQuote struct {
	Maker           common.Address
	Signer          common.Address
	Taker           common.Address
	ExternalAccount common.Address
	Series          abiSeries
	Side            uint8
	Amount          *big.Int
	MinFill         *big.Int
	Premium         *big.Int
	ValidUntil      uint64
	Nonce           *big.Int
	Epoch           *big.Int
}

func seriesTuple(s Series) abiSeries {
	return abiSeries{s.Underlying, s.Quote, s.Oracle, s.Expiry, s.IsPut, nz(s.Strike), s.Physical}
}

func quoteTuple(q Quote) abiQuote {
	return abiQuote{q.Maker, q.Signer, q.Taker, q.ExternalAccount, seriesTuple(q.Series), q.Side, nz(q.Amount),
		nz(q.MinFill), nz(q.Premium), q.ValidUntil, nz(q.Nonce), nz(q.Epoch)}
}

// FillCalldata is RfqSettler.fill(q, signature, amount), or close(...) for an early close: what the taker sends (the
// gateway returns it as fillCalldata). Makers do not send it; it is here to check the gateway's encoding.
func FillCalldata(q Quote, sig []byte, amount *big.Int, close bool) ([]byte, error) {
	name := "fill"
	if close {
		name = "close"
	}
	return Pack(name, quoteTuple(q), sig, nz(amount))
}
