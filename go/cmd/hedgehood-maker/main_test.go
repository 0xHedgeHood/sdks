package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0xHedgeHood/sdks/go/maker"
)

// The CLI against fakes: a JSON-RPC server answering eth_call by selector, and an unreachable gateway. Keys are
// generated per test; no transaction is ever sent.

const (
	tMaker = "0x36437b878415EdA1a24186CF79AFffBc9ecEd298"
	tHot   = "0xa6E328D981E199bD7A9C466AC3684679CF3a6D97"
)

func runCLI(t *testing.T, getenv func(string) string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	code = run(env{context.Background(), &o, &e, getenv}, args)
	return code, o.String(), e.String()
}

func TestTxPrintsUnsignedCalldataAndACastLine(t *testing.T) {
	code, out, errOut := runCLI(t, nil, "tx", "setSigner", "--maker", tMaker, "--signer", tHot, "--expires", "1800000000")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"UNSIGNED", "from " + tMaker, `"data":"0x07843c87`,
		`cast send 0xda43b0933031e41D5B367E1807536FcF4ebdEE7e "setSigner(address,uint64)" "` + tHot + `" "1800000000" --rpc-url https://rpc.testnet.chain.robinhood.com --account <your-keystore>`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}

	code, out, _ = runCLI(t, nil, "tx", "approve", "--owner", tMaker, "--amount", "max", "--json")
	var rows []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
		t.Fatalf("approve --json: %d %s", code, out)
	}
	if rows[0]["to"] != strings.ToLower(maker.Testnet.QuoteToken.Hex()) ||
		!strings.HasSuffix(rows[0]["data"].(string), strings.Repeat("f", 64)) || !strings.Contains(rows[0]["cast"].(string), "<your-keystore>") {
		t.Fatalf("approve %v", rows[0])
	}

	code, out, _ = runCLI(t, nil, "tx", "deposit", "--from", tMaker, "--amount", "12.5", "--json")
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil ||
		!strings.HasSuffix(rows[0]["data"].(string), hex.EncodeToString(common.LeftPadBytes(big.NewInt(12_500_000).Bytes(), 32))) {
		t.Fatalf("deposit: %d %s", code, out)
	}

	code, out, _ = runCLI(t, nil, "tx", "invalidateNonces", "--maker", tMaker, "--nonces", "1,2,300", "--json")
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 {
		t.Fatalf("invalidateNonces: %d %s", code, out)
	}

	dir := t.TempDir()
	series, _ := json.Marshal(maker.Series{Underlying: maker.Testnet.Underlyings["TSLA"].Token, Quote: maker.Testnet.QuoteToken,
		Oracle: maker.Testnet.Underlyings["TSLA"].Oracle, Expiry: 1794603600, IsPut: true, Strike: big.NewInt(330_000_000)})
	sf := filepath.Join(dir, "series.json")
	_ = os.WriteFile(sf, series, 0o600)
	code, out, errOut = runCLI(t, nil, "tx", "redeemLong", "--series", sf, "--amount", "1", "--on-behalf", tMaker)
	if code != 0 || !strings.Contains(out, "0xf8ad1b36") || !strings.Contains(out, `"1000000000000000000"`) {
		t.Fatalf("redeemLong: %d %s %s", code, out, errOut)
	}

	for _, bad := range [][]string{
		{"tx", "setSigner", "--signer", tHot},                        // no maker
		{"tx", "deposit", "--from", tMaker, "--amount", "1.0000001"}, // too many decimals
		{"tx", "deposit", "--from", tMaker, "--amount", "-1"},        // negative
		{"tx", "invalidateNonces", "--maker", tMaker},                // nothing to burn
		{"tx", "send", "--maker", tMaker},                            // no such transaction
	} {
		if code, _, _ := runCLI(t, nil, bad...); code == 0 {
			t.Errorf("accepted %v", bad)
		}
	}
}

func goldenQuoteJSON() []byte {
	q := maker.Quote{
		Maker:  common.HexToAddress("0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308"),
		Signer: common.HexToAddress("0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308"),
		Taker:  common.HexToAddress("0x36437b878415EdA1a24186CF79AFffBc9ecEd298"),
		Series: maker.Series{Underlying: common.HexToAddress("0x267ddeE02B44FDF4D073E124869DaA5BDbF1759D"),
			Quote: common.HexToAddress("0x850DE815d41F6983Aaf09247f9a465c0aefff04C"), Oracle: common.HexToAddress("0x0767e42f22cF3D0Ee3b49d5eDAad65b62CD0B3a2"),
			Expiry: 1793826000, Strike: big.NewInt(215_000_000)},
		Amount: new(big.Int).Mul(big.NewInt(5), maker.Share), MinFill: new(big.Int), Premium: big.NewInt(5_164_250),
		ValidUntil: 1793766600, Nonce: big.NewInt(42), Epoch: new(big.Int),
	}
	b, _ := json.Marshal(map[string]any{"type": "quote", "rfqId": "r1", "quote": q,
		"signature": "0x141373907102bdbdb3f7cecfa039b039ad6e6af54fe5a7ce0f195f6709e396ed3360ba613c6f7d76706fa18b4c6123a20896befeeb53ec9c9a2ae8afbb29577d1b"})
	return b
}

