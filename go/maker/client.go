package maker

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"math/rand/v2"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// The maker's side of the gateway WebSocket (wss://<gateway>/v1/maker/ws):
//
//	c, err := maker.NewClient(maker.Config{URL: maker.Testnet.GatewayWS, Signer: key, Pricer: price,
//		ChainID: maker.Testnet.ChainID, Settler: maker.Testnet.Settler})
//	err = c.Run(ctx)
//
// The client logs in (EIP-191 challenge signed by the signer key), calls the pricer for every request under the
// request's deadline, builds and signs the firm EIP-712 quote (fresh nonce, current epoch, minFill = amount), checks
// it the way the gateway will, and reconnects with backoff when the socket drops. The gateway's pings are answered
// while the client reads, which it always does.

// Answer is a pricer's reply to one request: a premium (quote raw for the whole amount, fee excluded), a full quote
// (from Client.BuildQuote) sent as is, or a decline reason. The zero Answer declines with "no_price".
type Answer struct {
	Premium *big.Int
	Quote   *Quote
	Decline string
}

// PriceAt answers with a premium.
func PriceAt(premium *big.Int) Answer { return Answer{Premium: premium} }

// SendQuote answers with a quote built by Client.BuildQuote.
func SendQuote(q Quote) Answer { return Answer{Quote: &q} }

// DeclineWith declines with a short reason code the gateway records (e.g. "no_price", "no_inventory").
func DeclineWith(reason string) Answer { return Answer{Decline: reason} }

// Pricer prices one request. ctx expires at respondBy minus the answer margin (150 ms by default); a pricer that has
// not returned by then is answered with a "timeout" decline. An error or a panic declines with "maker_error".
// Declining is cheap and keeps you off the silence breaker (20 unanswered requests in a row pause a maker).
type Pricer func(ctx context.Context, r RFQ) (Answer, error)

// Session is what the gateway told the client at login.
type Session struct {
	Maker          common.Address
	Signer         common.Address
	ChainID        uint64
	Settler        common.Address
	Epoch          *big.Int
	FeeBps         uint64
	NotionalFeeBps uint64
}

// LoginRefusedError is a login the gateway refused (not allowlisted, signer not delegated, bad signature). Run does
// not retry it.
type LoginRefusedError struct{ Code, Reason string }

func (e *LoginRefusedError) Error() string { return "login refused: " + e.Code + ": " + e.Reason }

// ProtocolError is a gateway the client cannot follow, or one on another chain or settler than pinned. Run does not
// retry it.
type ProtocolError struct{ Msg string }

func (e *ProtocolError) Error() string { return "protocol: " + e.Msg }

// Config configures a Client.
type Config struct {
	URL    string            // the gateway maker socket
	Signer *ecdsa.PrivateKey // the maker's key, or a hot key the maker delegated with setSigner
	Pricer Pricer

	Maker           common.Address // the maker account (zero = the signer's address)
	ExternalAccount common.Address // externalAccount on every built quote (zero = the maker's settler balance)
	QuoteLife       time.Duration  // validity of built quotes (0 = 60 s; capped at LifeCap = 115 s)

	ChainID uint64         // pin the chain (0 = accept the gateway's); a gateway on another chain is refused
	Settler common.Address // pin the settler (zero = accept the gateway's)

	OnEvent     func(Frame)                                                       // every frame after login but requests, from the read loop
	EpochReader func(ctx context.Context, maker common.Address) (*big.Int, error) // e.g. Reader.Epoch, after stale_epoch
	Nonces      *NonceManager                                                     // nil = a new one at a random slot

	AnswerMargin   time.Duration // kept before respondBy (0 = 150 ms)
	BackoffInitial time.Duration // reconnect delay (0 = 1 s), doubled per failed attempt
	BackoffMax     time.Duration // (0 = 30 s)
	DialTimeout    time.Duration // (0 = 10 s)
	Now            func() time.Time
	Logger         *slog.Logger
}

type sentQuote struct {
	digest common.Hash
	quote  Quote
}

// Client is one maker's gateway connection. Safe for concurrent use.
type Client struct {
	cfg    Config
	signer common.Address
	maker  common.Address
	log    *slog.Logger

	mu         sync.Mutex
	session    *Session
	conn       *websocket.Conn
	wantPaused bool
	sent       map[string]sentQuote
	sentOrder  []string
}

