package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0xHedgeHood/sdks/go/maker"
)

// demoPricer answers at a fixed premium per whole underlying token, or at a fixed bps of the notional
// (amount x strike), on the sides it is told to quote. A close (MakerSells) is quoted only when canSell finds the
// long (Reader.CanSell); without canSell closes are declined. A demo, not a model.
func demoPricer(perToken *big.Int, bps int64, sides string, maxAmount *big.Int,
	canSell func(context.Context, maker.RFQ) (string, error)) maker.Pricer {
	return func(ctx context.Context, r maker.RFQ) (maker.Answer, error) {
		switch {
		case sides == "buy" && !r.MakerBuys():
			return maker.DeclineWith("no_inventory"), nil
		case sides == "sell" && r.MakerBuys():
			return maker.DeclineWith("side_not_quoted"), nil
		case maxAmount != nil && maxAmount.Sign() > 0 && r.Amount.Cmp(maxAmount) > 0:
			return maker.DeclineWith("too_big"), nil
		}
		if !r.MakerBuys() {
			if canSell == nil {
				return maker.DeclineWith("no_inventory"), nil
			}
			why, err := canSell(ctx, r)
			if err != nil {
				return maker.DeclineWith("unavailable"), nil
			}
			if why != "" {
				return maker.DeclineWith("no_inventory"), nil
			}
		}
		var p *big.Int
		if perToken != nil {
			p = new(big.Int).Mul(perToken, r.Amount.Int)
			p.Quo(p, maker.Share)
		} else {
			p = maker.Notional(r.Amount.Int, r.Series.Strike, nil)
			p.Mul(p, big.NewInt(bps)).Quo(p, big.NewInt(maker.BPS))
		}
		return maker.PriceAt(p), nil // zero declines with no_price
	}
}

// inventoryCheck is Reader.CanSell for a close, read only from the deployment the request is for. The reader is the
// configured --network/--rpc/--settler/--core; with --no-pin the gateway may announce another chain or settler, and a
// balance read elsewhere would say nothing about this request. A request whose settler is not the reader's, or whose
// chain is not the RPC's (eth_chainId, read once), is an error: the pricer declines it as unavailable.
func inventoryCheck(reader *maker.Reader, makerOf func() common.Address,
	log *slog.Logger) func(context.Context, maker.RFQ) (string, error) {
	var mu sync.Mutex
	var rpcChain uint64
	return func(ctx context.Context, r maker.RFQ) (string, error) {
		if r.Settler != reader.Settler {
			err := fmt.Errorf("request settler %s is not the configured settler %s", r.Settler.Hex(), reader.Settler.Hex())
			log.Warn("close not checked", "rfq", r.ID, "err", err)
			return "", err
		}
		mu.Lock()
		id := rpcChain
		mu.Unlock()
		if id == 0 {
			got, err := reader.ChainID(ctx)
			if err != nil {
				return "", err
			}
			mu.Lock()
			rpcChain, id = got, got
			mu.Unlock()
		}
		if r.ChainID != id {
			err := fmt.Errorf("request is on chain %d, the RPC on chain %d", r.ChainID, id)
			log.Warn("close not checked", "rfq", r.ID, "err", err)
			return "", err
		}
		return reader.CanSell(ctx, makerOf(), r.Series, r.Amount.Int)
	}
}

