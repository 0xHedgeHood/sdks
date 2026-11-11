/**
 * Opt-in, read-only checks against the deployed testnet settler (eth_call only; nothing is signed or sent):
 *
 *     HEDGEHOOD_LIVE_RPC=https://rpc.testnet.chain.robinhood.com npm run test:live
 */

import { describe, expect, it } from "vitest";
import { domainSeparator, quoteDigest } from "../src/eip712.js";
import { replaceQuote } from "../src/models.js";
import { TESTNET } from "../src/networks.js";
import { Reader } from "../src/rpc.js";
import * as G from "./goldens.js";

const RPC = process.env.HEDGEHOOD_LIVE_RPC;

describe.skipIf(!RPC)("testnet settler (HEDGEHOOD_LIVE_RPC)", () => {
  const reader = () => Reader.forNetwork(TESTNET, { rpcUrl: RPC! });

  it("computes our digest", async () => {
    const r = reader();
    expect(await r.chainId()).toBe(TESTNET.chainId);
    const q = replaceQuote(G.QUOTE, { nonce: 12345n, epoch: 7n, minFill: G.QUOTE.amount });
    expect(await r.quoteDigest(q)).toBe(quoteDigest(q, TESTNET.chainId, TESTNET.settler));
  });

  it("has the pinned DOMAIN_SEPARATOR", async () => {
    const [sep] = await reader().call(TESTNET.settler, "DOMAIN_SEPARATOR()", [], ["bytes32"]);
    expect(sep).toBe(G.TESTNET_DOMAIN_SEPARATOR);
    expect(domainSeparator(TESTNET.chainId, TESTNET.settler)).toBe(G.TESTNET_DOMAIN_SEPARATOR);
  });

  it("has readable fee parameters", async () => {
    const { feeBps, notionalFeeBps } = await reader().fees();
    expect(feeBps >= 0n && feeBps <= 10_000n).toBe(true);
    expect(notionalFeeBps >= 0n && notionalFeeBps <= 10_000n).toBe(true);
  });
});
