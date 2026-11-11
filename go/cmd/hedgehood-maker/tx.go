package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/0xHedgeHood/hedgehood-maker-sdk/go/maker"
)

// txSpec is one `tx` subcommand: its flags and the transactions it builds. Every builder only encodes calldata.
type txSpec struct {
	help  string
	flags func(fs *flag.FlagSet) func(b maker.Txs, net maker.Network) ([]maker.UnsignedTx, error)
}

var txSpecs = map[string]txSpec{
	"setSigner": {"from the maker: let a hot key sign quotes and log in until an expiry", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		m := fs.String("maker", "", "maker address, the sender (required)")
		s := fs.String("signer", "", "hot signer address (required)")
		exp := fs.Uint64("expires", 0, "expiry, unix seconds (default: now + --days)")
		days := fs.Float64("days", 7, "lifetime in days when --expires is not given")
		revoke := fs.Bool("revoke", false, "revoke the signer (expiry 0)")
		return func(b maker.Txs, _ maker.Network) ([]maker.UnsignedTx, error) {
			mk, err := addr(*m, "--maker")
			if err != nil {
				return nil, err
			}
			sg, err := addr(*s, "--signer")
			if err != nil {
				return nil, err
			}
			at := *exp
			switch {
			case *revoke:
				at = 0
			case at == 0:
				if *days <= 0 {
					return nil, errors.New("--days must be positive")
				}
				at = uint64(time.Now().Add(time.Duration(*days * float64(24*time.Hour))).Unix())
			}
			return one(b.SetSigner(mk, sg, at))
		}
	}},
	"deposit": {"credit a settler balance (approve the settler first)", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		from := fs.String("from", "", "sender, whose tokens move (required)")
		acct := fs.String("account", "", "account credited (default: --from)")
		token, amt, dec, raw := tokenAmountFlags(fs)
		return func(b maker.Txs, net maker.Network) ([]maker.UnsignedTx, error) {
			f, err := addr(*from, "--from")
			if err != nil {
				return nil, err
			}
			a := f
			if *acct != "" {
				if a, err = addr(*acct, "--account"); err != nil {
					return nil, err
				}
			}
			t, v, err := tokenAmount(net, *token, *amt, *dec, *raw, false)
			if err != nil {
				return nil, err
			}
			return one(b.Deposit(f, t, a, v))
		}
	}},
	"requestWithdraw": {"start the 5 min withdrawal delay", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		m := fs.String("maker", "", "maker address, the sender (required)")
		token, amt, dec, raw := tokenAmountFlags(fs)
		return func(b maker.Txs, net maker.Network) ([]maker.UnsignedTx, error) {
			mk, err := addr(*m, "--maker")
			if err != nil {
				return nil, err
			}
			t, v, err := tokenAmount(net, *token, *amt, *dec, *raw, false)
			if err != nil {
				return nil, err
			}
			return one(b.RequestWithdraw(mk, t, v))
		}
	}},
	"withdraw": {"take a requested withdrawal (5 min to 1 day + 5 min after the request)", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		m := fs.String("maker", "", "maker address, the sender (required)")
		token := fs.String("token", "", "token (default: the network's USDG)")
		to := fs.String("to", "", "receiver (default: --maker)")
		return func(b maker.Txs, net maker.Network) ([]maker.UnsignedTx, error) {
			mk, err := addr(*m, "--maker")
			if err != nil {
				return nil, err
			}
			t, _, err := tokenAmount(net, *token, "0", 0, true, false)
			if err != nil {
				return nil, err
			}
			r := mk
			if *to != "" {
				if r, err = addr(*to, "--to"); err != nil {
					return nil, err
				}
			}
			return one(b.Withdraw(mk, t, r))
		}
	}},
	"increaseEpoch": {"void every outstanding quote of the maker (after a win this counts as a fade)", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		m := fs.String("maker", "", "maker address (required)")
		from := fs.String("from", "", "sender: the maker or a live signer (default: --maker)")
		return func(b maker.Txs, _ maker.Network) ([]maker.UnsignedTx, error) {
			mk, err := addr(*m, "--maker")
			if err != nil {
				return nil, err
			}
			f, err := optAddr(*from, "--from")
			if err != nil {
				return nil, err
			}
			return one(b.IncreaseEpoch(f, mk))
		}
	}},
	"invalidateNonces": {"burn chosen nonces (one transaction per slot)", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		m := fs.String("maker", "", "maker address (required)")
		from := fs.String("from", "", "sender: the maker or a live signer (default: --maker)")
		nonces := fs.String("nonces", "", "comma-separated nonces to burn")
		slot := fs.String("slot", "", "nonce slot (with --mask, instead of --nonces)")
		mask := fs.String("mask", "", "bit mask within the slot, decimal or 0x hex")
		return func(b maker.Txs, _ maker.Network) ([]maker.UnsignedTx, error) {
			mk, err := addr(*m, "--maker")
			if err != nil {
				return nil, err
			}
			f, err := optAddr(*from, "--from")
			if err != nil {
				return nil, err
			}
			masks := map[string]*big.Int{}
			switch {
			case *nonces != "" && *slot == "" && *mask == "":
				var ns []*big.Int
				for _, s := range strings.Split(*nonces, ",") {
					v, err := maker.ParseUint256(strings.TrimSpace(s))
					if err != nil {
						return nil, fmt.Errorf("--nonces: %w", err)
					}
					ns = append(ns, v)
				}
				masks = maker.NonceMasks(ns)
			case *nonces == "" && *slot != "" && *mask != "":
				mv, ok := new(big.Int).SetString(*mask, 0)
				if !ok || mv.Sign() <= 0 {
					return nil, errors.New("--mask: not a positive integer")
				}
				masks[*slot] = mv
			default:
				return nil, errors.New("pass --nonces, or --slot and --mask")
			}
			slots := make([]string, 0, len(masks))
			for s := range masks {
				slots = append(slots, s)
			}
			sort.Strings(slots)
			var out []maker.UnsignedTx
			for _, s := range slots {
				sv, err := maker.ParseUint256(s)
				if err != nil {
					return nil, fmt.Errorf("--slot: %w", err)
				}
				tx, err := b.InvalidateNonces(f, mk, sv, masks[s])
				if err != nil {
					return nil, err
				}
				out = append(out, tx)
			}
			return out, nil
		}
	}},
	"linkMaker": {"from a paying wallet: let the maker's quotes name it as externalAccount", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		acct := fs.String("account", "", "the paying wallet, the sender (required)")
		m := fs.String("maker", "", "maker address (required)")
		unlink := fs.Bool("unlink", false, "remove the link")
		return func(b maker.Txs, _ maker.Network) ([]maker.UnsignedTx, error) {
			a, err := addr(*acct, "--account")
			if err != nil {
				return nil, err
			}
			mk, err := addr(*m, "--maker")
			if err != nil {
				return nil, err
			}
			return one(b.LinkMaker(a, mk, !*unlink))
		}
	}},
	"approve": {"ERC-20 approve of the settler (or another spender)", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		owner := fs.String("owner", "", "token owner, the sender (required)")
		spender := fs.String("spender", "", "spender (default: the settler)")
		token, amt, dec, raw := tokenAmountFlags(fs)
		return func(b maker.Txs, net maker.Network) ([]maker.UnsignedTx, error) {
			o, err := addr(*owner, "--owner")
			if err != nil {
				return nil, err
			}
			sp, err := optAddr(*spender, "--spender")
			if err != nil {
				return nil, err
			}
			t, v, err := tokenAmount(net, *token, *amt, *dec, *raw, true)
			if err != nil {
				return nil, err
			}
			return one(b.Approve(o, t, sp, v))
		}
	}},
	"setOperator": {"OptionsCore.setOperator: let the settler deliver your longs (MakerSells)", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		m := fs.String("maker", "", "maker address, the sender (required)")
		op := fs.String("operator", "", "operator (default: the settler)")
		revoke := fs.Bool("revoke", false, "revoke the approval")
		return func(b maker.Txs, _ maker.Network) ([]maker.UnsignedTx, error) {
			mk, err := addr(*m, "--maker")
			if err != nil {
				return nil, err
			}
			o, err := optAddr(*op, "--operator")
			if err != nil {
				return nil, err
			}
			return one(b.SetOperator(mk, o, !*revoke))
		}
	}},
	"redeemLong": {"after settlement: burn longs and receive the payout", func(fs *flag.FlagSet) func(maker.Txs, maker.Network) ([]maker.UnsignedTx, error) {
		series := fs.String("series", "", "file with the series JSON as the gateway carries it (required)")
		amt := fs.String("amount", "", "long amount in tokens (18 decimals; required)")
		raw := fs.Bool("raw", false, "--amount is an integer in raw units")
		onBehalf := fs.String("on-behalf", "", "holder of the longs, the sender (required)")
		receiver := fs.String("receiver", "", "receiver of the payout (default: --on-behalf)")
		return func(b maker.Txs, _ maker.Network) ([]maker.UnsignedTx, error) {
			if err := required(*series, "--series"); err != nil {
				return nil, err
			}
			sb, err := os.ReadFile(*series)
			if err != nil {
				return nil, err
			}
			var s maker.Series
			if err := json.Unmarshal(sb, &s); err != nil {
				return nil, err
			}
			v, err := amount(*amt, 18, *raw, false, "--amount")
			if err != nil {
				return nil, err
			}
			ob, err := addr(*onBehalf, "--on-behalf")
			if err != nil {
				return nil, err
			}
			rc := ob
			if *receiver != "" {
				if rc, err = addr(*receiver, "--receiver"); err != nil {
					return nil, err
				}
			}
			return one(b.RedeemLong(s, v, ob, rc))
		}
	}},
}