func cmdConnect(e env, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	var n netFlags
	n.register(fs)
	gateway := fs.String("gateway", "", "gateway maker socket (default: the network's)")
	keyFile := fs.String("key-file", "", "file holding the signer's hex private key (the maker's, or a delegated hot key)")
	keyEnv := fs.String("key-env", "HEDGEHOOD_SIGNER_KEY", "environment variable holding the signer key, when no --key-file")
	makerFlag := fs.String("maker", "", "maker address when the key is a delegated hot signer (default: the key's address)")
	external := fs.String("external", "", `externalAccount: "" = the settler balance, "self" = the maker's wallet, or an address`)
	premium := fs.String("premium", "", "premium in USDG per whole underlying token (e.g. 1.25), fee excluded")
	bps := fs.Int64("bps", 0, "premium as basis points of the notional (amount x strike), fee excluded")
	sides := fs.String("sides", "buy", "sides to quote: buy (MakerBuys), sell (MakerSells, needs longs), both")
	maxAmount := fs.String("max-amount", "", "decline requests above this many underlying tokens")
	life := fs.Duration("life", 60*time.Second, "quote validity (capped at 115 s)")
	noPin := fs.Bool("no-pin", false, "accept whatever chain and settler the gateway announces (closes are still "+
		"quoted only on the configured --rpc chain and --settler)")
	duration := fs.Duration("duration", 0, "stop after this long (0 = until interrupted)")
	verbose := fs.Bool("v", false, "debug logging")
	if err := parse(fs, args, e.stderr); err != nil {
		return err
	}
	net, err := n.resolve()
	if err != nil {
		return err
	}
	var perToken *big.Int
	switch {
	case (*premium == "") == (*bps == 0):
		return errors.New("pass exactly one of --premium or --bps")
	case *premium != "":
		if perToken, err = amount(*premium, 6, false, false, "--premium"); err != nil {
			return err
		}
	case *bps < 0 || *bps > maker.BPS:
		return errors.New("--bps must be in [0, 10000]")
	}
	if *sides != "buy" && *sides != "sell" && *sides != "both" {
		return errors.New("--sides must be buy, sell or both")
	}
	var maxRaw *big.Int
	if *maxAmount != "" {
		if maxRaw, err = amount(*maxAmount, 18, false, false, "--max-amount"); err != nil {
			return err
		}
	}
	key, err := loadSigner(*keyFile, *keyEnv, e.getenv, e.stderr)
	if err != nil {
		return err
	}
	makerAddr, err := optAddr(*makerFlag, "--maker")
	if err != nil {
		return err
	}
	url := *gateway
	if url == "" {
		url = net.GatewayWS
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(e.stderr, &slog.HandlerOptions{Level: level}))
	var out sync.Mutex
	var c *maker.Client
	var canSell func(context.Context, maker.RFQ) (string, error)
	if *sides != "buy" { // closes: quote only what the maker holds (the gateway rejects the rest as inventory)
		canSell = inventoryCheck(maker.NewReader(net, ""), func() common.Address { return c.Maker() }, log)
	}
	cfg := maker.Config{URL: url, Signer: key, Maker: makerAddr, QuoteLife: *life,
		Pricer: demoPricer(perToken, *bps, *sides, maxRaw, canSell), Logger: log,
		OnEvent: func(f maker.Frame) { out.Lock(); printEvent(e.stdout, f); out.Unlock() }}
	if !*noPin {
		cfg.ChainID, cfg.Settler = net.ChainID, net.Settler
	}
	switch *external {
	case "":
	case "self":
		cfg.ExternalAccount = makerAddr
		if makerAddr == (common.Address{}) {
			cfg.ExternalAccount = crypto.PubkeyToAddress(key.PublicKey)
		}
	default:
		if cfg.ExternalAccount, err = addr(*external, "--external"); err != nil {
			return err
		}
	}
	c, err = maker.NewClient(cfg)
	if err != nil {
		return err
	}
	mode := fmt.Sprintf("%s USDG per token", *premium)
	if perToken == nil {
		mode = fmt.Sprintf("%d bps of the notional", *bps)
	}
	fmt.Fprintf(e.stdout, "# demo maker %s (signer %s), %s, sides %s, externalAccount %s\n# gateway %s -- TESTNET ONLY\n",
		c.Maker().Hex(), c.SignerAddress().Hex(), mode, *sides, cfg.ExternalAccount.Hex(), url)
	ctx := e.ctx
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	return c.Run(ctx)
}

// printEvent writes one gateway frame as a JSON line with the local time.
func printEvent(w io.Writer, f maker.Frame) {
	var body any = json.RawMessage(f.Raw())
	if !json.Valid(f.Raw()) {
		body = string(f.Raw())
	}
	b, _ := json.Marshal(map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "frame": body})
	fmt.Fprintln(w, string(b))
}
