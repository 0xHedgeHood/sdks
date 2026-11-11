package maker_test

import (
	"context"
	"crypto/ecdsa"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0xHedgeHood/sdks/go/maker"
)

// A maker that buys at 40 bps of the notional and declines everything else (the README's example).
func ExampleNewClient() {
	var key *ecdsa.PrivateKey    // the maker's key or its delegated hot key, from your own key store
	var makerAddr common.Address // zero when the key is the maker itself
	price := func(ctx context.Context, r maker.RFQ) (maker.Answer, error) {
		if !r.MakerBuys() {
			return maker.DeclineWith("no_inventory"), nil
		}
		p := maker.Notional(r.Amount.Int, r.Series.Strike, nil) // USDG raw
		p.Mul(p, big.NewInt(40)).Quo(p, big.NewInt(10_000))     // 40 bps, fee excluded
		return maker.PriceAt(p), nil
	}
	c, err := maker.NewClient(maker.Config{
		URL: maker.Testnet.GatewayWS, Signer: key, Maker: makerAddr, Pricer: price,
		ChainID: maker.Testnet.ChainID, Settler: maker.Testnet.Settler,
		OnEvent:     func(f maker.Frame) { log.Printf("%s %s", f.Type(), f.Raw()) },
		EpochReader: maker.NewReader(maker.Testnet, "").Epoch,
	})
	if err != nil {
		log.Print(err) // here: Signer is nil
		return
	}
	_ = c.Run(context.Background())
}
