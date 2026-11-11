# Maker guide

> **Testnet only.** The options core contracts (`OptionsCore`, `RfqSettler`) are not yet audited. Mainnet addresses
> are TBD. Nothing in this SDK or guide is a security review.

Hedgehood's RFQ gateway runs a short sealed-bid auction for every taker request on the options core. Makers answer
over a WebSocket with **firm EIP-712 quotes they sign themselves**; the gateway checks each quote, returns the best one
to the taker, and the taker (or the gateway's relayer, with the taker's signed consent) fills it on chain through
`RfqSettler`. The gateway never holds your funds or keys and cannot change a quote. There is no last look: a quote is
firm until its `validUntil`, until its nonce is used, or until you change your epoch.

```
taker ── POST /v1/rfq ──► gateway ── rfq ──► makers (WebSocket, in parallel)
                             ◄── quote (signed) / decline ──
taker ◄── best quote + calldata ──   (2 s window, earlier when every maker has answered)
taker ── RfqSettler.fill / close ──► chain ── Filled ──► gateway ── won / fill ──► winning maker
```

This guide covers what a maker needs; the wire format is in [Protocol reference](#protocol-reference).

## Going live

Every maker goes through the same four stages. Mainnet access follows a clean testnet run, never the other way round.

| Stage | What happens | Done when |
|---|---|---|
| 1. Intro call | You share your maker address(es), the underlyings you want to quote, expected sizes and a contact. We allowlist you on the **testnet** gateway and agree your initial mainnet limits. | You are on the testnet allowlist |
| 2. Testnet setup | The [checklist below](#testnet-checklist): signer, funding, operator approval, a connected bot. | Your bot logs in and answers requests |
| 3. Testnet run | Your bot stays connected for **2 days (48 h)**, across at least one US regular session. We send takers against you. | No breaker pause; every request answered (quote or decline) before `respondBy`; at least **3 opens** (MakerBuys) and **2 early closes** (MakerSells) filled; one position held to expiry and paid out (and, on a physical series, exercised) |
| 4. Mainnet | We review your testnet scorecard (`GET /v1/makers`) with you and add your mainnet address to the mainnet allowlist with the agreed limits. Mainnet funding and signer setup repeat stage 2 on mainnet. | Your first mainnet fill |

Questions during any stage go to the Hedgehood team contact you got at the intro call.

## After expiry

- **Cash payouts are pushed to you.** Hedgehood's expiry keeper settles every expired series and then calls
  `OptionsCore.redeemLong` for each holder's whole balance (anyone may push a whole balance home), so your USDG or
  underlying arrives without a transaction from you. You can still redeem yourself at any time after settlement.
- **Physical series.** An in-the-money long may `OptionsCore.exercise` for delivery during the **1-day window** after
  settlement: call `exercise(series, amount, maker, receiver)` from the maker, or from an account the maker authorized
  with `OptionsCore.setAuthorization`. **The caller pays** (the strike in USDG for a call, the shares for a put) and
  must approve the kernel for it first; `receiver` gets the shares or the USDG. What is not exercised in the window is paid in cash like a cash series.
- **No early exercise.** Options are European; a long you no longer want can be sold back to a taker only through a
  MakerSells quote (an early close).

## Testnet checklist

1. **Be allowlisted** (stage 1 above) for your **maker address**: the account whose funds and longs move. The
   gateway refuses any login from an address that is not on its allowlist.
2. **Delegate a hot signer** (recommended). From the maker, call `RfqSettler.setSigner(hotKey, expiresAt)`. The hot key
   can then log in and sign quotes for the maker. A quote only counts while `expiresAt > validUntil`, so rotate keys
   before they expire; `setSigner(key, 0)` revokes one. A hot signer has the maker's full authority over its settler
   balance and longs until it expires: keep it short-lived and keep the balance at what you mean to quote.
3. **Fund**, per quote, in one of two ways (chosen by the quote's `externalAccount`):

   | Funding | `externalAccount` | Setup | Notes |
   |---|---|---|---|
   | Settler balance | `0x0` | `USDG.approve(settler, x)`, `deposit(USDG, maker, x)` | Withdraw with `requestWithdraw`, then `withdraw` between 5 min and 1 day + 5 min later; the amount keeps backing fills until then. |
   | Wallet by allowance | the paying wallet | `USDG.approve(settler, x)` from that wallet; if it is not the maker, it also calls `linkMaker(maker, true)` once | Nothing parked in the settler. On MakerSells the net premium goes straight to that wallet. |

4. **To sell longs you hold (MakerSells)**: `OptionsCore.setOperator(settler, true)` from the maker.
5. **Build the transactions** with `python examples/onboard.py ...` (prints unsigned transactions and `cast send`
   lines; `--rpc` also shows the current on-chain state). Review them and send each yourself.
6. **Connect**: `python examples/simple_maker.py ...`, or your own `MakerClient` (see the [README](../README.md)).
7. **Test end to end** on testnet by requesting a quote yourself (any taker address works for a quote; filling needs
   the taker's collateral and approvals):

   ```
   curl -s https://testnet-rfq.hedgehood.io/v1/rfq -H 'content-type: application/json' -d \
     '{"underlying":"TSLA","expiry":<a listed 16:00 New York close>,"strike":"330000000","isPut":true,
       "side":0,"amount":"1000000000000000000","taker":"0x..."}'
   ```

## Timing

| Rule | Value | Enforced by |
|---|---|---|
| Auction window | `respondBy` = request + 2 s (unix **ms**) | gateway: later answers are dropped and counted late |
| Quote life | `validUntil` <= now + 120 s (`MAX_QUOTE_LIFE`) | gateway and settler |
| Minimum life left | `validUntil` >= `minValidUntil` (= `respondBy` + 15 s) | gateway |
| Fill size | `minFill` = `amount` (whole fills) | gateway |
| New shorts (MakerBuys) | listed oracle and expiry, not paused, at least `tradingCloseBefore` (15 min on testnet) before expiry | settler |
| Expiries | the 16:00 New York close of a listed weekday, at most `maxWeeksAhead` (6 on testnet) ahead | settler |

The gateway answers the taker as soon as every connected maker has replied, so a fast decline helps everyone.
`MakerClient` defaults to a 60 s quote life and caps it at 115 s to leave room for clock skew (keep your clock on
NTP). It gives your pricer the time left before `respondBy` minus 150 ms and declines with `timeout` if the pricer
takes longer.

## Scorecard and circuit breaker

The gateway keeps a per-maker scorecard (requests, answers, declines, late answers, rejections by code, wins, fills,
fades) at `GET /v1/makers`. Defaults (`cmd/rfq-gateway`; the operator can change them):

| Breaker | Default | Effect |
|---|---|---|
| `maxSilent` | 20 requests unanswered **in a row** | paused for `cooldown` |
| `maxFades` | 3 fades within `fadeWindow` (1 hour) | paused for `cooldown` |
| `cooldown` | 15 minutes | no requests are sent to you |

- A **decline counts as an answer** and resets the silence streak. Always decline what you will not price.
- A **fade** is a quote that won and then expired unfilled while your epoch had moved or its nonce had been burnt.
  Cancelling (`increaseEpoch`, `invalidateNonces`) after you have won is a fade. A funding shortfall that makes the
  taker's fill revert is not detected as a fade by the current gateway, but it fails the taker the same way: keep the
  balance or allowance behind every outstanding quote.
- The gateway sends **no frame** when the breaker pauses you; requests just stop. Watch `GET /v1/makers`.
- The operator can also pause or remove a maker.
- One connection per maker: a new login replaces the old one, so two processes for the same maker keep kicking each
  other off.

## Fees

The settler charges the **maker**, per fill, `fee = min(premium × feeBps, amount × strike / 1e18 × notionalFeeBps) /
10 000`, every step rounded down. On testnet `feeBps = 1250` and `notionalFeeBps = 100`: **12.5 % of the premium,
capped at 1 % of the notional**. Both are read from the chain and arrive in the gateway's `ready` frame
(`client.session.fee_bps`, `client.session.notional_fee_bps`).

| Side | You pay / receive |
|---|---|
| `0` MakerBuys (taker writes, you buy the long) | you pay `premium + fee` |
| `1` MakerSells (you sell a long you hold) | you receive `premium − fee` |

Example: 10 TSLA puts at strike 220, premium 300 USDG: 12.5 % would be 37.5, the cap is 1 % × 2 200 = 22, so the fee
is 22 USDG.

`premium` in a quote **excludes** the fee. To quote an all-in price, use
`fees.premium_for_net(side, net, amount, strike, fee_bps, notional_fee_bps)`: the largest premium whose premium + fee
fits your budget (buys), or the smallest premium that leaves you `net` after the fee (sells).

When the gateway relays a fill for a gasless taker, its gas fee (**0.02 USDG** on testnet) is paid by the **taker**: out
of the premium the taker receives on an open, on top of the premium it pays on a close. It never changes your side.

## Protocol reference

Checked against the gateway code and the deployed testnet settler.

**EIP-712.** Domain `{name: "Hedgehood RFQ Settler", version: "2", chainId, verifyingContract: settler}`.

```
Quote(address maker,address signer,address taker,address externalAccount,SeriesParams series,uint8 side,
      uint256 amount,uint256 minFill,uint256 premium,uint64 validUntil,uint256 nonce,uint256 epoch)
SeriesParams(address underlying,address quote,address oracle,uint64 expiry,bool isPut,uint256 strike,bool physical)
```

The digest is the standard EIP-712 digest (`settler.quoteDigest` equals `eip712.quote_digest`; the SDK's tests pin the
same golden vectors as the Solidity, Go and desk implementations). Signatures are 65-byte `r‖s‖v`, low-s.

**JSON.** uint256 fields (`strike`, `amount`, `minFill`, `premium`, `nonce`, `epoch`) are **decimal strings**;
`expiry`, `validUntil`, `side` and `chainId` are **JSON numbers** (the gateway rejects strings there). Addresses are
`0x` hex in any case. `physical` is part of the series.

**Nonces.** A Permit2-style bitmap per maker: slot = `nonce >> 8`. A full fill burns the bit; the first fill also binds
the nonce to that quote, so quotes sharing a nonce exclude each other. `NonceManager` gives every quote its own.
`epoch` must equal `settler.epoch(maker)`.

**Cancelling.** `increaseEpoch(maker)` voids everything outstanding (maker or a live signer); `invalidateNonces(maker,
slot, mask)` voids chosen nonces; WebSocket `{"type":"pause"}` / `{"type":"resume"}` stops / restarts requests
(answered with `paused` / `resumed`); let a signer expire or `setSigner(key, 0)`.

**WebSocket** `wss://<gateway>/v1/maker/ws`, JSON text frames, at most 16 KiB from you.

| Direction | Frame |
|---|---|
| ← | `{"type":"challenge","nonce":"<hex>","chainId":46630}` |
| → | `{"type":"login","maker","signer","signature"}`, signature = EIP-191 `personal_sign` by `signer` of `hedgehood-rfq-login:<chainId>:<maker lower-case>:<nonce>`, within 10 s |
| ← | `{"type":"ready","maker","settler","feeBps","notionalFeeBps","epoch"}` or `{"type":"error","code":"login","reason"}` and close |
| ← | `{"type":"rfq","rfqId","chainId","settler","taker","side","series":{...},"amount","respondBy","minValidUntil"}` |
| → | `{"type":"quote","rfqId","quote":{...},"signature"}` or `{"type":"decline","rfqId","reason"}` |
| ← | `{"type":"won","rfqId","digest"}`, `{"type":"lost","rfqId"}`, `{"type":"fill","digest","txHash","amount","rfqId"}` |
| ← | `{"type":"rejected","rfqId","code","reason"}`, `{"type":"error","code","reason"}` |

The gateway pings every 10 s and drops a maker that does not pong within 5 s (the websockets library answers pings).
A request for an early close (`close` on the taker side) looks like any side-1 request to makers.

**Rejection codes** (why a quote cannot win): `bad_signature`, `mismatch` (maker, taker, side, series, amount, `minFill
== amount`, premium > 0), `expiry` (`validUntil` window), `stale_epoch`, `nonce_used`, `funding` (side 0: settler
balance, or the external account's balance, allowance and link, >= premium + fee), `inventory` (side 1: your long >=
amount and the settler is your operator), `unavailable` (the gateway could not read the chain), `late`, `invalid`.

**Settlement.** At expiry the long pays `(P − K) / P` tokens per call and `K − P` USDG per put, `P` being the listed
`StockSettlementSource` price for the round in effect at 16:00:00 New York. After `settle`, redeem with
`OptionsCore.redeemLong` (`SettlerTxs.redeem_long`). Physical series let an in-the-money long `exercise` for a day
after settlement instead.

## Testnet addresses

Robinhood Chain testnet, chainId **46630**. Testnet only; mainnet addresses TBD.

| Contract | Address |
|---|---|
| RfqSettler (proxy) | `0xda43b0933031e41D5B367E1807536FcF4ebdEE7e` |
| OptionsCore (proxy) | `0xde5ce903254E9597822D5456a82a25640285B8B4` |
| TUSD (testnet USDG, 6 decimals) | `0x850DE815d41F6983Aaf09247f9a465c0aefff04C` |
| TSLA (stock token, 18 decimals) | `0x6661d20C2e343Bb78B42535eB060C3058c744f3F` |
| TSLA settlement oracle | `0x9e6C529CAA69d44b63df27483c57f97a42D6e898` |
| Gateway, maker socket | `wss://testnet-rfq.hedgehood.io/v1/maker/ws` |
| Gateway, taker API | `https://testnet-rfq.hedgehood.io/v1/rfq` |
| RPC | `https://rpc.testnet.chain.robinhood.com` |
