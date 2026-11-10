"""Deployments. TESTNET ONLY: the options core contracts are not audited yet; mainnet addresses are TBD."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class Underlying:
    token: str  # the stock token (18 decimals)
    oracle: str  # the StockSettlementSource listed on the settler for (token, quote token)


@dataclass(frozen=True)
class Network:
    name: str
    chain_id: int
    settler: str  # RfqSettler (UUPS proxy)
    core: str  # OptionsCore (UUPS proxy)
    quote_token: str  # USDG-like stable, 6 decimals
    gateway_ws: str
    rpc: str
    underlyings: dict[str, Underlying] = field(default_factory=dict)


# Robinhood Chain testnet. Fee parameters are read from the settler (and arrive in the gateway's `ready` frame); at
# the time of writing feeBps = 1250 and notionalFeeBps = 100.
TESTNET = Network(
    name="robinhood-testnet",
    chain_id=46630,
    settler="0xda43b0933031e41D5B367E1807536FcF4ebdEE7e",
    core="0xde5ce903254E9597822D5456a82a25640285B8B4",
    quote_token="0x850DE815d41F6983Aaf09247f9a465c0aefff04C",  # TUSD (testnet USDG stand-in, 6 decimals)
    gateway_ws="wss://testnet-rfq.hedgehood.io/v1/maker/ws",
    rpc="https://rpc.testnet.chain.robinhood.com",
    underlyings={
        "TSLA": Underlying(
            token="0x6661d20C2e343Bb78B42535eB060C3058c744f3F",
            oracle="0x9e6C529CAA69d44b63df27483c57f97a42D6e898",
        ),
    },
)

NETWORKS = {"testnet": TESTNET}
