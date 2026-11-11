package maker

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"
)

// Opt-in, read-only checks against the deployed testnet settler (eth_call only; nothing is signed or sent):
//
//	HEDGEHOOD_LIVE_RPC=https://rpc.testnet.chain.robinhood.com go test ./maker -run Live -v

func liveReader(t *testing.T) *Reader {
	rpc := os.Getenv("HEDGEHOOD_LIVE_RPC")
	if rpc == "" {
		t.Skip("set HEDGEHOOD_LIVE_RPC to run read-only testnet checks")
	}
	return NewReader(Testnet, rpc)
}

func TestLiveTestnetSettlerComputesOurDigest(t *testing.T) {
	r := liveReader(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := r.ChainID(ctx)
	if err != nil || id != Testnet.ChainID {
		t.Fatalf("chain id %d %v", id, err)
	}
	sep, err := r.DomainSeparator(ctx)
	if err != nil || sep != DomainSeparator(Testnet.ChainID, Testnet.Settler) || sep.Hex() != gTestnetSep {
		t.Fatalf("DOMAIN_SEPARATOR %s %v", sep.Hex(), err)
	}
	for _, q := range []Quote{goldenQuote(), func() Quote {
		q := goldenQuote()
		q.Nonce, q.Epoch, q.MinFill, q.Side = big.NewInt(12345), big.NewInt(7), q.Amount, MakerSells
		q.ExternalAccount, q.Series.IsPut, q.Series.Physical = q.Taker, true, true
		return q
	}()} {
		got, err := r.QuoteDigest(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if want := Digest(q, Testnet.ChainID, Testnet.Settler); got != want {
			t.Fatalf("settler.quoteDigest %s, ours %s", got.Hex(), want.Hex())
		}
	}
}

func TestLiveTestnetFeeParametersAreReadable(t *testing.T) {
	r := liveReader(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fee, notional, err := r.Fees(ctx)
	if err != nil || fee > BPS || notional > BPS {
		t.Fatalf("fees %d %d %v", fee, notional, err)
	}
	t.Logf("feeBps %d notionalFeeBps %d", fee, notional)
}
