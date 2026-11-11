package maker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// An in-process stand-in for the gateway's maker socket (hedgehood-rfq-gateway internal/hub/hub.go): challenge login
// with the allowlist and delegated signers, then a per-test script. It checks quotes with go-ethereum's own EIP-712
// encoder over the JSON it received, independent of this package's encoding.

const fgChain = 46630

var (
	fgSettler = common.HexToAddress("0xda43b0933031e41D5B367E1807536FcF4ebdEE7e")
	fgTaker   = common.HexToAddress("0xa6E328D981E199bD7A9C466AC3684679CF3a6D97")
)

func fgSeries() map[string]any {
	return map[string]any{
		"underlying": "0x6661d20c2e343bb78b42535eb060c3058c744f3f",
		"quote":      "0x850de815d41f6983aaf09247f9a465c0aefff04c",
		"oracle":     "0x9e6c529caa69d44b63df27483c57f97a42d6e898",
		"expiry":     1794603600, "isPut": true, "strike": "330000000", "physical": false,
	}
}

type rfqOpt func(map[string]any)

func withSide(s int) rfqOpt      { return func(m map[string]any) { m["side"] = s } }
func withAmount(a string) rfqOpt { return func(m map[string]any) { m["amount"] = a } }
func withSettler(a common.Address) rfqOpt {
	return func(m map[string]any) { m["settler"] = strings.ToLower(a.Hex()) }
}
func withDeadline(d time.Duration) rfqOpt {
	return func(m map[string]any) {
		by := time.Now().Add(d).UnixMilli()
		m["respondBy"], m["minValidUntil"] = by, by/1000+15
	}
}

func rfqFrame(id string, opts ...rfqOpt) map[string]any {
	by := time.Now().Add(2 * time.Second).UnixMilli()
	m := map[string]any{"type": "rfq", "rfqId": id, "chainId": fgChain, "settler": strings.ToLower(fgSettler.Hex()),
		"taker": strings.ToLower(fgTaker.Hex()), "side": 0, "series": fgSeries(), "amount": "1000000000000000000",
		"respondBy": by, "minValidUntil": by/1000 + 15}
	for _, o := range opts {
		o(m)
	}
	return m
}

// independentDigest hashes a received quote JSON with go-ethereum's TypedData, the way a wallet would.
func independentDigest(t *testing.T, q map[string]any) common.Hash {
	t.Helper()
	s := q["series"].(map[string]any)
	num := func(v any) string {
		switch x := v.(type) {
		case float64:
			return fmt.Sprintf("%.0f", x)
		case json.Number:
			return x.String()
		}
		return v.(string)
	}
	td := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {{Name: "name", Type: "string"}, {Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"}},
			"SeriesParams": {{Name: "underlying", Type: "address"}, {Name: "quote", Type: "address"},
				{Name: "oracle", Type: "address"}, {Name: "expiry", Type: "uint64"}, {Name: "isPut", Type: "bool"},
				{Name: "strike", Type: "uint256"}, {Name: "physical", Type: "bool"}},
			"Quote": {{Name: "maker", Type: "address"}, {Name: "signer", Type: "address"}, {Name: "taker", Type: "address"},
				{Name: "externalAccount", Type: "address"}, {Name: "series", Type: "SeriesParams"}, {Name: "side", Type: "uint8"},
				{Name: "amount", Type: "uint256"}, {Name: "minFill", Type: "uint256"}, {Name: "premium", Type: "uint256"},
				{Name: "validUntil", Type: "uint64"}, {Name: "nonce", Type: "uint256"}, {Name: "epoch", Type: "uint256"}},
		},
		PrimaryType: "Quote",
		Domain: apitypes.TypedDataDomain{Name: "Hedgehood RFQ Settler", Version: "2",
			ChainId: math.NewHexOrDecimal256(fgChain), VerifyingContract: fgSettler.Hex()},
		Message: apitypes.TypedDataMessage{
			"maker": q["maker"], "signer": q["signer"], "taker": q["taker"], "externalAccount": q["externalAccount"],
			"series": map[string]any{"underlying": s["underlying"], "quote": s["quote"], "oracle": s["oracle"],
				"expiry": num(s["expiry"]), "isPut": s["isPut"], "strike": num(s["strike"]), "physical": s["physical"]},
			"side": num(q["side"]), "amount": num(q["amount"]), "minFill": num(q["minFill"]), "premium": num(q["premium"]),
			"validUntil": num(q["validUntil"]), "nonce": num(q["nonce"]), "epoch": num(q["epoch"]),
		},
	}
	h, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		t.Fatal(err)
	}
	return common.BytesToHash(h)
}