func one(tx maker.UnsignedTx, err error) ([]maker.UnsignedTx, error) {
	if err != nil {
		return nil, err
	}
	return []maker.UnsignedTx{tx}, nil
}

func tokenAmountFlags(fs *flag.FlagSet) (token, amt *string, dec *int, raw *bool) {
	token = fs.String("token", "", "token (default: the network's USDG)")
	amt = fs.String("amount", "", "amount in token units, e.g. 1000 or 12.5 (required)")
	dec = fs.Int("decimals", 6, "token decimals for --amount")
	raw = fs.Bool("raw", false, "--amount is an integer in raw units")
	return
}

func tokenAmount(net maker.Network, token, amt string, dec int, raw, allowMax bool) (common.Address, *big.Int, error) {
	t := net.QuoteToken
	if token != "" {
		var err error
		if t, err = addr(token, "--token"); err != nil {
			return common.Address{}, nil, err
		}
	}
	v, err := amount(amt, dec, raw, allowMax, "--amount")
	return t, v, err
}

func txNames() string {
	names := make([]string, 0, len(txSpecs))
	for n := range txSpecs {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// cmdTx prints unsigned transactions: review them, then sign and send each yourself (wallet, Safe, or the printed
// `cast send` line with your own keystore).
func cmdTx(e env, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(e.stderr, "usage: hedgehood-maker tx <name> [flags]\n\n")
		names := strings.Split(txNames(), ", ")
		for _, n := range names {
			fmt.Fprintf(e.stderr, "  %-17s %s\n", n, txSpecs[n].help)
		}
		return errUsage
	}
	spec, ok := txSpecs[args[0]]
	if !ok {
		return fmt.Errorf("unknown transaction %q (one of: %s)", args[0], txNames())
	}
	fs := flag.NewFlagSet("tx "+args[0], flag.ContinueOnError)
	var n netFlags
	n.register(fs)
	asJSON := fs.Bool("json", false, "print the transactions as a JSON array only")
	build := spec.flags(fs)
	if err := parse(fs, args[1:], e.stderr); err != nil {
		return err
	}
	net, err := n.resolve()
	if err != nil {
		return err
	}
	txs, err := build(maker.TxsFor(net), net)
	if err != nil {
		return err
	}
	if *asJSON {
		type row struct {
			ChainID     uint64          `json:"chainId"`
			To          common.Address  `json:"to"`
			Data        string          `json:"data"`
			Value       string          `json:"value"`
			From        *common.Address `json:"from,omitempty"`
			Description string          `json:"description"`
			Cast        string          `json:"cast"`
		}
		rows := make([]row, len(txs))
		for i, t := range txs {
			rows[i] = row{t.ChainID, t.To, hexutil.Encode(t.Data), hexutil.EncodeBig(t.Value), nil, t.Description, t.Cast(net.RPC)}
			if t.Sender != (common.Address{}) {
				s := t.Sender
				rows[i].From = &s
			}
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", " ")
		enc.SetEscapeHTML(false)
		return enc.Encode(rows)
	}
	fmt.Fprintf(e.stdout, "# %s (chain %d) -- TESTNET ONLY, contracts not audited. UNSIGNED: review, then send yourself.\n",
		net.Name, net.ChainID)
	fmt.Fprintf(e.stdout, "# settler %s  core %s  USDG %s\n\n", net.Settler.Hex(), net.Core.Hex(), net.QuoteToken.Hex())
	for i, t := range txs {
		j, _ := json.Marshal(t)
		from := "<the sender named above>"
		if t.Sender != (common.Address{}) {
			from = t.Sender.Hex()
		}
		fmt.Fprintf(e.stdout, "%d. %s\n   from %s\n   %s\n   %s\n\n", i+1, t.Description, from, j, t.Cast(net.RPC))
	}
	return nil
}
