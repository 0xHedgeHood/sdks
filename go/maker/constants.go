// Package maker is a market maker's client for the Hedgehood options RFQ gateway: the RfqSettler quote and its EIP-712
// digest, the gateway's WebSocket protocol (login, requests, signed firm quotes, outcome frames), the settler's fee
// math, nonce management, unsigned calldata for the maker's on-chain setup and read-only chain helpers.
//
// Testnet only (Robinhood Chain testnet, chainId 46630): the options core contracts are not yet audited and mainnet
// addresses are TBD. Nothing in this package signs or sends a transaction; it signs quotes and the gateway login only.
package maker

import "time"

// Quote sides (IRfqSettler.Side).
const (
	// MakerBuys: the taker writes (covered call / cash-secured put); the maker pays the premium and gets the long.
	MakerBuys uint8 = 0
	// MakerSells: the maker delivers a long it holds; the taker pays the premium (buy, or buy-to-close).
	MakerSells uint8 = 1
)

// RfqSettler constants.
const (
	MaxQuoteLife   = 120 * time.Second       // RfqSettler.MAX_QUOTE_LIFE: validUntil <= block.timestamp + 120 s
	WithdrawDelay  = 5 * time.Minute         // RfqSettler.WITHDRAW_DELAY
	WithdrawWindow = 24 * time.Hour          // RfqSettler.WITHDRAW_WINDOW
	BPS            = 10_000                  // basis points
	DomainName     = "Hedgehood RFQ Settler" // EIP-712 domain name of the settler
	DomainVersion  = "2"                     // EIP-712 domain version of the settler
	LoginPrefix    = "hedgehood-rfq-login"   // the gateway login message prefix
)

// Gateway defaults (cmd/rfq-gateway; an operator may configure others).
const (
	AuctionWindow    = 2 * time.Second  // respondBy = request time + 2 s
	MinQuoteLifeLeft = 15 * time.Second // a winning quote has at least 15 s left when returned to the taker
	LoginTimeout     = 10 * time.Second // to answer the challenge
	PingEvery        = 10 * time.Second // the gateway pings every 10 s ...
	PongWithin       = 5 * time.Second  // ... and drops a maker that does not pong within 5 s
	MaxFrame         = 16 << 10         // the gateway reads at most 16 KiB per frame
	BreakerMaxSilent = 20               // unanswered requests in a row before a pause
	BreakerMaxFades  = 3                // fades within the fade window before a pause
	BreakerCooldown  = 15 * time.Minute // pause length
)

// LifeCap is the longest quote life the client signs: MaxQuoteLife minus a few seconds for clock skew between the
// maker, the gateway and the chain.
const LifeCap = MaxQuoteLife - 5*time.Second

// Version of this module.
const Version = "0.1.0"
