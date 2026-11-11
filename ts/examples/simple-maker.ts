/**
 * A minimal maker: quotes every listed TESTNET request at a fixed volatility (Black-Scholes on a spot you pass) or at
 * a fixed premium in basis points of the notional. A starting point, not a pricing model: no spot feed, no hedging,
 * no inventory limits beyond --max-amount. The TypeScript twin of examples/simple_maker.py.
 *
 *     npm run build
 *     export HEDGEHOOD_SIGNER_KEY=0x...     # the maker key, or a hot key the maker delegated with setSigner
 *     node examples/simple-maker.ts --maker 0xYourMaker --mode vol --vol 0.6 --spot TSLA=330 --edge 0.05
 *     node examples/simple-maker.ts --mode bps --bps 50
 *
 * (Node >= 22.18 runs .ts directly; on Node 20 use `npx tsx examples/simple-maker.ts ...`.)
 * Testnet only. The key is read from the environment (or --key-file) and never printed.
 */

import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import {
  address,
  Decline,
  MAKER_BUYS,
  MakerClient,
  notional,
  type PricerResult,
  premiumForNet,
  Reader,
  type RFQ,
  SHARE,
  TESTNET,
  USDG,
} from "@hedgehood/maker";
import type { Hex } from "viem";

const YEAR = 365 * 24 * 3600;

