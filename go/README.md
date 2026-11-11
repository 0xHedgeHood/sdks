# hedgehood-maker (Go)

Go client and CLI for market makers on the Hedgehood options RFQ gateway, mirroring the Python SDK in this
repository: sign firm EIP-712 quotes for the `RfqSettler`, answer the gateway's requests over its WebSocket, and build
the (unsigned) on-chain setup transactions.

> **Testnet only** (Robinhood Chain testnet, chainId 46630). The options core contracts are not yet audited; mainnet
> addresses are TBD. The protocol reference is [docs/MAKER_GUIDE.md](../docs/MAKER_GUIDE.md).

Module `github.com/0xHedgeHood/sdks/go`, Go 1.27.3+, dependencies `go-ethereum` and
`coder/websocket`. Package `maker`:

- `Client`: login, a `Pricer` per request whose context expires at `respondBy` − 150 ms, quote building and signing
  (fresh nonce, current epoch, `minFill = amount`), local checks matching the gateway's, events, reconnect with
  backoff, `Pause` / `Resume`, epoch refresh after `stale_epoch`.
- `Digest`, `SignQuote`, `TypedData`: byte-equal to `RfqSettler.quoteDigest` (tests pin the golden vectors of
  `tests/goldens.py`, shared with the Solidity, gateway and desk implementations).
- `Txs`: unsigned `{to, data}` for `setSigner`, `deposit`, `requestWithdraw`, `withdraw`, `increaseEpoch`,
  `invalidateNonces`, `linkMaker`, ERC-20 `approve`, `OptionsCore.setOperator` and `redeemLong`, each with a
  `cast send` line. Nothing in this module signs or sends a transaction.
- `Fee`, `PreviewFill`, `PremiumForNet`: the settler's fee and pro-rata math; `NonceManager`; `Reader` (read-only
  `eth_call`s: epoch, signer expiry, balances, allowances, fees, `quoteDigest`, `CanSell`).

## CLI

```
cd go && go build ./cmd/hedgehood-maker      # or: go install github.com/0xHedgeHood/sdks/go/cmd/hedgehood-maker@<ref>

# 1. onboarding: print the unsigned transactions, review them, send each yourself
hedgehood-maker tx setSigner --maker 0xYourMaker --signer 0xYourHotKey --days 7
hedgehood-maker tx approve   --owner 0xYourMaker --amount 1000
hedgehood-maker tx deposit   --from  0xYourMaker --amount 1000
hedgehood-maker state    --maker 0xYourMaker --signer 0xYourHotKey     # read-only
hedgehood-maker balances --maker 0xYourMaker                           # read-only

# 2. a demo maker: 40 bps of the notional on MakerBuys requests (or --premium 1.25 USDG per token)
HEDGEHOOD_SIGNER_KEY=... hedgehood-maker connect --maker 0xYourMaker --bps 40
hedgehood-maker connect --key-file ./hot.key --maker 0xYourMaker --premium 1.25 --sides both

# check a quote's digest and signer (and, with --check, the settler's own digest)
hedgehood-maker quote-digest --quote quote.json --check
```

The signer key is read from `--key-file` (a file holding the hex key; keep it `chmod 600`) or from the environment
variable named by `--key-env` (default `HEDGEHOOD_SIGNER_KEY`). There is no flag that takes the key itself, and the
key is never printed. `tx` only prints.

## Library

```go
price := func(ctx context.Context, r maker.RFQ) (maker.Answer, error) {
	if !r.MakerBuys() {
		return maker.DeclineWith("no_inventory"), nil
	}
	p := maker.Notional(r.Amount.Int, r.Series.Strike, nil) // USDG raw
	p.Mul(p, big.NewInt(40)).Quo(p, big.NewInt(10_000))      // 40 bps, fee excluded
	return maker.PriceAt(p), nil
}
c, err := maker.NewClient(maker.Config{
	URL: maker.Testnet.GatewayWS, Signer: key, Maker: makerAddr, Pricer: price,
	ChainID: maker.Testnet.ChainID, Settler: maker.Testnet.Settler, // refuse any other deployment
	OnEvent: func(f maker.Frame) { log.Printf("%s %s", f.Type(), f.Raw()) },
	EpochReader: maker.NewReader(maker.Testnet, "").Epoch,
})
err = c.Run(ctx) // returns nil when ctx ends; a refused login or a wrong deployment is returned, not retried
```

A pricer may also return `maker.SendQuote(q)` with `q` from `c.BuildQuote(r, premium, maker.ValidFor(30*time.Second),
maker.WithExternalAccount(wallet))`. Use `maker.PremiumForNet` to quote an all-in price.

Before quoting an early close (a MakerSells request), check you hold the long: `reader.CanSell(ctx, maker, r.Series,
r.Amount.Int)` returns why the settler could not deliver it ("" when it can); the gateway rejects anything else as
`inventory`. A runnable library example that does this: [examples/simple-maker](examples/simple-maker)
(`go run ./examples/simple-maker -bps 40 -sell`). The CLI's `connect --sides sell|both` checks the same way.

## Development

```
cd go
gofmt -l . && go vet ./... && go test -race ./...     # offline; a fake gateway and a fake RPC run in process
HEDGEHOOD_LIVE_RPC=https://rpc.testnet.chain.robinhood.com go test ./maker -run Live -v   # opt-in, read-only
```
