package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/0xHedgeHood/sdks/go/maker"
)

func readInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(stdin, 1<<20))
	}
	return os.ReadFile(path)
}

// cmdQuoteDigest prints the EIP-712 digest of a quote, and, with a signature, its signer; --check also asks the
// settler for its own quoteDigest (eth_call).
func cmdQuoteDigest(e env, args []string) error {
	fs := flag.NewFlagSet("quote-digest", flag.ContinueOnError)
	var n netFlags
	n.register(fs)
	file := fs.String("quote", "-", `quote JSON as the gateway carries it, or a whole {"type":"quote",...} frame ("-" = stdin)`)
	sigFlag := fs.String("signature", "", "0x signature to recover the signer from (default: the frame's)")
	check := fs.Bool("check", false, "also compare with settler.quoteDigest over --rpc (read-only eth_call)")
	if err := parse(fs, args, e.stderr); err != nil {
		return err
	}
	net, err := n.resolve()
	if err != nil {
		return err
	}
	b, err := readInput(*file, os.Stdin)
	if err != nil {
		return err
	}
	var frame struct {
		Type      string          `json:"type"`
		Quote     json.RawMessage `json:"quote"`
		Signature string          `json:"signature"`
	}
	_ = json.Unmarshal(b, &frame)
	if frame.Quote != nil {
		b = frame.Quote
	}
	var q maker.Quote
	if err := json.Unmarshal(b, &q); err != nil {
		return err
	}
	sig := *sigFlag
	if sig == "" {
		sig = frame.Signature
	}
	fmt.Fprintf(e.stdout, "chainId          %d\nsettler          %s\n", net.ChainID, net.Settler.Hex())
	fmt.Fprintf(e.stdout, "domainSeparator  %s\n", maker.DomainSeparator(net.ChainID, net.Settler).Hex())
	fmt.Fprintf(e.stdout, "structHash       %s\n", maker.StructHash(q).Hex())
	digest := maker.Digest(q, net.ChainID, net.Settler)
	fmt.Fprintf(e.stdout, "digest           %s\n", digest.Hex())
	fmt.Fprintf(e.stdout, "seriesId         %s\n", q.Series.ID())
	if sig != "" {
		raw, err := hexutil.Decode(sig)
		if err != nil {
			return fmt.Errorf("--signature: %w", err)
		}
		who, err := maker.Recover(digest, raw)
		if err != nil {
			return fmt.Errorf("signature: %w", err)
		}
		ok := "matches quote.signer"
		if who != q.Signer {
			ok = "DOES NOT match quote.signer " + q.Signer.Hex()
		}
		fmt.Fprintf(e.stdout, "recoveredSigner  %s (%s)\n", who.Hex(), ok)
	}
	if *check {
		got, err := maker.NewReader(net, "").QuoteDigest(e.ctx, q)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "settlerDigest    %s\n", got.Hex())
		if got != digest {
			return errors.New("the settler's digest differs: wrong --settler / --chain-id, or schema drift")
		}
	}
	return nil
}

type makerFlags struct {
	netFlags
	maker, signer, external string
}

func (m *makerFlags) register(fs *flag.FlagSet) {
	m.netFlags.register(fs)
	fs.StringVar(&m.maker, "maker", "", "maker address (required)")
	fs.StringVar(&m.signer, "signer", "", "a delegated hot signer to check")
	fs.StringVar(&m.external, "external", "", "an external paying wallet to check")
}

type readCtx struct {
	net      maker.Network
	r        *maker.Reader
	maker    common.Address
	signer   common.Address
	external common.Address
}

