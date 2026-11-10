"""Protocol constants of the RfqSettler (options core v3) and the RFQ gateway."""

from __future__ import annotations

MAKER_BUYS = 0  # the taker writes (covered call / cash-secured put); the maker pays the premium and gets the long
MAKER_SELLS = 1  # the maker delivers a long it holds; the taker pays the premium (buy, or buy-to-close)

MAX_QUOTE_LIFE = 120  # RfqSettler.MAX_QUOTE_LIFE: validUntil <= block.timestamp + 120 s
WITHDRAW_DELAY = 5 * 60  # RfqSettler.WITHDRAW_DELAY
WITHDRAW_WINDOW = 24 * 3600  # RfqSettler.WITHDRAW_WINDOW: take the withdrawal within a day of it opening
BPS = 10_000

# gateway defaults (cmd/rfq-gateway; an operator may configure others)
AUCTION_WINDOW_MS = 2_000  # deadlineMs: respondBy = request time + 2 s
MIN_QUOTE_LIFE_LEFT = 15  # minLifeS: a winning quote has at least 15 s left when returned to the taker
LOGIN_TIMEOUT = 10  # seconds to answer the challenge
PING_EVERY = 10  # the gateway pings every 10 s ...
PONG_WITHIN = 5  # ... and drops a maker that does not pong within 5 s
MAX_FRAME = 16 << 10  # the gateway reads at most 16 KiB per frame
BREAKER_MAX_SILENT = 20  # unanswered requests in a row before a pause
BREAKER_MAX_FADES = 3  # fades within the window before a pause
BREAKER_FADE_WINDOW = 3600
BREAKER_COOLDOWN = 900

ZERO_ADDRESS = "0x" + "00" * 20
UINT256_MAX = 2**256 - 1
UINT64_MAX = 2**64 - 1

DOMAIN_NAME = "Hedgehood RFQ Settler"
DOMAIN_VERSION = "2"
LOGIN_PREFIX = "hedgehood-rfq-login"