const keepSent = 1024

// NewClient validates the config and applies its defaults.
func NewClient(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("config: URL is required")
	}
	if cfg.Signer == nil {
		return nil, errors.New("config: Signer is required")
	}
	if cfg.Pricer == nil {
		return nil, errors.New("config: Pricer is required")
	}
	if cfg.QuoteLife < 0 {
		return nil, errors.New("config: QuoteLife must be positive")
	}
	if cfg.QuoteLife == 0 {
		cfg.QuoteLife = 60 * time.Second
	}
	cfg.QuoteLife = min(cfg.QuoteLife, LifeCap)
	if cfg.AnswerMargin == 0 {
		cfg.AnswerMargin = 150 * time.Millisecond
	}
	if cfg.BackoffInitial <= 0 {
		cfg.BackoffInitial = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 30 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Nonces == nil {
		cfg.Nonces = NewNonceManager()
	}
	c := &Client{cfg: cfg, signer: crypto.PubkeyToAddress(cfg.Signer.PublicKey), log: cfg.Logger,
		sent: map[string]sentQuote{}}
	if c.log == nil {
		c.log = slog.Default()
	}
	c.maker = cfg.Maker
	if c.maker == (common.Address{}) {
		c.maker = c.signer
	}
	return c, nil
}

// Maker is the maker account the client quotes for.
func (c *Client) Maker() common.Address { return c.maker }

// SignerAddress is the address of the signing key.
func (c *Client) SignerAddress() common.Address { return c.signer }

// Session is a copy of the current login (false before the first login).
func (c *Client) Session() (Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return Session{}, false
	}
	s := *c.session
	s.Epoch = new(big.Int).Set(s.Epoch)
	return s, true
}

// SetEpoch makes future quotes sign under `epoch` (after your own increaseEpoch).
func (c *Client) SetEpoch(epoch *big.Int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return errors.New("not logged in")
	}
	c.session.Epoch = new(big.Int).Set(nz(epoch))
	return nil
}

// QuoteFor is the (digest, quote) this client sent for a request (the last 1024 are kept).
func (c *Client) QuoteFor(rfqID string) (common.Hash, Quote, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sent[rfqID]
	return s.digest, s.quote, ok
}

// ------------------------------------------------------------------ lifecycle

// Run connects, logs in and answers until ctx ends (then it returns nil), reconnecting with exponential backoff and
// jitter. A refused login returns a *LoginRefusedError, a gateway on another chain or settler than pinned a
// *ProtocolError.
func (c *Client) Run(ctx context.Context) error {
	delay := c.cfg.BackoffInitial
	for {
		if ctx.Err() != nil {
			return nil
		}
		loggedIn, err := c.RunOnce(ctx)
		var lr *LoginRefusedError
		var pe *ProtocolError
		if errors.As(err, &lr) || errors.As(err, &pe) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.log.Warn("gateway connection ended", "err", err)
		}
		if loggedIn {
			delay = c.cfg.BackoffInitial
		}
		wait := time.Duration(float64(delay) * (0.8 + 0.4*rand.Float64()))
		c.log.Info("reconnecting", "in", wait.Round(time.Millisecond))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		delay = min(delay*2, c.cfg.BackoffMax)
	}
}

// RunOnce is one connection: login, then answer until the socket closes or ctx ends. It reports whether the login
// succeeded.
func (c *Client) RunOnce(ctx context.Context) (loggedIn bool, err error) {
	dctx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
	conn, _, err := websocket.Dial(dctx, c.cfg.URL, &websocket.DialOptions{
		HTTPHeader: http.Header{"User-Agent": {"hedgehood-maker-go/" + Version}}})
	cancel()
	if err != nil {
		return false, err
	}
	conn.SetReadLimit(1 << 20)
	cctx, ccancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		ccancel()
		wg.Wait()
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	if err := c.login(cctx, conn); err != nil {
		return false, err
	}
	c.mu.Lock()
	c.conn = conn
	paused := c.wantPaused
	c.mu.Unlock()
	if paused {
		if err := c.write(cctx, conn, map[string]string{"type": "pause"}); err != nil {
			return true, err
		}
	}
	for {
		_, data, err := conn.Read(cctx)
		if err != nil {
			if ctx.Err() != nil {
				return true, nil
			}
			return true, err
		}
		c.dispatch(cctx, conn, data, &wg)
	}
}

