package maker

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// MakerClient against the in-process fake gateway (fakegw_test.go). Offline; keys are generated per test.

func newKey(t *testing.T) (*ecdsa.PrivateKey, common.Address) {
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k, crypto.PubkeyToAddress(k.PublicKey)
}

func fixed(p int64) Pricer {
	return func(context.Context, RFQ) (Answer, error) { return PriceAt(big.NewInt(p)), nil }
}

func clientFor(t *testing.T, g *fakeGW, key *ecdsa.PrivateKey, p Pricer, mod ...func(*Config)) *Client {
	cfg := Config{URL: g.URL, Signer: key, Pricer: p, ChainID: fgChain, Settler: fgSettler,
		BackoffInitial: 50 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, m := range mod {
		m(&cfg)
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// drive runs the client until done closes (or Run returns) and returns what Run returned.
func drive(t *testing.T, c *Client, done <-chan struct{}) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- c.Run(ctx) }()
	select {
	case <-done:
	case err := <-res:
		cancel()
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("test timed out")
	}
	cancel()
	select {
	case err := <-res:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	return nil
}

type events struct {
	mu sync.Mutex
	fs []Frame
}

func (e *events) add(f Frame) { e.mu.Lock(); e.fs = append(e.fs, f); e.mu.Unlock() }
func (e *events) n() int      { e.mu.Lock(); defer e.mu.Unlock(); return len(e.fs) }

func until(t *testing.T, cond func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(end) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQuoteIsSignedValidAndFollowedToTheFill(t *testing.T) {
	key, addr := newKey(t)
	ev := &events{}
	done := make(chan struct{})
	var q1, q2 map[string]any
	var digest common.Hash
	g := newFakeGW(t, []common.Address{addr}, func(g *gwConn, maker common.Address, n int) {
		g.send(rfqFrame("r1"))
		q1 = g.recv()
		g.send(rfqFrame("r2", withSide(1)))
		q2 = g.recv()
		digest = independentDigest(t, q1["quote"].(map[string]any))
		g.send(map[string]any{"type": "won", "rfqId": "r1", "digest": digest.Hex()})
		g.send(map[string]any{"type": "lost", "rfqId": "r2"})
		g.send(map[string]any{"type": "fill", "digest": digest.Hex(), "txHash": "0x" + strings.Repeat("ab", 32),
			"amount": q1["quote"].(map[string]any)["amount"], "rfqId": "r1"})
		until(t, func() bool { return ev.n() >= 4 })
		close(done)
		g.waitClosed()
	})
	c := clientFor(t, g, key, fixed(2_500_000), func(c *Config) { c.OnEvent = ev.add })
	if err := drive(t, c, done); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, f := range []map[string]any{q1, q2} {
		q := f["quote"].(map[string]any)
		if f["type"] != "quote" || recoverQuote(t, f) != addr {
			t.Fatalf("not a quote signed by the maker: %v", f)
		}
		if common.HexToAddress(q["maker"].(string)) != addr || common.HexToAddress(q["signer"].(string)) != addr ||
			common.HexToAddress(q["taker"].(string)) != fgTaker {
			t.Fatalf("parties %v", q)
		}
		if q["amount"] != "1000000000000000000" || q["minFill"] != q["amount"] || q["premium"] != "2500000" ||
			q["epoch"] != "0" || q["externalAccount"] != "0x0000000000000000000000000000000000000000" {
			t.Fatalf("terms %v", q)
		}
		vu, ok := q["validUntil"].(float64)
		if !ok || int64(vu) < now+50 || int64(vu) > now+120 {
			t.Fatalf("validUntil %v", q["validUntil"])
		}
		if len(f["signature"].(string)) != 132 {
			t.Fatal("signature length")
		}
	}
	if q2["quote"].(map[string]any)["side"] != float64(1) || q1["quote"].(map[string]any)["nonce"] == q2["quote"].(map[string]any)["nonce"] {
		t.Fatal("side or nonce")
	}
	kinds := []string{}
	for _, f := range ev.fs {
		kinds = append(kinds, f.Type())
	}
	if strings.Join(kinds, ",") != "ready,won,lost,fill" {
		t.Fatalf("events %v", kinds)
	}
	r := ev.fs[0].(Ready)
	if r.FeeBps != 1250 || r.NotionalFeeBps != 100 || r.Epoch.Sign() != 0 {
		t.Fatalf("ready %+v", r)
	}
	d, _, ok := c.QuoteFor("r1")
	if !ok || d != digest || ev.fs[1].(Won).Digest != digest || ev.fs[3].(Fill).Digest != digest ||
		ev.fs[3].(Fill).Amount.Cmp(Share) != 0 {
		t.Fatal("digests differ")
	}
}

func TestDelegatedHotSignerLogsInAndSignsForTheMaker(t *testing.T) {
	_, maker := newKey(t)
	hot, hotAddr := newKey(t)
	done := make(chan struct{})
	var q map[string]any
	g := newFakeGW(t, []common.Address{maker}, func(g *gwConn, m common.Address, n int) {
		g.send(rfqFrame("r1"))
		q = g.recv()
		close(done)
		g.waitClosed()
	})
	g.delegate(maker, hotAddr)
	if err := drive(t, clientFor(t, g, hot, fixed(1_000_000), func(c *Config) { c.Maker = maker }), done); err != nil {
		t.Fatal(err)
	}
	if common.HexToAddress(g.logins[0]["maker"].(string)) != maker || common.HexToAddress(g.logins[0]["signer"].(string)) != hotAddr {
		t.Fatalf("login %v", g.logins[0])
	}
	qq := q["quote"].(map[string]any)
	if common.HexToAddress(qq["maker"].(string)) != maker || common.HexToAddress(qq["signer"].(string)) != hotAddr ||
		recoverQuote(t, q) != hotAddr {
		t.Fatalf("quote %v", qq)
	}
}

func TestRefusedLoginIsReturnedAndNotRetried(t *testing.T) {
	_, maker := newKey(t)
	stranger, _ := newKey(t)
	g := newFakeGW(t, []common.Address{maker}, nil)
	never := make(chan struct{})
	var lr *LoginRefusedError
	err := drive(t, clientFor(t, g, stranger, fixed(1), func(c *Config) { c.Maker = maker }), never)
	if !errors.As(err, &lr) || !strings.Contains(lr.Reason, "not a live delegate") {
		t.Fatalf("got %v", err)
	}
	err = drive(t, clientFor(t, g, stranger, fixed(1)), never)
	if !errors.As(err, &lr) || !strings.Contains(lr.Reason, "allowlist") {
		t.Fatalf("got %v", err)
	}
	if g.count() != 2 {
		t.Fatalf("%d connections", g.count())
	}
}

func TestPinnedDeploymentIsEnforced(t *testing.T) {
	key, addr := newKey(t)
	g := newFakeGW(t, []common.Address{addr}, nil)
	never := make(chan struct{})
	var pe *ProtocolError
	err := drive(t, clientFor(t, g, key, fixed(1), func(c *Config) { c.Settler = common.HexToAddress("0x11") }), never)
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "settler") {
		t.Fatalf("got %v", err)
	}
	err = drive(t, clientFor(t, g, key, fixed(1), func(c *Config) { c.ChainID = 1 }), never)
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "chain") {
		t.Fatalf("got %v", err)
	}
}

func TestDeclines(t *testing.T) {
	key, addr := newKey(t)
	var c *Client
	pricer := func(ctx context.Context, r RFQ) (Answer, error) {
		switch r.ID {
		case "boom":
			return Answer{}, errors.New("model blew up")
		case "panic":
			panic("model blew up")
		case "bad": // a quote that does not match the request must never be sent
			q, err := c.BuildQuote(r, big.NewInt(5))
			q.Taker = common.HexToAddress("0x11")
			return SendQuote(q), err
		case "zero":
			return PriceAt(new(big.Int)), nil
		}
		if !r.MakerBuys() {
			return DeclineWith("no_inventory"), nil
		}
		if r.Amount.Cmp(Share) > 0 {
			return DeclineWith("too_big"), nil
		}
		return Answer{}, nil
	}
	done := make(chan struct{})
	got := map[string][2]string{}
	g := newFakeGW(t, []common.Address{addr}, func(g *gwConn, m common.Address, n int) {
		cases := []map[string]any{rfqFrame("sell", withSide(1)), rfqFrame("big", withAmount("10000000000000000000")),
			rfqFrame("none"), rfqFrame("other", withSettler(common.HexToAddress("0x22"))),
			rfqFrame("late", withDeadline(-5*time.Millisecond)), rfqFrame("boom"), rfqFrame("panic"), rfqFrame("bad"),
			rfqFrame("zero"), {"type": "rfq", "rfqId": "broken", "side": 0}}
		for _, f := range cases {
			g.send(f)
		}
		for range cases {
			f := g.recv()
			got[f["rfqId"].(string)] = [2]string{f["type"].(string), f["reason"].(string)}
		}
		close(done)
		g.waitClosed()
	})
	c = clientFor(t, g, key, pricer)
	if err := drive(t, c, done); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"sell": "no_inventory", "big": "too_big", "none": "no_price", "other": "wrong_settler",
		"late": "late", "boom": "maker_error", "panic": "maker_error", "bad": "maker_error", "zero": "no_price",
		"broken": "invalid_rfq"}
	for id, reason := range want {
		if got[id] != [2]string{"decline", reason} {
			t.Errorf("%s: got %v, want decline %s", id, got[id], reason)
		}
	}
}