func TestQuoteDigestPrintsTheGoldens(t *testing.T) {
	f := filepath.Join(t.TempDir(), "quote.json")
	_ = os.WriteFile(f, goldenQuoteJSON(), 0o600)
	code, out, errOut := runCLI(t, nil, "quote-digest", "--quote", f, "--settler", "0x5991A2dF15A8F6A256D3Ec51E99254Cd3fb576A9")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	k7, _ := crypto.ToECDSA(common.LeftPadBytes([]byte{7}, 32))
	for _, want := range []string{"0x15adc15e4a460be6a0dd0cde1a16c01c5d1e74d73bcc5edd7a54823a7dcfd81e",
		"11623297058463634065704459141464122235595256587129134766422972952495797106800",
		// the shared golden quote names 0x17BC... as signer, but its golden signature is by private key 7
		crypto.PubkeyToAddress(k7.PublicKey).Hex() + " (DOES NOT match quote.signer 0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in\n%s", want, out)
		}
	}

	// a quote whose signer field is the key that signed it
	key, _ := crypto.GenerateKey()
	var frame struct {
		Quote maker.Quote `json:"quote"`
	}
	_ = json.Unmarshal(goldenQuoteJSON(), &frame)
	q := frame.Quote
	q.Signer = crypto.PubkeyToAddress(key.PublicKey)
	sig, _ := maker.SignQuote(q, maker.Testnet.ChainID, maker.Testnet.Settler, key)
	b, _ := json.Marshal(q)
	_ = os.WriteFile(f, b, 0o600)
	code, out, errOut = runCLI(t, nil, "quote-digest", "--quote", f, "--signature", "0x"+hex.EncodeToString(sig))
	if code != 0 || !strings.Contains(out, q.Signer.Hex()+" (matches quote.signer)") ||
		!strings.Contains(out, maker.Digest(q, maker.Testnet.ChainID, maker.Testnet.Settler).Hex()) {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
}

