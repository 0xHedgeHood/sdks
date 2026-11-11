// Command simple-maker is a minimal maker built on package maker (the library, not the CLI): it buys every listed
// TESTNET option at a fixed share of the notional and, with -sell, sells back longs it holds to users closing early.
// A starting point, not a pricing model: no spot feed, no hedging, no limits beyond -max-amount.
//
//	export HEDGEHOOD_SIGNER_KEY=0x...   # the maker key, or a hot key the maker delegated with setSigner
//	go run ./examples/simple-maker -bps 40
//	go run ./examples/simple-maker -maker 0xYourMaker -bps 40 -sell -external self
//
// Testnet only. The key is read from the environment and never printed.
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0xHedgeHood/sdks/go/maker"
)

func main() {
	makerFlag := flag.String("maker", "", "maker address when the key is a delegated hot signer")
	bps := flag.Int64("bps", 40, "premium in basis points of the notional (amount x strike), fee excluded")
	edge := flag.Int64("edge", 200, "basis points above the buy price to sell a long back (-sell)")
	sell := flag.Bool("sell", false, "also quote early closes, for series this maker holds")
	external := flag.String("external", "", `pay premiums from a wallet by allowance: an address, or "self"`)
	maxAmount := flag.Float64("max-amount", 10, "largest request quoted, in whole tokens")
	flag.Parse()

	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(os.Getenv("HEDGEHOOD_SIGNER_KEY")), "0x"))
	if err != nil {
		log.Fatal("set HEDGEHOOD_SIGNER_KEY to the signer's hex private key")
	}
	limit, _ := new(big.Float).Mul(big.NewFloat(*maxAmount), new(big.Float).SetInt(maker.Share)).Int(nil)
	reader := maker.NewReader(maker.Testnet, "")
	var c *maker.Client

	price := func(ctx context.Context, r maker.RFQ) (maker.Answer, error) {
		if r.Amount.Cmp(limit) > 0 {
			return maker.DeclineWith("too_large"), nil
		}
		p := maker.Notional(r.Amount.Int, r.Series.Strike, nil) // USDG raw
		if r.MakerBuys() {
			p.Mul(p, big.NewInt(*bps)).Quo(p, big.NewInt(maker.BPS))
			return maker.PriceAt(p), nil
		}
		// a close: sell back only a long we hold; the gateway would reject anything else as `inventory`
		if !*sell {
			return maker.DeclineWith("no_inventory"), nil
		}
		why, err := reader.CanSell(ctx, c.Maker(), r.Series, r.Amount.Int)
		if err != nil {
			return maker.DeclineWith("unavailable"), nil
		}
		if why != "" {
			return maker.DeclineWith("no_inventory"), nil
		}
		p.Mul(p, big.NewInt(*bps+*edge)).Quo(p, big.NewInt(maker.BPS))
		return maker.PriceAt(p), nil
	}

	cfg := maker.Config{
		URL: maker.Testnet.GatewayWS, Signer: key, Pricer: price,
		ChainID: maker.Testnet.ChainID, Settler: maker.Testnet.Settler, // refuse any other deployment
		EpochReader: reader.Epoch,
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
		OnEvent:     func(f maker.Frame) { log.Printf("%s %s", f.Type(), f.Raw()) },
	}
	cfg.Maker = crypto.PubkeyToAddress(key.PublicKey)
	if *makerFlag != "" {
		cfg.Maker = common.HexToAddress(*makerFlag)
	}
	switch *external {
	case "":
	case "self":
		cfg.ExternalAccount = cfg.Maker
	default:
		cfg.ExternalAccount = common.HexToAddress(*external)
	}
	if c, err = maker.NewClient(cfg); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("maker %s on %s (testnet only)", c.Maker().Hex(), maker.Testnet.GatewayWS)
	if err := c.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
