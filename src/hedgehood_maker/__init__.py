"""Market-maker SDK for the Hedgehood options RFQ gateway (testnet only; contracts not yet audited)."""

from .client import LoginRefused, MakerClient, ProtocolError, Session
from .constants import MAKER_BUYS, MAKER_SELLS, MAX_QUOTE_LIFE
from .eip712 import quote_digest, recover_quote_signer, series_id, sign_quote, typed_data
from .fees import fee_of, maker_cash, premium_for_net, preview_fill
from .models import (
    RFQ,
    Decline,
    ErrorFrame,
    Fill,
    Lost,
    Quote,
    Ready,
    Rejected,
    Series,
    ValidationError,
    Won,
)
from .networks import TESTNET, Network, Underlying
from .nonces import NonceManager
from .settler import SettlerTxs, UnsignedTx

__version__ = "0.1.0"

__all__ = [
    "MAKER_BUYS", "MAKER_SELLS", "MAX_QUOTE_LIFE", "RFQ", "TESTNET", "Decline", "ErrorFrame", "Fill", "LoginRefused",
    "Lost", "MakerClient", "Network", "NonceManager", "ProtocolError", "Quote", "Ready", "Rejected", "Series",
    "Session", "SettlerTxs", "Underlying", "UnsignedTx", "ValidationError", "Won", "fee_of", "maker_cash",
    "premium_for_net", "preview_fill", "quote_digest", "recover_quote_signer", "series_id", "sign_quote", "typed_data",
]
