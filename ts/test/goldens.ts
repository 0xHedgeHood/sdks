/**
 * Golden vectors shared with the other implementations of the same encoding (tests/goldens.py of this repo):
 *
 * - hedgehood-options contracts/test/core/AbiCoreGolden.t.sol (Solidity, the settler itself),
 * - hedgehood-rfq-gateway internal/quote/quote_test.go (Go, the gateway),
 * - hedgehood-rfq-gateway desk/tests/test_abi_core.py (Python, our desk).
 *
 * Same fixed quote, settler proxy at the Solidity test's second CREATE, chain 46630.
 */

import type { Hex } from "viem";
import { makeQuote, makeSeries } from "../src/models.js";

export const SETTLER = "0x5991A2dF15A8F6A256D3Ec51E99254Cd3fb576A9";
export const CHAIN = 46630;
export const SERIES = makeSeries({
  underlying: "0x267ddeE02B44FDF4D073E124869DaA5BDbF1759D",
  quote: "0x850DE815d41F6983Aaf09247f9a465c0aefff04C",
  oracle: "0x0767e42f22cF3D0Ee3b49d5eDAad65b62CD0B3a2",
  expiry: 1793826000n,
  isPut: false,
  strike: 215_000_000n,
});
export const QUOTE = makeQuote({
  maker: "0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308",
  signer: "0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308",
  taker: "0x36437b878415EdA1a24186CF79AFffBc9ecEd298",
  series: SERIES,
  side: 0,
  amount: 5n * 10n ** 18n,
  minFill: 0n,
  premium: 5_164_250n,
  validUntil: 1793766600n,
  nonce: 42n,
});
/** The golden signer: private key 7. */
export const KEY_7: Hex = `0x${"00".repeat(31)}07`;
export const SERIES_ID = 11623297058463634065704459141464122235595256587129134766422972952495797106800n;
export const DIGEST = "0x15adc15e4a460be6a0dd0cde1a16c01c5d1e74d73bcc5edd7a54823a7dcfd81e";
export const SIGNATURE = ("0x141373907102bdbdb3f7cecfa039b039ad6e6af54fe5a7ce0f195f6709e396ed" +
  "3360ba613c6f7d76706fa18b4c6123a20896befeeb53ec9c9a2ae8afbb29577d1b") as Hex;
/** keccak256 of fill(QUOTE, SIGNATURE, 5e18) and close(QUOTE, SIGNATURE, 1e18) calldata. */
export const FILL_CALLDATA_HASH = "0x1c808620179dc62b8a80b4ae0556db4f4ac8d99a89b035f54d6b3ce1167855d0";
export const CLOSE_CALLDATA_HASH = "0x7cdac40d9224ced1edd8d9a3374fb657e30f9565c08287df53a890c81dab1e36";

/** Testnet: RfqSettler.DOMAIN_SEPARATOR() of 0xda43b093...dEE7e on chain 46630, read with eth_call on 2026-11-10. */
export const TESTNET_DOMAIN_SEPARATOR = "0xb21e093b8f5159c565f78c4205db5b8f1d41abad3822682cf1c2c22a3107c7b6";

/** The type strings exactly as RfqSettler.sol hashes them (options core v3, hedgehood-options-core 4f0cd1e). */
export const SOLIDITY_SERIES_TYPE =
  "SeriesParams(address underlying,address quote,address oracle,uint64 expiry,bool isPut,uint256 strike," +
  "bool physical)";
export const SOLIDITY_QUOTE_TYPE =
  "Quote(address maker,address signer,address taker,address externalAccount,SeriesParams series,uint8 side," +
  "uint256 amount,uint256 minFill,uint256 premium,uint64 validUntil,uint256 nonce,uint256 epoch)" +
  SOLIDITY_SERIES_TYPE;

/** 4-byte selectors from the forge artifacts (methodIdentifiers) of hedgehood-options-core 4f0cd1e. */
export const SELECTORS: Record<string, string> = {
  setSigner: "07843c87",
  deposit: "8340f549",
  requestWithdraw: "397a1b28",
  withdraw: "f940e385",
  increaseEpoch: "ad96cd5d",
  invalidateNonces: "9423b210",
  linkMaker: "6aeac35f",
  fill: "8476ec41",
  close: "16ec32b8",
  epoch: "8b810c36",
  signerExpiry: "e57def0c",
  nonceUsed: "1647795e",
  settlerBalanceOf: "f7888aec",
  pendingWithdraw: "742e1c0f",
  linked: "f476ea2b",
  feeBps: "24a9d853",
  notionalFeeBps: "1faa35a9",
  quoteDigest: "1d971f5d",
  previewFill: "f59a193d",
  isListed: "5c86bf56",
  "core.setOperator": "558a7297",
  "core.isOperator": "b6363cf2",
  "core.balanceOf": "00fdd58e",
  "core.redeemLong": "f8ad1b36",
  "erc20.approve": "095ea7b3",
  "erc20.allowance": "dd62ed3e",
  "erc20.balanceOf": "70a08231",
};