// Pause stops requests (kept across reconnects until Resume). The gateway answers with a Paused frame.
func (c *Client) Pause(ctx context.Context) error { return c.setPaused(ctx, true) }

// Resume restarts requests. The gateway answers with a Resumed frame.
func (c *Client) Resume(ctx context.Context) error { return c.setPaused(ctx, false) }

func (c *Client) setPaused(ctx context.Context, p bool) error {
	c.mu.Lock()
	c.wantPaused = p
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil // sent after the next login
	}
	t := "resume"
	if p {
		t = "pause"
	}
	return c.write(ctx, conn, map[string]string{"type": t})
}

// ------------------------------------------------------------------ login

func (c *Client) read(ctx context.Context, conn *websocket.Conn, within time.Duration) (Frame, error) {
	rctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	_, b, err := conn.Read(rctx)
	if err != nil {
		return nil, err
	}
	f, err := ParseFrame(b)
	if err != nil {
		return nil, &ProtocolError{err.Error()}
	}
	return f, nil
}

func (c *Client) login(ctx context.Context, conn *websocket.Conn) error {
	f, err := c.read(ctx, conn, LoginTimeout+5*time.Second)
	if err != nil {
		return err
	}
	ch, ok := f.(Challenge)
	if !ok {
		return &ProtocolError{fmt.Sprintf("expected a challenge, got %s", f.Raw())}
	}
	if c.cfg.ChainID != 0 && ch.ChainID != c.cfg.ChainID {
		return &ProtocolError{fmt.Sprintf("gateway is on chain %d, expected %d", ch.ChainID, c.cfg.ChainID)}
	}
	sig, err := SignLogin(ch.ChainID, c.maker, ch.Nonce, c.cfg.Signer)
	if err != nil {
		return err
	}
	if err := c.write(ctx, conn, LoginFrame{"login", c.maker, c.signer, sig}); err != nil {
		return err
	}
	f, err = c.read(ctx, conn, LoginTimeout+5*time.Second)
	if err != nil {
		return err
	}
	if e, ok := f.(ErrorFrame); ok {
		return &LoginRefusedError{e.Code, e.Reason}
	}
	r, ok := f.(Ready)
	if !ok {
		return &ProtocolError{fmt.Sprintf("expected ready, got %s", f.Raw())}
	}
	if r.Maker != c.maker {
		return &ProtocolError{fmt.Sprintf("gateway logged us in as %s, expected %s", r.Maker.Hex(), c.maker.Hex())}
	}
	if c.cfg.Settler != (common.Address{}) && r.Settler != c.cfg.Settler {
		return &ProtocolError{fmt.Sprintf("gateway settler %s, expected %s", r.Settler.Hex(), c.cfg.Settler.Hex())}
	}
	c.mu.Lock()
	c.session = &Session{Maker: c.maker, Signer: c.signer, ChainID: ch.ChainID, Settler: r.Settler,
		Epoch: new(big.Int).Set(r.Epoch.Int), FeeBps: r.FeeBps, NotionalFeeBps: r.NotionalFeeBps}
	c.mu.Unlock()
	c.log.Info("logged in", "maker", c.maker, "signer", c.signer, "settler", r.Settler, "epoch", r.Epoch.Int,
		"feeBps", r.FeeBps, "notionalFeeBps", r.NotionalFeeBps)
	c.emit(r)
	return nil
}

// ------------------------------------------------------------------ frames

var epochInReason = regexp.MustCompile(`settler epoch (\d+)`)

func (c *Client) dispatch(ctx context.Context, conn *websocket.Conn, data []byte, wg *sync.WaitGroup) {
	f, err := ParseFrame(data)
	if err != nil {
		c.log.Warn("bad frame from gateway", "err", err)
		var head struct {
			Type string `json:"type"`
			ID   string `json:"rfqId"`
		}
		if json.Unmarshal(data, &head) == nil && head.Type == "rfq" && head.ID != "" {
			_ = c.write(ctx, conn, NewDecline(head.ID, "invalid_rfq"))
		}
		return
	}
	switch v := f.(type) {
	case RFQ:
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.write(ctx, conn, c.Respond(ctx, v)); err != nil && ctx.Err() == nil {
				c.log.Warn("answer not sent", "rfq", v.ID, "err", err)
			}
		}()
		return
	case Rejected:
		c.log.Info("rejected", "rfq", v.RFQID, "code", v.Code, "reason", v.Reason)
		if v.Code == "stale_epoch" {
			c.refreshEpoch(ctx, v)
		}
	case Won:
		c.log.Info("won", "rfq", v.RFQID, "digest", v.Digest)
	}
	c.emit(f)
}

