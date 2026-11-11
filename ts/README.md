# @hedgehood/maker (TypeScript)

TypeScript SDK for market makers on the Hedgehood options RFQ gateway, the twin of the Python package in this repo:
sign firm EIP-712 quotes for the `RfqSettler`, answer the gateway's requests over its WebSocket, and build the
(unsigned) on-chain setup transactions.

> **Testnet only** (Robinhood Chain testnet, chainId 46630). The options core contracts are not yet audited; mainnet
> addresses are TBD. The SDK never signs or sends a transaction.

- `MakerClient`: login, an async pricer per request under its deadline (`respondBy` − now − 150 ms), quote building
  and signing (fresh nonce, current epoch, `minFill = amount`), the gateway's static checks locally, events
  (`ready`, `won`, `lost`, `fill`, `rejected`, `error`, `paused`, `resumed`, `frame`), reconnect with backoff,
  `pause()` / `resume()`.
- `quoteDigest`, `signQuote`, `typedData`, `seriesId`: byte-equal to `RfqSettler.quoteDigest` (tests pin the golden
  vectors shared with the Solidity, Go and Python implementations).
- `SettlerTxs`: unsigned `{to, data}` for `setSigner`, `deposit`, `requestWithdraw`, `withdraw`, `increaseEpoch`,
  `invalidateNonces`, `linkMaker`, ERC-20 `approve`, `OptionsCore.setOperator` and `redeemLong` (`castCommand(tx)`
  prints the `cast send` equivalent).
- `feeOf`, `previewFill`, `premiumForNet`: the settler's fee math in bigints.
- `Reader`: read-only `eth_call`s through a viem client (epoch, signer expiry, balances, allowances, fees,
  `quoteDigest`, `canSell`).

Node ≥ 20, ESM. Dependencies: `viem`, `ws`. Every on-chain integer is a `bigint`.

## Install

From a checkout (the package is not published to npm):

```
cd ts && npm ci && npm run build
npm install /path/to/sdks/ts      # in your project
```

## Quickstart

Onboarding (allowlist, `setSigner`, funding) is in [docs/MAKER_GUIDE.md](../docs/MAKER_GUIDE.md); the transactions:

```ts
import { castCommand, SettlerTxs, TESTNET } from "@hedgehood/maker";

const txs = SettlerTxs.forNetwork(TESTNET);
const hotKeyUntil = BigInt(Math.floor(Date.now() / 1000) + 7 * 86400);
for (const tx of [
  txs.setSigner("0xYourHotKey", hotKeyUntil, { from: "0xYourMaker" }),
  txs.approve(TESTNET.quoteToken, 1_000_000_000n, { from: "0xYourMaker" }),
  txs.deposit(TESTNET.quoteToken, "0xYourMaker", 1_000_000_000n),
]) console.log(tx.description, "\n ", castCommand(tx, TESTNET.rpc)); // review, then sign and send yourself
```

Quote:

```ts
import { Decline, MAKER_BUYS, MakerClient, notional, TESTNET } from "@hedgehood/maker";

const client = new MakerClient({
  url: TESTNET.gatewayWs,
  signer: process.env.HEDGEHOOD_SIGNER_KEY as `0x${string}`, // the maker key or its delegated hot key
  maker: "0xYourMaker",                                      // omit when the key is the maker itself
  chainId: TESTNET.chainId,
  settler: TESTNET.settler,
  pricer: async (rfq) => {
    if (rfq.side !== MAKER_BUYS) return new Decline("no_inventory"); // or a reason string; null = "no_price"
    return (notional(rfq.amount, rfq.series.strike) * 40n) / 10_000n; // premium, USDG raw, fee excluded
  },
});
client.on("won", (f) => console.log("won", f.rfqId, f.digest));
client.on("fill", (f) => console.log("fill", f.txHash));
await client.run();
```

The pricer returns a premium (`bigint`), a quote from `client.buildQuote(rfq, premium, { validFor, externalAccount })`,
or a decline. It runs on the event loop: keep it async or quick; past its budget the client declines with `timeout`
and aborts `signal`.

Before quoting an early close (a MakerSells request), check you hold the long: `reader.canSell(maker, rfq.series,
rfq.amount)` resolves to why the settler could not deliver it (null when it can); the gateway rejects anything else as
`inventory`.

A fuller example (fixed volatility or fixed bps, fee-aware, `--sell` quotes closes only for longs it holds):
[examples/simple-maker.ts](examples/simple-maker.ts).
Run it with `npm run build && node examples/simple-maker.ts --mode bps --bps 50` (flags in its header; Node ≥ 22.18
runs `.ts` directly, Node 20 needs `npx tsx`).

## Development

```
npm ci
npm run typecheck && npm run lint && npm test          # offline; a fake gateway runs in process
HEDGEHOOD_LIVE_RPC=https://rpc.testnet.chain.robinhood.com npm run test:live   # opt-in, read-only eth_calls
```