/** Abramowitz-Stegun 7.1.26 (|error| < 1.5e-7): plenty for an example. */
function erf(x: number): number {
  const sign = Math.sign(x);
  const t = 1 / (1 + 0.3275911 * Math.abs(x));
  const y =
    1 -
    ((((1.061405429 * t - 1.453152027) * t + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-x * x);
  return sign * y;
}

const normCdf = (x: number) => 0.5 * (1 + erf(x / Math.SQRT2));

/** Price per share (rate 0, no dividends). */
export function blackScholes(spot: number, strike: number, years: number, vol: number, isPut: boolean): number {
  if (years <= 0 || vol <= 0) return Math.max(0, isPut ? strike - spot : spot - strike);
  const sd = vol * Math.sqrt(years);
  const d1 = (Math.log(spot / strike) + 0.5 * sd * sd) / sd;
  const d2 = d1 - sd;
  return isPut ? strike * normCdf(-d2) - spot * normCdf(-d1) : spot * normCdf(d1) - strike * normCdf(d2);
}

export interface PricerArgs {
  mode: "vol" | "bps";
  vol: number;
  /** TICKER -> spot, e.g. { TSLA: 330 } (mode vol). */
  spots: Record<string, number>;
  /** Premium in bps of amount x strike (mode bps). */
  bps: number;
  /** Fraction below theo to buy / above to sell. */
  edge: number;
  /** Largest request quoted, in whole tokens. */
  maxAmount: number;
  /** Also quote MakerSells (you must hold the longs). */
  sell: boolean;
}

/** The pricer: theo less (buys) or plus (sells) the edge, all-in after the settler's fee. */
export function makePricer(args: PricerArgs, fees: () => { feeBps: number; notionalFeeBps: number }) {
  const listed = new Map(Object.entries(TESTNET.underlyings).map(([name, u]) => [`${u.token}|${u.oracle}`, name]));
  return (rfq: RFQ): PricerResult => {
    const s = rfq.series;
    const name = listed.get(`${s.underlying}|${s.oracle}`);
    if (name === undefined || s.quote !== TESTNET.quoteToken) return new Decline("unlisted");
    if (rfq.amount > BigInt(Math.floor(args.maxAmount * 1e6)) * (SHARE / 1_000_000n)) return new Decline("too_large");
    if (rfq.side !== MAKER_BUYS && !args.sell) return new Decline("no_inventory");
    let theo: number; // quote raw
    if (args.mode === "bps") {
      theo = Number((notional(rfq.amount, s.strike) * BigInt(args.bps)) / 10_000n);
    } else {
      const spot = args.spots[name];
      if (spot === undefined) return new Decline("no_price");
      const years = (Number(s.expiry) - Date.now() / 1000) / YEAR;
      const perShare = blackScholes(spot, Number(s.strike) / Number(USDG), years, args.vol, s.isPut);
      theo = (perShare * Number(USDG) * Number(rfq.amount)) / Number(SHARE);
    }
    const edge = rfq.side === MAKER_BUYS ? 1 - args.edge : 1 + args.edge; // buy below, sell above
    const net = BigInt(Math.floor(theo * edge)); // what we pay all-in (buys) or keep after the fee (sells)
    if (net <= 0n) return new Decline("no_price");
    const { feeBps, notionalFeeBps } = fees();
    const premium = premiumForNet(rfq.side, net, rfq.amount, s.strike, feeBps, notionalFeeBps);
    return premium > 0n ? premium : new Decline("no_price");
  };
}

async function main(): Promise<void> {
  const { values: a } = parseArgs({
    options: {
      gateway: { type: "string", default: TESTNET.gatewayWs },
      maker: { type: "string" }, // maker address when the key is a delegated hot signer
      "key-file": { type: "string" }, // file holding the signer key (default: $HEDGEHOOD_SIGNER_KEY)
      external: { type: "string" }, // externalAccount: an address, or "self" for the maker wallet
      mode: { type: "string", default: "bps" },
      vol: { type: "string", default: "0.6" },
      spot: { type: "string", multiple: true, default: [] },
      bps: { type: "string", default: "50" },
      edge: { type: "string", default: "0.05" },
      "max-amount": { type: "string", default: "10" },
      sell: { type: "boolean", default: false },
      life: { type: "string", default: "60" }, // quote validity, seconds (<= 115)
      "check-rpc": { type: "boolean", default: false }, // read the epoch from the chain after a stale_epoch rejection
    },
  });
  if (a.mode !== "vol" && a.mode !== "bps") throw new Error("--mode must be vol or bps");
  const key = (a["key-file"] ? readFileSync(a["key-file"], "utf8").trim() : process.env.HEDGEHOOD_SIGNER_KEY) as
    | Hex
    | undefined;
  if (!key) {
    console.error("set HEDGEHOOD_SIGNER_KEY or pass --key-file");
    process.exit(1);
  }
  const spots = Object.fromEntries(
    a.spot.map((s) => {
      const [name, price] = s.split("=");
      return [name, Number(price)];
    }),
  );
  const reader = a["check-rpc"] ? Reader.forNetwork(TESTNET) : undefined;
  // the pricer reads the fee parameters the gateway announced at login
  const box: { client?: MakerClient } = {};
  const pricer = makePricer(
    {
      mode: a.mode,
      vol: Number(a.vol),
      spots,
      bps: Number(a.bps),
      edge: Number(a.edge),
      maxAmount: Number(a["max-amount"]),
      sell: a.sell,
    },
    () => box.client!.session!,
  );
  const client = new MakerClient({
    url: a.gateway,
    signer: key,
    pricer,
    maker: a.maker,
    quoteLife: Number(a.life),
    chainId: TESTNET.chainId,
    settler: TESTNET.settler,
    epochReader: reader ? (m) => reader.epoch(m) : undefined,
    logger: {
      debug() {},
      info: (m) => console.log(new Date().toISOString(), m),
      warn: (m) => console.warn(new Date().toISOString(), m),
      error: (m, ...x) => console.error(new Date().toISOString(), m, ...x),
    },
  });
  box.client = client;
  if (a.external) client.externalAccount = address(a.external === "self" ? client.maker : a.external, "--external");
  for (const e of ["won", "fill", "rejected"] as const) {
    client.on(e, (f) => console.log(new Date().toISOString(), f.type, JSON.stringify(f.raw)));
  }
  process.on("SIGINT", () => client.stop());
  process.on("SIGTERM", () => client.stop());
  await client.run();
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((e) => {
    console.error(e instanceof Error ? `${e.name}: ${e.message}` : e);
    process.exit(1);
  });
}