func TestSlowPricerDeclinesBeforeTheDeadline(t *testing.T) {
	key, addr := newKey(t)
	pricer := func(ctx context.Context, r RFQ) (Answer, error) {
		if r.ID == "slow" {
			time.Sleep(5 * time.Second) // ignores ctx on purpose
		}
		return PriceAt(big.NewInt(7_000_000)), nil
	}
	done := make(chan struct{})
	var slow, fast map[string]any
	var slowAt, respondBy int64
	g := newFakeGW(t, []common.Address{addr}, func(g *gwConn, m common.Address, n int) {
		f := rfqFrame("slow", withDeadline(600*time.Millisecond))
		respondBy = f["respondBy"].(int64)
		g.send(f)
		slow = g.recv()
		slowAt = time.Now().UnixMilli()
		g.send(rfqFrame("fast"))
		fast = g.recv()
		close(done)
		g.waitClosed()
	})
	if err := drive(t, clientFor(t, g, key, pricer), done); err != nil {
		t.Fatal(err)
	}
	if slow["type"] != "decline" || slow["reason"] != "timeout" || slowAt > respondBy {
		t.Fatalf("slow %v at %d, respondBy %d", slow, slowAt, respondBy)
	}
	if fast["type"] != "quote" || fast["quote"].(map[string]any)["premium"] != "7000000" {
		t.Fatalf("fast %v", fast)
	}
}

