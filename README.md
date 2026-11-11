# Hedgehood SDKs

Market-maker SDKs and a CLI for the Hedgehood options RFQ: Earn upfront users write covered calls and cash-secured
puts on Robinhood Chain stock tokens, and makers buy them with **firm EIP-712 quotes they sign themselves**. Every
request is a short sealed-bid auction on Hedgehood's RFQ gateway; the best quote wins and the user fills it on chain
through `RfqSettler`. The gateway never holds your funds or keys and cannot change a quote. There is no last look.

> **Testnet only** (Robinhood Chain testnet, chainId 46630). The contracts (`OptionsCore`, `RfqSettler`) are not yet
> audited; mainnet addresses follow the launch. Nothing in this repository is a security review.

| | Path | Package | Example |
|---|---|---|---|
| Python | [`src/hedgehood_maker`](src/hedgehood_maker) | `hedgehood-maker` | [`examples/simple_maker.py`](examples/simple_maker.py), [`examples/onboard.py`](examples/onboard.py) |
| TypeScript | [`ts/`](ts) | `@hedgehood/maker` | [`ts/examples/simple-maker.ts`](ts/examples/simple-maker.ts) |
| Go | [`go/maker`](go/maker) | `github.com/0xHedgeHood/sdks/go/maker` | [`go/examples/simple-maker`](go/examples/simple-maker) |
| CLI (Go) | [`go/cmd/hedgehood-maker`](go/cmd/hedgehood-maker) | `hedgehood-maker` | `hedgehood-maker connect --bps 40` |

All three clients do the same things, checked against the same test vectors (quote digests byte-equal to
`RfqSettler.quoteDigest`):

- **Connect and quote**: log in to the gateway, receive each request, call your pricer within the 2-second window,
  build and sign the quote (fresh nonce, current epoch, all-or-nothing), check it the way the gateway will, reconnect
  with backoff.
- **Set up on chain**: build the unsigned transactions a maker needs (`setSigner`, `approve`, `deposit`, `withdraw`,
  `increaseEpoch`, `invalidateNonces`, `linkMaker`, `OptionsCore.setOperator`, `redeemLong`). The SDKs never sign or
  send a transaction; you review and send each one.
- **Read the chain**: epoch, signer delegation, balances, allowances, fees, and `canSell` / `CanSell`, which tells
  you whether you hold a long before quoting an early close.

The full maker guide (going live, funding, fees, timing, scorecard, wire format) is
[docs/MAKER_GUIDE.md](docs/MAKER_GUIDE.md).

## How a maker goes live

1. **Intro call.** Share your maker address and the underlyings you want to quote; we allowlist you on testnet.
2. **Testnet setup.** Delegate a hot signer, fund (a settler deposit or a wallet allowance), approve the settler as
   operator of your longs if you will quote early closes, and connect your bot.
3. **48 h testnet run.** Every request answered in time; at least 3 opens and 2 closes filled; one position held to
   expiry.
4. **Mainnet.** We review your scorecard with you and allowlist your mainnet address.

## Fees

The settler charges the maker per fill: 12.5 % of the premium, capped at 1 % of the notional, enforced on chain. Fee
increases are announced on chain and apply 24 hours later; decreases apply at once.

## Testnet

| | |
|---|---|
| Chain | Robinhood Chain testnet, chainId 46630, RPC `https://rpc.testnet.chain.robinhood.com` |
| Gateway (makers) | `wss://testnet-rfq.hedgehood.io/v1/maker/ws` |
| RfqSettler | `0xda43b0933031e41D5B367E1807536FcF4ebdEE7e` |
| OptionsCore | `0xde5ce903254E9597822D5456a82a25640285B8B4` |
| Quote token (test USDG, 6 decimals) | `0x850DE815d41F6983Aaf09247f9a465c0aefff04C` |
| Scorecard | `https://testnet-rfq.hedgehood.io/v1/makers` |

## Python

```
pip install "git+https://github.com/0xHedgeHood/sdks"      # or: pip install -e ".[dev]" from a checkout
```

Python 3.10+, dependencies `eth-account` and `websockets`.

1. Print the setup transactions, review them and send them yourself:

   ```
   python examples/onboard.py --maker 0xYourMaker --signer 0xYourHotKey --signer-days 7 --deposit 1000 --rpc
   ```

2. Quote:

   ```python
   import asyncio, os
   from hedgehood_maker import MAKER_BUYS, RFQ, TESTNET, Decline, MakerClient
   from hedgehood_maker.fees import notional

   def price(rfq: RFQ):
       if rfq.side != MAKER_BUYS:
           return Decline("no_inventory")             # closes: see examples/simple_maker.py --sell
       return notional(rfq.amount, rfq.series.strike) * 40 // 10_000   # premium, USDG raw, fee excluded

   client = MakerClient(
       TESTNET.gateway_ws,
       os.environ["HEDGEHOOD_SIGNER_KEY"],              # the maker key or its delegated hot key
       price,
       maker="0xYourMaker",                             # omit when the key is the maker itself
       chain_id=TESTNET.chain_id, settler=TESTNET.settler,
       on_event=lambda f: print(f.type, f.raw),         # won, lost, fill, rejected, ...
   )
   asyncio.run(client.run())
   ```

   The pricer may be sync (run in a thread) or async. It returns a premium (int), a `Quote` from
   `client.build_quote(...)`, or a decline. `examples/simple_maker.py` is fuller: Black-Scholes or fixed bps,
   fee-aware, and with `--sell` it quotes early closes only for longs it holds:

   ```
   export HEDGEHOOD_SIGNER_KEY=...
   python examples/simple_maker.py --mode vol --vol 0.6 --spot TSLA=390 --edge 0.05 --sell --external self
   ```

## TypeScript and Go

See [ts/README.md](ts/README.md) and [go/README.md](go/README.md). The CLI in one line:

```
cd go && go run ./cmd/hedgehood-maker connect --maker 0xYourMaker --bps 40 --sides both
```

## Development

```
pip install -e ".[dev]" && ruff check . && pytest                       # Python, offline
cd ts && npm ci && npm run typecheck && npm run lint && npm test        # TypeScript, offline
cd go && go vet ./... && go test -race ./...                            # Go and the CLI, offline
```

Each suite also has an opt-in, read-only test against the testnet RPC (`HEDGEHOOD_LIVE_RPC`).

## License

MIT, see [LICENSE](LICENSE).
