# hedgehood-maker

Python SDK for market makers on the Hedgehood options RFQ gateway: sign firm EIP-712 quotes for the `RfqSettler`,
answer the gateway's requests over its WebSocket, and build the (unsigned) on-chain setup transactions.

> **Testnet only** (Robinhood Chain testnet, chainId 46630). The options core contracts are not yet audited; mainnet
> addresses are TBD.

- `MakerClient`: login, a pricer callback per request under the request's deadline, automatic quote building and
  signing (fresh nonce, current epoch, `minFill = amount`), local checks matching the gateway's, won / lost / fill /
  rejected events, reconnect with backoff, pause / resume.
- `eip712`: the quote digest and signature, byte-equal to `RfqSettler.quoteDigest` (tests pin the golden vectors shared
  with the Solidity, Go and desk implementations, and an opt-in test compares with the deployed settler).
- `settler.SettlerTxs`: unsigned transactions for `setSigner`, `deposit`, `requestWithdraw`, `withdraw`,
  `increaseEpoch`, `invalidateNonces`, `linkMaker`, ERC-20 `approve`, `OptionsCore.setOperator` and `redeemLong`.
  The SDK never signs or sends a transaction.
- `fees`: the settler's fee and pro-rata math (`previewFill`), and `premium_for_net` to quote an all-in price.
- `rpc.Reader`: read-only `eth_call`s (epoch, signer expiry, balances, allowances, fees, `quoteDigest`).

## Install

```
pip install git+https://github.com/0xHedgeHood/hedgehood-maker-sdk   # or: pip install -e ".[dev]" from a checkout
```

Python 3.10+. Dependencies: `eth-account`, `websockets`.

## Quickstart

1. Get your maker address allowlisted, delegate a hot signer and fund (see [docs/MAKER_GUIDE.md](docs/MAKER_GUIDE.md)):

   ```
   python examples/onboard.py --maker 0xYourMaker --signer 0xYourHotKey --signer-days 7 --deposit 1000 --rpc
   ```

   This prints the unsigned transactions (and `cast send` equivalents) for you to review and send yourself.

2. Quote:

   ```python
   import asyncio, os
   from hedgehood_maker import MAKER_BUYS, RFQ, TESTNET, Decline, MakerClient
   from hedgehood_maker.fees import notional

   def price(rfq: RFQ):
       if rfq.side != MAKER_BUYS:
           return Decline("no_inventory")             # or a reason string; None = "no_price"
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
   `client.build_quote(rfq, premium, valid_for=..., external_account=...)`, or a decline.

   A fuller example (fixed volatility or fixed bps, fee-aware): `examples/simple_maker.py`.

## Development

```
pip install -e ".[dev]"
ruff check .
pytest                                                    # offline; a fake gateway runs in process
HEDGEHOOD_LIVE_RPC=https://rpc.testnet.chain.robinhood.com pytest tests/test_live.py   # opt-in, read-only
```

## License

MIT, see [LICENSE](LICENSE).
