package maker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	tMaker = common.HexToAddress("0x36437b878415EdA1a24186CF79AFffBc9ecEd298")
	tHot   = common.HexToAddress("0xa6E328D981E199bD7A9C466AC3684679CF3a6D97")
)

func mustTx(t *testing.T) func(UnsignedTx, error) UnsignedTx {
	return func(tx UnsignedTx, err error) UnsignedTx {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
}

// argsOf decodes the calldata with the method its selector names.
func argsOf(t *testing.T, tx UnsignedTx) (string, []any) {
	t.Helper()
	for name, m := range Methods {
		if hex.EncodeToString(m.ID) == hex.EncodeToString(tx.Data[:4]) && m.Sig == tx.Function {
			v, err := m.Inputs.Unpack(tx.Data[4:])
			if err != nil {
				t.Fatal(err)
			}
			return name, v
		}
	}
	t.Fatalf("no method for %x", tx.Data[:4])
	return "", nil
}

func TestOnboardingTransactions(t *testing.T) {
	b, must, usdg := TxsFor(Testnet), mustTx(t), Testnet.QuoteToken

	tx := must(b.SetSigner(tMaker, tHot, 1_800_000_000))
	if name, a := argsOf(t, tx); name != "setSigner" || tx.To != Testnet.Settler || tx.Sender != tMaker ||
		!reflect.DeepEqual(a, []any{tHot, uint64(1_800_000_000)}) || hex.EncodeToString(tx.Data[:4]) != "07843c87" {
		t.Fatalf("setSigner %s %v", name, a)
	}
	j, _ := json.Marshal(tx)
	want := `{"chainId":46630,"data":"` + hexutil.Encode(tx.Data) + `","from":"` + strings.ToLower(tMaker.Hex()) +
		`","to":"` + strings.ToLower(Testnet.Settler.Hex()) + `","value":"0x0"}`
	if string(j) != want {
		t.Fatalf("json %s\nwant %s", j, want)
	}
	if c := tx.Cast(Testnet.RPC); !strings.HasPrefix(c, `cast send `+Testnet.Settler.Hex()+` "setSigner(address,uint64)" "`+
		tHot.Hex()+`" "1800000000" --rpc-url `+Testnet.RPC+` --account <your-keystore>`) {
		t.Fatalf("cast %s", c)
	}

	raw := big.NewInt(1000e6)
	check := func(tx UnsignedTx, wantName string, to common.Address, sender common.Address, want ...any) {
		t.Helper()
		name, a := argsOf(t, tx)
		if name != wantName || tx.To != to || tx.Sender != sender || !reflect.DeepEqual(a, want) {
			t.Fatalf("%s: got %s to %s from %s args %v", wantName, name, tx.To.Hex(), tx.Sender.Hex(), a)
		}
	}
	check(must(b.Approve(tMaker, usdg, common.Address{}, raw)), "erc20.approve", usdg, tMaker, Testnet.Settler, raw)
	check(must(b.Deposit(tMaker, usdg, tMaker, raw)), "deposit", Testnet.Settler, tMaker, usdg, tMaker, raw)
	check(must(b.RequestWithdraw(tMaker, usdg, big.NewInt(5))), "requestWithdraw", Testnet.Settler, tMaker, usdg, big.NewInt(5))
	check(must(b.Withdraw(tMaker, usdg, tMaker)), "withdraw", Testnet.Settler, tMaker, usdg, tMaker)
	check(must(b.IncreaseEpoch(common.Address{}, tMaker)), "increaseEpoch", Testnet.Settler, tMaker, tMaker)
	check(must(b.InvalidateNonces(tHot, tMaker, big.NewInt(7), big.NewInt(5))), "invalidateNonces", Testnet.Settler, tHot,
		tMaker, big.NewInt(7), big.NewInt(5))
	check(must(b.LinkMaker(tHot, tMaker, true)), "linkMaker", Testnet.Settler, tHot, tMaker, true)
	check(must(b.SetOperator(tMaker, common.Address{}, true)), "core.setOperator", Testnet.Core, tMaker, Testnet.Settler, true)

	tx = must(b.RedeemLong(goldenSeries(), e18(1), tMaker, tHot))
	name, a := argsOf(t, tx)
	if name != "core.redeemLong" || tx.To != Testnet.Core || tx.Sender != tMaker || hex.EncodeToString(tx.Data[:4]) != "f8ad1b36" ||
		a[1].(*big.Int).Cmp(e18(1)) != 0 || a[2] != tMaker || a[3] != tHot {
		t.Fatalf("redeemLong %s %v", name, a)
	}
	if !strings.Contains(tx.Cast(""), `"(0x267ddeE02B44FDF4D073E124869DaA5BDbF1759D,0x850DE815d41F6983Aaf09247f9a465c0aefff04C,`+
		`0x0767e42f22cF3D0Ee3b49d5eDAad65b62CD0B3a2,1793826000,false,215000000,false)" "1000000000000000000"`) {
		t.Fatalf("redeemLong cast %s", tx.Cast(""))
	}
	if !strings.Contains(tx.Cast(""), "--rpc-url $RPC") {
		t.Fatal("default rpc placeholder")
	}
}

func TestBuildersValidate(t *testing.T) {
	b := TxsFor(Testnet)
	if _, err := b.Deposit(tMaker, Testnet.QuoteToken, tMaker, big.NewInt(-1)); err == nil {
		t.Fatal("negative amount")
	}
	if _, err := b.Approve(tMaker, Testnet.QuoteToken, common.Address{}, new(big.Int).Lsh(big.NewInt(1), 256)); err == nil {
		t.Fatal("amount above uint256")
	}
	if _, err := (Txs{ChainID: 46630, Settler: Testnet.Settler}).SetOperator(tMaker, common.Address{}, true); err == nil {
		t.Fatal("no core")
	}
}

func TestNoncesAreUniqueAndRollSlots(t *testing.T) {
	m := NewNonceManagerAt(big.NewInt(5))
	seen := map[string]bool{}
	var got []*big.Int
	for i := 0; i < 300; i++ {
		n := m.Next()
		if seen[n.String()] {
			t.Fatal("duplicate nonce")
		}
		seen[n.String()] = true
		got = append(got, n)
	}
	for i, want := range map[int][2]int64{0: {5, 0}, 255: {5, 255}, 256: {6, 0}} {
		s, bit := SplitNonce(got[i])
		if s.Int64() != want[0] || int64(bit) != want[1] {
			t.Fatalf("nonce %d: slot %s bit %d", i, s, bit)
		}
	}
	masks := NonceMasks(got)
	full := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if len(masks) != 2 || masks["5"].Cmp(full) != 0 || masks["6"].Cmp(big.NewInt(1<<44-1)) != 0 {
		t.Fatalf("masks %v", masks)
	}
	if len(m.InvalidateAll()) != 2 {
		t.Fatal("InvalidateAll")
	}
	if NewNonceManager().Next().Cmp(NewNonceManager().Next()) == 0 {
		t.Fatal("random start slots collided")
	}
}

// fakeRPC answers eth_call by selector and records every request.
type fakeRPC struct {
	answers map[string]string
	methods []string
	calls   []map[string]any
}

func (f *fakeRPC) transport(_ context.Context, method string, params []any) (json.RawMessage, error) {
	f.methods = append(f.methods, method)
	if method == "eth_chainId" {
		return json.RawMessage(`"0xb626"`), nil
	}
	call := params[0].(map[string]any)
	f.calls = append(f.calls, call)
	data := call["data"].(string)
	out, ok := f.answers[data[2:10]]
	if !ok {
		return nil, &RPCError{method, -32000, "execution reverted"}
	}
	return json.Marshal("0x" + out)
}

func w32(v int64) string { return hex.EncodeToString(common.LeftPadBytes(big.NewInt(v).Bytes(), 32)) }

func TestReaderDecodesSettlerState(t *testing.T) {
	f := &fakeRPC{answers: map[string]string{
		"8b810c36": w32(3), "24a9d853": w32(1250), "1faa35a9": w32(100), "e57def0c": w32(1_800_000_000),
		"1d971f5d": gDigest[2:], "f59a193d": w32(3_098_550) + w32(154_927), "b6363cf2": w32(1),
		"742e1c0f": w32(5) + w32(1_700_000_000), "3644e515": gTestnetSep[2:],
	}}
	r := &Reader{Settler: Testnet.Settler, Core: Testnet.Core, Transport: f.transport}
	ctx := context.Background()
	if id, err := r.ChainID(ctx); err != nil || id != 46630 {
		t.Fatalf("chain id %d %v", id, err)
	}
	if e, err := r.Epoch(ctx, tMaker); err != nil || e.Int64() != 3 {
		t.Fatalf("epoch %v %v", e, err)
	}
	if fb, nb, err := r.Fees(ctx); err != nil || fb != 1250 || nb != 100 {
		t.Fatalf("fees %d %d %v", fb, nb, err)
	}
	if x, err := r.SignerExpiry(ctx, tMaker, tHot); err != nil || x != 1_800_000_000 {
		t.Fatalf("signerExpiry %d %v", x, err)
	}
	if d, err := r.QuoteDigest(ctx, goldenQuote()); err != nil || d.Hex() != gDigest {
		t.Fatalf("quoteDigest %s %v", d.Hex(), err)
	}
	want, _ := Pack("quoteDigest", quoteTuple(goldenQuote()))
	if last := f.calls[len(f.calls)-1]; last["to"] != Testnet.Settler || last["data"] != hexutil.Encode(want) {
		t.Fatalf("quoteDigest call %v", last)
	}
	if p, fee, err := r.PreviewFill(ctx, goldenQuote(), e18(3)); err != nil || p.Int64() != 3_098_550 || fee.Int64() != 154_927 {
		t.Fatalf("previewFill %v %v %v", p, fee, err)
	}
	if ok, err := r.IsOperator(ctx, tMaker, common.Address{}); err != nil || !ok {
		t.Fatalf("isOperator %v %v", ok, err)
	}
	if a, at, err := r.PendingWithdraw(ctx, Testnet.QuoteToken, tMaker); err != nil || a.Int64() != 5 || at.Int64() != 1_700_000_000 {
		t.Fatalf("pendingWithdraw %v %v %v", a, at, err)
	}
	if d, err := r.DomainSeparator(ctx); err != nil || d.Hex() != gTestnetSep {
		t.Fatalf("domain separator %s %v", d.Hex(), err)
	}
	var re *RPCError
	if _, err := r.NonceUsed(ctx, tMaker, big.NewInt(1)); !errors.As(err, &re) {
		t.Fatalf("revert not surfaced: %v", err)
	}
	for _, m := range f.methods {
		if m != "eth_call" && m != "eth_chainId" {
			t.Fatalf("reader sent %s", m)
		}
	}
	if _, err := r.request(ctx, "eth_sendRawTransaction", "0x"); err == nil {
		t.Fatal("reader would send a transaction")
	}
	if _, err := (&Reader{Settler: Testnet.Settler, Transport: f.transport}).LongBalance(ctx, tMaker, big.NewInt(1)); err == nil {
		t.Fatal("no core")
	}
}

func TestCanSell(t *testing.T) {
	long, op := hex.EncodeToString(crypto.Keccak256([]byte("balanceOf(address,uint256)"))[:4]),
		hex.EncodeToString(crypto.Keccak256([]byte("isOperator(address,address)"))[:4])
	f := &fakeRPC{answers: map[string]string{long: w32(2e18), op: w32(1)}}
	r := &Reader{Settler: Testnet.Settler, Core: Testnet.Core, Transport: f.transport}
	ctx := context.Background()
	s := goldenQuote().Series
	if why, err := r.CanSell(ctx, tMaker, s, e18(2)); err != nil || why != "" {
		t.Fatalf("held: %q %v", why, err)
	}
	want, _ := Pack("core.balanceOf", tMaker, SeriesID(s))
	if f.calls[0]["to"] != Testnet.Core || f.calls[0]["data"] != hexutil.Encode(want) {
		t.Fatalf("balanceOf call %v", f.calls[0])
	}
	if why, _ := r.CanSell(ctx, tMaker, s, e18(3)); !strings.HasPrefix(why, "holds") {
		t.Fatalf("short: %q", why)
	}
	f.answers[op] = w32(0)
	if why, _ := r.CanSell(ctx, tMaker, s, e18(1)); !strings.Contains(why, "operator") {
		t.Fatalf("no operator: %q", why)
	}
}