func TestPricerSeesTheDeadlineBudget(t *testing.T) {
	key, addr := newKey(t)
	var left time.Duration
	pricer := func(ctx context.Context, r RFQ) (Answer, error) {
		dl, _ := ctx.Deadline()
		left = time.UnixMilli(r.RespondBy).Sub(dl)
		return PriceAt(big.NewInt(1)), nil
	}
	done := make(chan struct{})
	g := newFakeGW(t, []common.Address{addr}, func(g *gwConn, m common.Address, n int) {
		g.send(rfqFrame("r1"))
		g.recv()
		close(done)
		g.waitClosed()
	})
	if err := drive(t, clientFor(t, g, key, pricer), done); err != nil {
		t.Fatal(err)
	}
	if left < 140*time.Millisecond || left > 200*time.Millisecond {
		t.Fatalf("pricer deadline is %s before respondBy, want about 150 ms", left)
	}
}

func TestCustomQuoteFromBuildQuote(t *testing.T) {
	key, addr := newKey(t)
	_, treasury := newKey(t)
	var c *Client
	pricer := func(ctx context.Context, r RFQ) (Answer, error) {
		q, err := c.BuildQuote(r, big.NewInt(3_000_000), ValidFor(30*time.Second), WithExternalAccount(treasury))
		return SendQuote(q), err
	}
	done := make(chan struct{})
	var q map[string]any
	var minValid int64
	g := newFakeGW(t, []common.Address{addr}, func(g *gwConn, m common.Address, n int) {
		f := rfqFrame("r1")
		minValid = f["minValidUntil"].(int64)
		g.send(f)
		q = g.recv()
		close(done)
		g.waitClosed()
	})
	c = clientFor(t, g, key, pricer)
	if err := drive(t, c, done); err != nil {
		t.Fatal(err)
	}
	qq := q["quote"].(map[string]any)
	vu := int64(qq["validUntil"].(float64))
	if common.HexToAddress(qq["externalAccount"].(string)) != treasury || recoverQuote(t, q) != addr ||
		vu < minValid || vu > time.Now().Unix()+31 {
		t.Fatalf("quote %v", qq)
	}
}

