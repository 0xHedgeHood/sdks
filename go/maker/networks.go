package maker

import "github.com/ethereum/go-ethereum/common"

// Underlying is one listed stock token.
type Underlying struct {
	Token  common.Address // the stock token (18 decimals)
	Oracle common.Address // the StockSettlementSource listed on the settler for (token, quote token)
}

// Network is one deployment.
type Network struct {
	Name        string
	ChainID     uint64
	Settler     common.Address // RfqSettler (UUPS proxy)
	Core        common.Address // OptionsCore (UUPS proxy)
	QuoteToken  common.Address // USDG-like stable, 6 decimals
	GatewayWS   string
	RPC         string
	Underlyings map[string]Underlying
}

// Testnet is Robinhood Chain testnet. TESTNET ONLY: the options core contracts are not audited yet; mainnet addresses
// are TBD. Fee parameters are read from the settler and arrive in the gateway's ready frame (at the time of writing
// feeBps = 1250 and notionalFeeBps = 100).
var Testnet = Network{
	Name:       "robinhood-testnet",
	ChainID:    46630,
	Settler:    common.HexToAddress("0xda43b0933031e41D5B367E1807536FcF4ebdEE7e"),
	Core:       common.HexToAddress("0xde5ce903254E9597822D5456a82a25640285B8B4"),
	QuoteToken: common.HexToAddress("0x850DE815d41F6983Aaf09247f9a465c0aefff04C"), // TUSD, testnet USDG stand-in
	GatewayWS:  "wss://testnet-rfq.hedgehood.io/v1/maker/ws",
	RPC:        "https://rpc.testnet.chain.robinhood.com",
	Underlyings: map[string]Underlying{
		"TSLA": {
			Token:  common.HexToAddress("0x6661d20C2e343Bb78B42535eB060C3058c744f3F"),
			Oracle: common.HexToAddress("0x9e6C529CAA69d44b63df27483c57f97a42D6e898"),
		},
	},
}

// Networks by short name.
var Networks = map[string]Network{"testnet": Testnet}