func (c *Client) write(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return fmt.Errorf("frame of %d bytes exceeds the gateway's %d", len(b), MaxFrame)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

func (c *Client) emit(f Frame) {
	if c.cfg.OnEvent == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil { // a handler bug must not drop the connection
			c.log.Error("OnEvent handler panicked", "panic", r)
		}
	}()
	c.cfg.OnEvent(f)
}

func (c *Client) refreshEpoch(ctx context.Context, rej Rejected) {
	s, ok := c.Session()
	if !ok {
		return
	}
	var epoch *big.Int
	if c.cfg.EpochReader != nil {
		e, err := c.cfg.EpochReader(ctx, c.maker)
		if err != nil {
			c.log.Warn("EpochReader failed", "err", err)
		} else {
			epoch = e
		}
	}
	if epoch == nil {
		if m := epochInReason.FindStringSubmatch(rej.Reason); m != nil {
			epoch, _ = new(big.Int).SetString(m[1], 10)
		} else {
			epoch = new(big.Int).Add(s.Epoch, big.NewInt(1))
		}
	}
	c.log.Info("epoch moved after stale_epoch", "from", s.Epoch, "to", epoch)
	_ = c.SetEpoch(epoch)
}

// ------------------------------------------------------------------ quoting

type priced struct {
	ans Answer
	err error
}

// Respond is the frame answering r: a QuoteFrame or a DeclineFrame. It never fails.
func (c *Client) Respond(ctx context.Context, r RFQ) any {
	s, ok := c.Session()
	if !ok {
		return NewDecline(r.ID, "not_ready")
	}
	if r.ChainID != s.ChainID || r.Settler != s.Settler {
		return NewDecline(r.ID, "wrong_settler")
	}
	respondBy := time.UnixMilli(r.RespondBy)
	budget := respondBy.Sub(c.cfg.Now()) - c.cfg.AnswerMargin
	if budget <= 0 {
		return NewDecline(r.ID, "late")
	}
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	out := make(chan priced, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				out <- priced{err: fmt.Errorf("pricer panicked: %v", p)}
			}
		}()
		a, err := c.cfg.Pricer(pctx, r)
		out <- priced{a, err}
	}()
	var res priced
	select {
	case res = <-out:
	case <-pctx.Done():
		c.log.Warn("pricer timed out", "rfq", r.ID)
		return NewDecline(r.ID, "timeout")
	}
	if res.err != nil {
		c.log.Error("pricer failed", "rfq", r.ID, "err", res.err)
		return NewDecline(r.ID, "maker_error")
	}
	a := res.ans
	var q Quote
	switch {
	case a.Decline != "":
		return NewDecline(r.ID, a.Decline)
	case a.Quote != nil:
		q = *a.Quote
	case a.Premium == nil || a.Premium.Sign() <= 0:
		return NewDecline(r.ID, "no_price")
	default:
		var err error
		if q, err = c.BuildQuote(r, a.Premium); err != nil {
			c.log.Error("cannot build a quote", "rfq", r.ID, "err", err)
			return NewDecline(r.ID, "maker_error")
		}
	}
	if err := c.CheckQuote(r, q); err != nil {
		c.log.Error("not sending an invalid quote", "rfq", r.ID, "err", err)
		return NewDecline(r.ID, "maker_error")
	}
	if c.cfg.Now().After(respondBy) {
		return NewDecline(r.ID, "late") // never hand out a firm quote the auction will not read
	}
	digest := Digest(q, s.ChainID, s.Settler)
	sig, err := SignDigest(digest, c.cfg.Signer)
	if err != nil {
		c.log.Error("signing failed", "rfq", r.ID, "err", err)
		return NewDecline(r.ID, "maker_error")
	}
	c.remember(r.ID, digest, q)
	return QuoteFrame{"quote", r.ID, q, hexutil.Encode(sig)}
}

