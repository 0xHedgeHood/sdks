# Hedgehood SDKs

Market-maker SDKs and a CLI for the Hedgehood options RFQ: Earn upfront users write covered calls and cash-secured
puts on Robinhood Chain stock tokens, and makers buy them with **firm EIP-712 quotes they sign themselves**. Every
request is a short sealed-bid auction on Hedgehood's RFQ gateway; the best quote wins and the user fills it on chain
through `RfqSettler`. The gateway never holds your funds or keys and cannot change a quote. There is no last look.

> **Testnet only** (Robinhood Chain testnet, chainId 46630). The contracts (`OptionsCore`, `RfqSettler`) are not yet
> audited; mainnet addresses follow the launch. Nothing in these repositories is a security review.

Each language lives in its own repository:

| | Repository | Package | Install |
|---|---|---|---|
| Python | [hedgehood-maker-py](https://github.com/0xHedgeHood/hedgehood-maker-py) | `hedgehood-maker` | `pip install "git+https://github.com/0xHedgeHood/hedgehood-maker-py"` |
| TypeScript | [hedgehood-maker-ts](https://github.com/0xHedgeHood/hedgehood-maker-ts) | `@hedgehood/maker` | `npm install github:0xHedgeHood/hedgehood-maker-ts` |
| Go | [hedgehood-maker-go](https://github.com/0xHedgeHood/hedgehood-maker-go) | `github.com/0xHedgeHood/hedgehood-maker-go/maker` | `go get github.com/0xHedgeHood/hedgehood-maker-go/maker` |
| CLI (Go) | [hedgehood-maker-go](https://github.com/0xHedgeHood/hedgehood-maker-go/tree/main/cmd/hedgehood-maker) | `hedgehood-maker` | `go install github.com/0xHedgeHood/hedgehood-maker-go/cmd/hedgehood-maker@latest` |

This repository holds what they share:

- [docs/MAKER_GUIDE.md](docs/MAKER_GUIDE.md): the maker guide (going live, funding, fees, timing, scorecard, wire
  format).
- [vectors/goldens.json](vectors/goldens.json): the golden vectors for the quote encoding (digest, signature, series
  id, calldata hashes, type strings, selectors). Each SDK's CI checks this repository out and recomputes them, as do
  the Solidity tests, the gateway and our desk. A change to the encoding starts here.

All three clients do the same things:

- **Connect and quote**: log in to the gateway, receive each request, call your pricer within the 2-second window,
  build and sign the quote (fresh nonce, current epoch, all-or-nothing), check it the way the gateway will, reconnect
  with backoff.
- **Set up on chain**: build the unsigned transactions a maker needs (`setSigner`, `approve`, `deposit`, `withdraw`,
  `increaseEpoch`, `invalidateNonces`, `linkMaker`, `OptionsCore.setOperator`, `redeemLong`). The SDKs never sign or
  send a transaction; you review and send each one.
- **Read the chain**: epoch, signer delegation, balances, allowances, fees, and `can_sell` / `canSell` / `CanSell`,
  which tells you whether you hold a long before quoting an early close.

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

## History

Until 2026-11-11 the three SDKs lived here together (Python at the root, `ts/`, `go/`, module path
`github.com/0xHedgeHood/sdks/go`). They moved to their own repositories from commit `4d03dda`; this repository's
history keeps the earlier work.

## License

MIT, see [LICENSE](LICENSE).
