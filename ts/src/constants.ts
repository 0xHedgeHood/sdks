/** Protocol constants of the RfqSettler (options core v3) and the RFQ gateway. Mirrors hedgehood_maker.constants. */

/** The taker writes (covered call / cash-secured put); the maker pays the premium and gets the long. */
export const MAKER_BUYS = 0;
/** The maker delivers a long it holds; the taker pays the premium (buy, or buy-to-close). */
export const MAKER_SELLS = 1;
export type Side = typeof MAKER_BUYS | typeof MAKER_SELLS;

/** RfqSettler.MAX_QUOTE_LIFE: validUntil <= block.timestamp + 120 s. */
export const MAX_QUOTE_LIFE = 120;
/** RfqSettler.WITHDRAW_DELAY, seconds. */
export const WITHDRAW_DELAY = 5 * 60;
/** RfqSettler.WITHDRAW_WINDOW: take the withdrawal within a day of it opening. */
export const WITHDRAW_WINDOW = 24 * 3600;
export const BPS = 10_000n;

// gateway defaults (cmd/rfq-gateway; an operator may configure others)
/** deadlineMs: respondBy = request time + 2 s. */
export const AUCTION_WINDOW_MS = 2_000;
/** minLifeS: a winning quote has at least 15 s left when returned to the taker. */
export const MIN_QUOTE_LIFE_LEFT = 15;
/** Seconds to answer the challenge. */
export const LOGIN_TIMEOUT = 10;
/** The gateway pings every 10 s ... */
export const PING_EVERY = 10;
/** ... and drops a maker that does not pong within 5 s. */
export const PONG_WITHIN = 5;
/** The gateway reads at most 16 KiB per frame. */
export const MAX_FRAME = 16 << 10;
/** Unanswered requests in a row before a pause. */
export const BREAKER_MAX_SILENT = 20;
/** Fades within the window before a pause. */
export const BREAKER_MAX_FADES = 3;
export const BREAKER_FADE_WINDOW = 3600;
export const BREAKER_COOLDOWN = 900;

export const ZERO_ADDRESS = "0x0000000000000000000000000000000000000000" as const;
export const UINT256_MAX = (1n << 256n) - 1n;
export const UINT64_MAX = (1n << 64n) - 1n;

export const DOMAIN_NAME = "Hedgehood RFQ Settler";
export const DOMAIN_VERSION = "2";
export const LOGIN_PREFIX = "hedgehood-rfq-login";

/** One whole stock token (18 decimals). */
export const SHARE = 10n ** 18n;
/** One USDG (6 decimals). */
export const USDG = 10n ** 6n;