// recoverQuote is the address that signed a received quote frame (independent recovery, high s refused).
func recoverQuote(t *testing.T, f map[string]any) common.Address {
	t.Helper()
	sig := hexutil.MustDecode(f["signature"].(string))
	if len(sig) != 65 || (sig[64] != 27 && sig[64] != 28) {
		t.Fatalf("signature is not 65-byte r||s||v with v 27/28: %x", sig)
	}
	s := new(big.Int).SetBytes(sig[32:64])
	if s.Cmp(new(big.Int).Rsh(crypto.S256().Params().N, 1)) > 0 {
		t.Fatal("high s")
	}
	sig[64] -= 27
	pub, err := crypto.SigToPub(independentDigest(t, f["quote"].(map[string]any)).Bytes(), sig)
	if err != nil {
		t.Fatal(err)
	}
	return crypto.PubkeyToAddress(*pub)
}

type gwConn struct {
	t   *testing.T
	ctx context.Context
	c   *websocket.Conn
}

func (g *gwConn) send(v any) {
	b, _ := json.Marshal(v)
	if err := g.c.Write(g.ctx, websocket.MessageText, b); err != nil {
		g.t.Errorf("fake gateway write: %v", err)
	}
}

func (g *gwConn) recv() map[string]any {
	m, err := g.tryRecv()
	if err != nil {
		g.t.Errorf("fake gateway read: %v", err)
	}
	return m
}

// tryRecv is recv for a read the maker may legitimately abandon (the login of a client that refuses the gateway).
func (g *gwConn) tryRecv() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(g.ctx, 5*time.Second)
	defer cancel()
	_, b, err := g.c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("not JSON: %s", b)
	}
	return m, nil
}

// waitClosed reads until the maker goes away.
func (g *gwConn) waitClosed() {
	for {
		if _, _, err := g.c.Read(g.ctx); err != nil {
			return
		}
	}
}

type fakeGW struct {
	t         *testing.T
	makers    map[common.Address]bool
	delegates map[[2]common.Address]bool
	epoch     string
	settler   common.Address
	script    func(g *gwConn, maker common.Address, n int)

	mu          sync.Mutex
	connections int
	logins      []map[string]any
	srv         *httptest.Server
	URL         string
}

func newFakeGW(t *testing.T, makers []common.Address, script func(g *gwConn, maker common.Address, n int)) *fakeGW {
	g := &fakeGW{t: t, makers: map[common.Address]bool{}, delegates: map[[2]common.Address]bool{}, epoch: "0",
		settler: fgSettler, script: script}
	for _, m := range makers {
		g.makers[m] = true
	}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	g.URL = "ws" + strings.TrimPrefix(g.srv.URL, "http") + "/v1/maker/ws"
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGW) delegate(maker, signer common.Address) {
	g.delegates[[2]common.Address{maker, signer}] = true
}

func (g *fakeGW) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(MaxFrame) // the gateway reads at most 16 KiB per frame
	defer c.Close(websocket.StatusNormalClosure, "")
	g.mu.Lock()
	g.connections++
	n := g.connections
	g.mu.Unlock()
	conn := &gwConn{t: g.t, ctx: r.Context(), c: c}
	nb := make([]byte, 16)
	_, _ = rand.Read(nb)
	nonce := hex.EncodeToString(nb)
	conn.send(map[string]any{"type": "challenge", "nonce": nonce, "chainId": fgChain})
	login, err := conn.tryRecv()
	if err != nil {
		return // the maker went away (e.g. it refused the chain)
	}
	g.mu.Lock()
	g.logins = append(g.logins, login)
	g.mu.Unlock()
	maker, reason := g.check(login, nonce)
	if reason != "" {
		conn.send(map[string]any{"type": "error", "code": "login", "reason": reason})
		_ = c.Close(websocket.StatusPolicyViolation, "login")
		return
	}
	conn.send(map[string]any{"type": "ready", "maker": strings.ToLower(maker.Hex()), "settler": g.settler,
		"feeBps": 1250, "notionalFeeBps": 100, "epoch": g.epoch})
	if g.script != nil {
		g.script(conn, maker, n)
	} else {
		conn.waitClosed()
	}
}

func (g *fakeGW) check(login map[string]any, nonce string) (common.Address, string) {
	if login == nil || login["type"] != "login" {
		return common.Address{}, "expected a login frame"
	}
	maker, signer := common.HexToAddress(login["maker"].(string)), common.HexToAddress(login["signer"].(string))
	if !g.makers[maker] {
		return common.Address{}, fmt.Sprintf("maker %s is not on the allowlist", maker.Hex())
	}
	msg := fmt.Sprintf("hedgehood-rfq-login:%d:%s:%s", fgChain, strings.ToLower(maker.Hex()), nonce)
	sig, err := hexutil.Decode(login["signature"].(string))
	if err != nil || len(sig) != 65 {
		return common.Address{}, "signature is not hex"
	}
	sig[64] -= 27
	pub, err := crypto.SigToPub(accounts.TextHash([]byte(msg)), sig)
	if err != nil || crypto.PubkeyToAddress(*pub) != signer {
		return common.Address{}, "login signature does not recover to signer"
	}
	if signer != maker && !g.delegates[[2]common.Address{maker, signer}] {
		return common.Address{}, "signer is not a live delegate of the maker on the settler"
	}
	return maker, ""
}

func (g *fakeGW) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.connections
}