func TestStaleEpochRejectionMovesTheEpoch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader func(context.Context, common.Address) (*big.Int, error)
		want   string
	}{
		{"from the reason", nil, "4"},
		{"from the reader", func(context.Context, common.Address) (*big.Int, error) { return big.NewInt(9), nil }, "9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, addr := newKey(t)
			done := make(chan struct{})
			var q1, q2 map[string]any
			g := newFakeGW(t, []common.Address{addr}, func(g *gwConn, m common.Address, n int) {
				g.send(rfqFrame("r1"))
				q1 = g.recv()
				g.send(map[string]any{"type": "rejected", "rfqId": "r1", "code": "stale_epoch",
					"reason": "quote epoch 0, settler epoch 4"})
				g.send(rfqFrame("r2"))
				q2 = g.recv()
				close(done)
				g.waitClosed()
			})
			if err := drive(t, clientFor(t, g, key, fixed(1_000_000), func(c *Config) { c.EpochReader = tc.reader }), done); err != nil {
				t.Fatal(err)
			}
			if q1["quote"].(map[string]any)["epoch"] != "0" || q2["quote"].(map[string]any)["epoch"] != tc.want {
				t.Fatalf("epochs %v %v", q1["quote"].(map[string]any)["epoch"], q2["quote"].(map[string]any)["epoch"])
			}
		})
	}
}

func TestReconnectsAndKeepsPauseAcrossConnections(t *testing.T) {
	key, addr := newKey(t)
	done := make(chan struct{})
	var first, second, q map[string]any
	g := newFakeGW(t, []common.Address{addr}, func(g *gwConn, m common.Address, n int) {
		if n == 1 {
			first = g.recv() // the pause the test sends once logged in
			g.send(map[string]any{"type": "paused"})
			return // the gateway restarts: the deferred close drops the socket
		}
		second = g.recv() // re-sent on the new connection
		g.send(map[string]any{"type": "paused"})
		g.send(rfqFrame("r1"))
		q = g.recv()
		close(done)
		g.waitClosed()
	})
	c := clientFor(t, g, key, fixed(1_000_000))
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- c.Run(ctx) }()
	until(t, func() bool { _, ok := c.Session(); return ok })
	if err := c.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}
	cancel()
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	if first["type"] != "pause" || second["type"] != "pause" || g.count() != 2 || q["type"] != "quote" {
		t.Fatalf("first %v second %v connections %d q %v", first, second, g.count(), q)
	}
}