// fakeRPC is a JSON-RPC server answering eth_chainId and eth_call by selector; anything else fails the test.
func fakeRPC(t *testing.T, answers map[string]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any               `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0xb626"
		case "eth_call":
			var call struct {
				Data string `json:"data"`
			}
			_ = json.Unmarshal(req.Params[0], &call)
			if a, ok := answers[call.Data[2:10]]; ok {
				res["result"] = "0x" + a
			} else {
				res["error"] = map[string]any{"code": -32000, "message": "execution reverted"}
			}
		default:
			t.Errorf("CLI sent %s", req.Method)
			res["error"] = map[string]any{"code": -32601, "message": "not allowed"}
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func w(v int64) string { return hex.EncodeToString(common.LeftPadBytes(big.NewInt(v).Bytes(), 32)) }

func TestStateAndBalancesAreReadOnly(t *testing.T) {
	srv := fakeRPC(t, map[string]string{
		"24a9d853": w(1250), "1faa35a9": w(100), "8b810c36": w(3), "e57def0c": w(1_800_000_000), "b6363cf2": w(1),
		"f476ea2b": w(0), "f7888aec": w(1_000_000_000), "742e1c0f": w(0) + w(0), "70a08231": w(2_500_000),
		"dd62ed3e": hex.EncodeToString(maker.MaxUint256().Bytes()), "00fdd58e": w(1e18),
	})
	code, out, errOut := runCLI(t, nil, "state", "--rpc", srv.URL, "--maker", tMaker, "--signer", tHot, "--external", tHot)
	if code != 0 {
		t.Fatalf("state exit %d: %s", code, errOut)
	}
	for _, want := range []string{"1250 bps", "capped at 100 bps", "epoch(maker)             3", "1800000000",
		"linked(" + tHot + ", maker) false", "isOperator(maker, settler) true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("state: missing %q in\n%s", want, out)
		}
	}
	code, out, errOut = runCLI(t, nil, "balances", "--rpc", srv.URL, "--maker", tMaker, "--series-id", "7")
	if code != 0 {
		t.Fatalf("balances exit %d: %s", code, errOut)
	}
	for _, want := range []string{"settler balance          1000 USDG", "2.5 USDG, allowance to the settler unlimited",
		"long 7  1 tokens"} {
		if !strings.Contains(out, want) {
			t.Fatalf("balances: missing %q in\n%s", want, out)
		}
	}
	if code, _, _ := runCLI(t, nil, "state", "--rpc", srv.URL, "--maker", tMaker, "--chain-id", "1"); code == 0 {
		t.Fatal("accepted an RPC on another chain")
	}
}

func TestConnectReadsTheKeyFromAFileOrTheEnvironmentAndNeverPrintsIt(t *testing.T) {
	key, _ := crypto.GenerateKey()
	keyHex := hex.EncodeToString(crypto.FromECDSA(key))
	addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
	f := filepath.Join(t.TempDir(), "signer.key")
	if err := os.WriteFile(f, []byte("0x"+keyHex+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dead := "ws://127.0.0.1:1/v1/maker/ws" // nothing listens: the client keeps retrying until --duration
	base := []string{"connect", "--gateway", dead, "--bps", "40", "--duration", "300ms"}

	code, out, errOut := runCLI(t, nil, append(base, "--key-file", f)...)
	if code != 0 || !strings.Contains(out, "demo maker "+addr) {
		t.Fatalf("key file: exit %d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out+errOut, keyHex) {
		t.Fatal("the key was printed")
	}

	getenv := func(k string) string {
		if k == "MY_KEY" {
			return keyHex
		}
		return ""
	}
	code, out, errOut = runCLI(t, getenv, append(base, "--key-env", "MY_KEY")...)
	if code != 0 || !strings.Contains(out, "demo maker "+addr) || strings.Contains(out+errOut, keyHex) {
		t.Fatalf("key env: exit %d\n%s\n%s", code, out, errOut)
	}

	if code, _, errOut := runCLI(t, nil, base...); code == 0 || !strings.Contains(errOut, "no signer key") {
		t.Fatalf("no key: exit %d %s", code, errOut)
	}
	bad := filepath.Join(t.TempDir(), "bad.key")
	_ = os.WriteFile(bad, []byte("not-a-key-SECRET"), 0o600)
	if code, out, errOut := runCLI(t, nil, append(base, "--key-file", bad)...); code == 0 ||
		strings.Contains(out+errOut, "SECRET") {
		t.Fatalf("bad key file: exit %d, output %s %s", code, out, errOut)
	}
	if code, _, _ := runCLI(t, nil, append(base, "--key", keyHex)...); code != 2 {
		t.Fatal("a --key flag must not exist")
	}
	loose := filepath.Join(t.TempDir(), "loose.key")
	_ = os.WriteFile(loose, []byte(keyHex), 0o644)
	if _, _, errOut := runCLI(t, nil, append(base, "--key-file", loose)...); !strings.Contains(errOut, "readable by other users") {
		t.Fatalf("no warning for a world-readable key file: %s", errOut)
	}
}

func TestDemoPricer(t *testing.T) {
	r := maker.RFQ{Side: maker.MakerBuys, Amount: maker.Dec{Int: new(big.Int).Mul(big.NewInt(2), maker.Share)},
		Series: maker.Series{Strike: big.NewInt(330_000_000)}}
	ctx := context.Background()
	a, _ := demoPricer(big.NewInt(1_250_000), 0, "buy", nil, nil)(ctx, r)
	if a.Premium.Int64() != 2_500_000 {
		t.Fatalf("per token: %v", a)
	}
	a, _ = demoPricer(nil, 40, "both", nil, nil)(ctx, r)
	if a.Premium.Int64() != 2_640_000 { // 2 x 330 USDG x 0.4 %
		t.Fatalf("bps: %v", a)
	}
	sell := r
	sell.Side = maker.MakerSells
	if a, _ := demoPricer(nil, 40, "buy", nil, nil)(ctx, sell); a.Decline != "no_inventory" {
		t.Fatalf("sell on a buy-only maker: %v", a)
	}
	if a, _ := demoPricer(nil, 40, "sell", nil, nil)(ctx, r); a.Decline != "side_not_quoted" {
		t.Fatalf("buy on a sell-only maker: %v", a)
	}
	if a, _ := demoPricer(nil, 40, "both", maker.Share, nil)(ctx, r); a.Decline != "too_big" {
		t.Fatalf("max amount: %v", a)
	}
	// closes: only what the maker holds
	holds := func(why string, err error) func(context.Context, maker.RFQ) (string, error) {
		return func(context.Context, maker.RFQ) (string, error) { return why, err }
	}
	if a, _ := demoPricer(nil, 40, "both", nil, holds("", nil))(ctx, sell); a.Premium == nil || a.Premium.Sign() <= 0 {
		t.Fatalf("held long: %v", a)
	}
	if a, _ := demoPricer(nil, 40, "both", nil, holds("holds 0 of 2", nil))(ctx, sell); a.Decline != "no_inventory" {
		t.Fatalf("no long: %v", a)
	}
	if a, _ := demoPricer(nil, 40, "both", nil, holds("", errors.New("rpc down")))(ctx, sell); a.Decline != "unavailable" {
		t.Fatalf("rpc down: %v", a)
	}
	if a, _ := demoPricer(nil, 40, "sell", nil, nil)(ctx, sell); a.Decline != "no_inventory" {
		t.Fatalf("no reader: %v", a)
	}
}
