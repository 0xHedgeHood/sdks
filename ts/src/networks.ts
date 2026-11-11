/** Deployments. TESTNET ONLY: the options core contracts are not audited yet; mainnet addresses are TBD. */

import type { Address } from "viem";

export interface Underlying {
  /** The stock token (18 decimals). */
  readonly token: Address;
  /** The StockSettlementSource listed on the settler for (token, quote token). */
  readonly oracle: Address;
}

export interface Network {
  readonly name: string;
  readonly chainId: number;
  /** RfqSettler (UUPS proxy). */
  readonly settler: Address;
  /** OptionsCore (UUPS proxy). */
  readonly core: Address;
  /** USDG-like stable, 6 decimals. */
  readonly quoteToken: Address;
  readonly gatewayWs: string;
  readonly rpc: string;
  readonly underlyings: Readonly<Record<string, Underlying>>;
}

/**
 * Robinhood Chain testnet. Fee parameters are read from the settler (and arrive in the gateway's `ready` frame); at
 * the time of writing feeBps = 1250 and notionalFeeBps = 100.
 */
export const TESTNET: Network = Object.freeze({
  name: "robinhood-testnet",
  chainId: 46630,
  settler: "0xda43b0933031e41D5B367E1807536FcF4ebdEE7e",
  core: "0xde5ce903254E9597822D5456a82a25640285B8B4",
  quoteToken: "0x850DE815d41F6983Aaf09247f9a465c0aefff04C", // TUSD (testnet USDG stand-in, 6 decimals)
  gatewayWs: "wss://testnet-rfq.hedgehood.io/v1/maker/ws",
  rpc: "https://rpc.testnet.chain.robinhood.com",
  underlyings: Object.freeze({
    TSLA: Object.freeze({
      token: "0x6661d20C2e343Bb78B42535eB060C3058c744f3F",
      oracle: "0x9e6C529CAA69d44b63df27483c57f97a42D6e898",
    }),
  }),
});

export const NETWORKS: Readonly<Record<string, Network>> = Object.freeze({ testnet: TESTNET });