func TestAnswersTheGatewaysPing(t *testing.T) {
	key, addr := newKey(t)
	done := make(chan struct{})
	var pingErr error
	var latency time.Duration
	g := newFakeGW(t, []common.Address{addr}, func(g *gwConn, m common.Address, n int) {
		ctx := g.c.CloseRead(g.ctx) // a reader must run for the pong to arrive
		pctx, cancel := context.WithTimeout(ctx, PongWithin)
		start := time.Now()
		pingErr = g.c.Ping(pctx)
		latency = time.Since(start)
		cancel()
		close(done)
		<-ctx.Done()
	})
	if err := drive(t, clientFor(t, g, key, fixed(1)), done); err != nil {
		t.Fatal(err)
	}
	if pingErr != nil || latency > PongWithin {
		t.Fatalf("ping %v after %s", pingErr, latency)
	}
}

func TestReconnectBackoffAfterADroppedDial(t *testing.T) {
	key, _ := newKey(t)
	c, err := NewClient(Config{URL: "ws://127.0.0.1:1/v1/maker/ws", Signer: key, Pricer: fixed(1),
		BackoffInitial: 20 * time.Millisecond, BackoffMax: 40 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := c.Run(ctx); err != nil { // keeps retrying a dead gateway until ctx ends
		t.Fatalf("Run returned %v", err)
	}
}

func TestCheckQuoteMirrorsTheGatewaysStaticChecks(t *testing.T) {
	key, addr := newKey(t)
	now := time.Unix(1_800_000_000, 0)
	c, _ := NewClient(Config{URL: "ws://x", Signer: key, Pricer: fixed(1), Now: func() time.Time { return now }})
	c.session = &Session{Maker: addr, Signer: addr, ChainID: fgChain, Settler: fgSettler, Epoch: big.NewInt(2)}
	f, err := ParseFrame(mustJSON(t, rfqFrame("r1")))
	if err != nil {
		t.Fatal(err)
	}
	r := f.(RFQ)
	r.MinValidUntil = uint64(now.Unix()) + 17
	good, err := c.BuildQuote(r, big.NewInt(10))
	if err != nil || c.CheckQuote(r, good) != nil {
		t.Fatalf("good quote refused: %v %v", err, c.CheckQuote(r, good))
	}
	if good.Epoch.Int64() != 2 || good.ValidUntil != uint64(now.Unix())+60 || good.MinFill.Cmp(r.Amount.Int) != 0 {
		t.Fatalf("built %+v", good)
	}
	long, _ := c.BuildQuote(r, big.NewInt(10), ValidFor(10*time.Minute))
	if long.ValidUntil != uint64(now.Add(LifeCap).Unix()) {
		t.Fatalf("life not capped: %d", long.ValidUntil)
	}
	for name, mut := range map[string]func(*Quote){
		"maker":        func(q *Quote) { q.Maker = common.HexToAddress("0x1") },
		"signer":       func(q *Quote) { q.Signer = common.HexToAddress("0x1") },
		"taker":        func(q *Quote) { q.Taker = common.HexToAddress("0x1") },
		"side":         func(q *Quote) { q.Side = 1 },
		"amount":       func(q *Quote) { q.Amount = big.NewInt(1) },
		"minFill":      func(q *Quote) { q.MinFill = big.NewInt(1) },
		"strike":       func(q *Quote) { q.Series.Strike = big.NewInt(1) },
		"physical":     func(q *Quote) { q.Series.Physical = true },
		"premium":      func(q *Quote) { q.Premium = new(big.Int) },
		"short life":   func(q *Quote) { q.ValidUntil = r.MinValidUntil - 1 },
		"long life":    func(q *Quote) { q.ValidUntil = uint64(now.Unix()) + 121 },
		"wide nonce":   func(q *Quote) { q.Nonce = new(big.Int).Lsh(big.NewInt(1), 256) },
		"wide premium": func(q *Quote) { q.Premium = new(big.Int).Lsh(big.NewInt(1), 256) },
	} {
		q := good
		q.Series.Strike = new(big.Int).Set(good.Series.Strike)
		mut(&q)
		var ve *ValidationError
		if err := c.CheckQuote(r, q); !errors.As(err, &ve) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