func (c *Client) remember(id string, d common.Hash, q Quote) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.sent[id]; !ok {
		c.sentOrder = append(c.sentOrder, id)
	}
	c.sent[id] = sentQuote{d, q}
	for len(c.sentOrder) > keepSent {
		delete(c.sent, c.sentOrder[0])
		c.sentOrder = c.sentOrder[1:]
	}
}

// QuoteOption changes a quote BuildQuote makes.
type QuoteOption func(*quoteOpts)

type quoteOpts struct {
	life     time.Duration
	external *common.Address
	nonce    *big.Int
}

// ValidFor sets the quote's life (capped at LifeCap; validUntil is never below the request's minValidUntil).
func ValidFor(d time.Duration) QuoteOption { return func(o *quoteOpts) { o.life = d } }

// WithExternalAccount sets externalAccount (zero = the maker's settler balance).
func WithExternalAccount(a common.Address) QuoteOption { return func(o *quoteOpts) { o.external = &a } }

// WithNonce uses a chosen nonce instead of the next fresh one (quotes sharing a nonce exclude each other).
func WithNonce(n *big.Int) QuoteOption { return func(o *quoteOpts) { o.nonce = new(big.Int).Set(n) } }

// BuildQuote is the firm, unsigned quote for r at `premium` (quote raw for the whole amount, fee excluded): this
// maker and signer, the request's terms, minFill = amount, a fresh nonce and the session's epoch.
func (c *Client) BuildQuote(r RFQ, premium *big.Int, opts ...QuoteOption) (Quote, error) {
	s, ok := c.Session()
	if !ok {
		return Quote{}, errors.New("not logged in")
	}
	o := quoteOpts{life: c.cfg.QuoteLife}
	for _, f := range opts {
		f(&o)
	}
	if o.life <= 0 {
		o.life = c.cfg.QuoteLife
	}
	o.life = min(o.life, LifeCap)
	ext := c.cfg.ExternalAccount
	if o.external != nil {
		ext = *o.external
	}
	nonce := o.nonce
	if nonce == nil {
		nonce = c.cfg.Nonces.Next()
	}
	validUntil := max(uint64(c.cfg.Now().Add(o.life).Unix()), r.MinValidUntil)
	amount := nz(r.Amount.Int)
	return Quote{Maker: c.maker, Signer: c.signer, Taker: r.Taker, ExternalAccount: ext, Series: r.Series,
		Side: r.Side, Amount: new(big.Int).Set(amount), MinFill: new(big.Int).Set(amount),
		Premium: new(big.Int).Set(nz(premium)), ValidUntil: validUntil, Nonce: nonce, Epoch: s.Epoch}, nil
}

// CheckQuote runs the gateway's static checks (auction.Static, docs/RFQ_GATEWAY.md section 7) so a bad quote never
// leaves the process: the logged-in maker and this signer, the request's taker, side, amount and series, minFill =
// amount, a positive premium, and validUntil within [minValidUntil, now + 120 s].
func (c *Client) CheckQuote(r RFQ, q Quote) error {
	if q.Maker != c.maker {
		return invalid("quote maker %s is not the logged-in maker %s", q.Maker.Hex(), c.maker.Hex())
	}
	if q.Signer != c.signer {
		return invalid("quote signer %s is not this client's key %s", q.Signer.Hex(), c.signer.Hex())
	}
	if q.Side != MakerBuys && q.Side != MakerSells {
		return invalid("side must be 0 or 1")
	}
	if q.Taker != r.Taker || q.Side != r.Side || !eq(q.Amount, r.Amount.Int) || !q.Series.Equal(r.Series) {
		return invalid("taker, side, amount and series must equal the request")
	}
	if !eq(q.MinFill, r.Amount.Int) {
		return invalid("minFill must equal the amount")
	}
	if q.Premium == nil || q.Premium.Sign() <= 0 {
		return invalid("premium must be positive")
	}
	if q.Premium.BitLen() > 256 || nz(q.Nonce).BitLen() > 256 || nz(q.Epoch).BitLen() > 256 || nz(q.Nonce).Sign() < 0 ||
		nz(q.Epoch).Sign() < 0 {
		return invalid("premium, nonce and epoch must be uint256")
	}
	latest := uint64(c.cfg.Now().Add(MaxQuoteLife).Unix())
	if q.ValidUntil < r.MinValidUntil || q.ValidUntil > latest {
		return invalid("validUntil %d outside [%d, %d]", q.ValidUntil, r.MinValidUntil, latest)
	}
	return nil
}
