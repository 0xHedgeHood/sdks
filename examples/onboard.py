"""Print the UNSIGNED transactions a maker sends once before quoting on TESTNET, and (with --rpc) the current on-chain
state. Nothing is signed or sent: review the output, then send each transaction yourself from the account named in
`from` (wallet, Safe, or the printed `cast send` line with your own keystore).

    python examples/onboard.py --maker 0xMaker --signer 0xHotKey --signer-days 7 --deposit 1000
    python examples/onboard.py --maker 0xMaker --external self --allowance 1000 --sell
    python examples/onboard.py --maker 0xMaker --external 0xTreasury --allowance 5000 --rpc
"""

from __future__ import annotations

import argparse
import json
import sys
import time

from eth_utils import to_checksum_address

from hedgehood_maker import TESTNET, SettlerTxs
from hedgehood_maker.fees import USDG
from hedgehood_maker.rpc import Reader


def usd(raw: int) -> str:
    return "unlimited" if raw >= 2**255 else f"{raw / USDG:,.6f}"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--maker", required=True)
    ap.add_argument("--signer", help="hot key to delegate with setSigner")
    ap.add_argument("--signer-days", type=float, default=7.0)
    ap.add_argument("--deposit", type=float, default=0.0, help="USDG to deposit into the settler balance")
    ap.add_argument("--external", help='pay premiums from a wallet by allowance: "self" (the maker) or an address')
    ap.add_argument("--allowance", type=float, default=0.0, help="USDG allowance for the external account")
    ap.add_argument("--sell", action="store_true", help="also let the settler deliver your longs (MakerSells)")
    ap.add_argument("--rpc", nargs="?", const=TESTNET.rpc, help="print the current state (read-only eth_call)")
    ap.add_argument("--json", action="store_true", help="print the transactions as JSON only")
    a = ap.parse_args()

    maker = to_checksum_address(a.maker)
    b = SettlerTxs.for_network(TESTNET)
    usdg = TESTNET.quote_token
    txs = []
    if a.signer:
        txs.append(b.set_signer(a.signer, int(time.time() + a.signer_days * 86400), maker=maker))
    if a.deposit:
        raw = int(a.deposit * USDG)
        txs += [b.approve(usdg, raw, owner=maker), b.deposit(usdg, maker, raw, sender=maker)]
    if a.external:
        ext = maker if a.external == "self" else to_checksum_address(a.external)
        if a.allowance:
            txs.append(b.approve(usdg, int(a.allowance * USDG), owner=ext))
        if ext != maker:
            txs.append(b.link_maker(maker, True, account=ext))
    if a.sell:
        txs.append(b.set_operator(maker=maker))

    if a.json:
        print(json.dumps([dict(t.to_dict(), description=t.description) for t in txs], indent=1))
    else:
        print(f"# {TESTNET.name} (chain {TESTNET.chain_id}) -- TESTNET ONLY, contracts not audited")
        print(f"# settler {TESTNET.settler}  core {TESTNET.core}  USDG {usdg}\n")
        for i, t in enumerate(txs, 1):
            print(f"{i}. {t.description}\n   from {t.sender or '<the sender named above>'}")
            print(f"   {json.dumps(t.to_dict())}\n   {t.cast(TESTNET.rpc)}\n")
        if not txs:
            print("(no transactions: pass --signer, --deposit, --external/--allowance or --sell)")

    if a.rpc:
        r = Reader.for_network(TESTNET, rpc_url=a.rpc)
        fee, cap = r.fees()
        print(f"# state at {a.rpc}")
        print(f"fee {fee} bps of the premium, capped at {cap} bps of amount x strike")
        print(f"epoch(maker) {r.epoch(maker)}")
        amount, at = r.pending_withdraw(usdg, maker)
        print(f"settler balance {usd(r.settler_balance(usdg, maker))} USDG, pending withdraw {usd(amount)} at {at}")
        print(f"maker wallet {usd(r.token_balance(usdg, maker))} USDG, allowance {usd(r.allowance(usdg, maker))}")
        if a.signer:
            print(f"signerExpiry(maker, {a.signer}) {r.signer_expiry(maker, a.signer)}")
        if a.external and a.external != "self":
            ext = to_checksum_address(a.external)
            print(f"linked({ext}, maker) {r.linked(ext, maker)}, allowance {usd(r.allowance(usdg, ext))}")
        print(f"core.isOperator(maker, settler) {r.is_operator(maker)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