func (m *makerFlags) open(e env) (*readCtx, error) {
	net, err := m.resolve()
	if err != nil {
		return nil, err
	}
	if err := required(m.maker, "--maker"); err != nil {
		return nil, err
	}
	rc := &readCtx{net: net, r: maker.NewReader(net, "")}
	if rc.maker, err = addr(m.maker, "--maker"); err != nil {
		return nil, err
	}
	if rc.signer, err = optAddr(m.signer, "--signer"); err != nil {
		return nil, err
	}
	if rc.external, err = optAddr(m.external, "--external"); err != nil {
		return nil, err
	}
	id, err := rc.r.ChainID(e.ctx)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(e.stdout, "# %s (chain %d) via %s -- read-only, TESTNET ONLY\n# settler %s  core %s  USDG %s\n",
		net.Name, id, net.RPC, net.Settler.Hex(), net.Core.Hex(), net.QuoteToken.Hex())
	if id != net.ChainID {
		return nil, fmt.Errorf("the RPC is on chain %d, expected %d", id, net.ChainID)
	}
	return rc, nil
}

func usd(v *big.Int) string { return maker.FormatUnits(v, 6) }

// cmdState prints the settler's view of a maker: fees, epoch, delegation, links, operator approval.
func cmdState(e env, args []string) error {
	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	var m makerFlags
	m.register(fs)
	if err := parse(fs, args, e.stderr); err != nil {
		return err
	}
	rc, err := m.open(e)
	if err != nil {
		return err
	}
	ctx, r := e.ctx, rc.r
	fee, notionalCap, err := r.Fees(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "fee                      %d bps of the premium, capped at %d bps of amount x strike\n", fee, notionalCap)
	ep, err := r.Epoch(ctx, rc.maker)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "epoch(maker)             %s\n", ep)
	if rc.signer != (common.Address{}) {
		exp, err := r.SignerExpiry(ctx, rc.maker, rc.signer)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "signerExpiry(maker, %s) %d\n", rc.signer.Hex(), exp)
	}
	if rc.external != (common.Address{}) && rc.external != rc.maker {
		ok, err := r.Linked(ctx, rc.external, rc.maker)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "linked(%s, maker) %t\n", rc.external.Hex(), ok)
	}
	op, err := r.IsOperator(ctx, rc.maker, common.Address{})
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "core.isOperator(maker, settler) %t\n", op)
	return nil
}

// cmdBalances prints what backs a maker's quotes: settler balance, pending withdrawal, wallet balance and allowances,
// and the longs it holds of given series.
func cmdBalances(e env, args []string) error {
	fs := flag.NewFlagSet("balances", flag.ContinueOnError)
	var m makerFlags
	m.register(fs)
	ids := fs.String("series-id", "", "comma-separated series ids (OptionsCore.idOf) to show long balances of")
	if err := parse(fs, args, e.stderr); err != nil {
		return err
	}
	rc, err := m.open(e)
	if err != nil {
		return err
	}
	ctx, r, usdg := e.ctx, rc.r, rc.net.QuoteToken
	bal, err := r.SettlerBalance(ctx, usdg, rc.maker)
	if err != nil {
		return err
	}
	amt, at, err := r.PendingWithdraw(ctx, usdg, rc.maker)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "settler balance          %s USDG (pending withdraw %s, requested at %s)\n", usd(bal), usd(amt), at)
	wallets := []common.Address{rc.maker}
	if rc.external != (common.Address{}) && rc.external != rc.maker {
		wallets = append(wallets, rc.external)
	}
	for _, w := range wallets {
		b, err := r.TokenBalance(ctx, usdg, w)
		if err != nil {
			return err
		}
		a, err := r.Allowance(ctx, usdg, w, common.Address{})
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "wallet %s %s USDG, allowance to the settler %s\n", w.Hex(), usd(b), usd(a))
	}
	if *ids != "" {
		for _, s := range strings.Split(*ids, ",") {
			id, err := maker.ParseUint256(strings.TrimSpace(s))
			if err != nil {
				return fmt.Errorf("--series-id: %w", err)
			}
			l, err := r.LongBalance(ctx, rc.maker, id)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.stdout, "long %s  %s tokens\n", id, maker.FormatUnits(l, 18))
		}
	}
	return nil
}
