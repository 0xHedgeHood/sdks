// Command hedgehood-maker is a market maker's tool for the Hedgehood options RFQ gateway (testnet only; the options
// core contracts are not yet audited):
//
//	hedgehood-maker connect      run a demo maker (fixed premium or fixed bps of the notional)
//	hedgehood-maker quote-digest the EIP-712 digest of a quote (and its signer, and the settler's own digest)
//	hedgehood-maker state        the maker's settler state (read-only)
//	hedgehood-maker balances     the maker's balances and allowances (read-only)
//	hedgehood-maker tx <name>    print an UNSIGNED setup transaction and its `cast send` line
//
// The CLI never signs or sends a transaction. The signer key (used by connect only, to log in and sign quotes) is
// read from a file (--key-file) or an environment variable (--key-env, default HEDGEHOOD_SIGNER_KEY), never from a
// flag value, and is never printed.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/0xHedgeHood/sdks/go/maker"
)

const usage = `hedgehood-maker %s -- Hedgehood options RFQ maker tool (TESTNET ONLY, contracts not audited)

Usage:
  hedgehood-maker connect      [flags]   run a demo maker against the gateway
  hedgehood-maker quote-digest [flags]   EIP-712 digest of a quote JSON (and its signer)
  hedgehood-maker state        [flags]   settler state of a maker (read-only eth_call)
  hedgehood-maker balances     [flags]   balances and allowances of a maker (read-only eth_call)
  hedgehood-maker tx <name>    [flags]   print an UNSIGNED transaction: %s
  hedgehood-maker version

Run "hedgehood-maker <command> -h" for the flags of a command. Nothing here signs or sends a transaction.
`

type env struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(env{ctx, os.Stdout, os.Stderr, os.Getenv}, os.Args[1:]))
}

func run(e env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(e.stderr, usage, maker.Version, txNames())
		return 2
	}
	var err error
	switch args[0] {
	case "connect":
		err = cmdConnect(e, args[1:])
	case "quote-digest":
		err = cmdQuoteDigest(e, args[1:])
	case "state":
		err = cmdState(e, args[1:])
	case "balances":
		err = cmdBalances(e, args[1:])
	case "tx":
		err = cmdTx(e, args[1:])
	case "version":
		fmt.Fprintln(e.stdout, maker.Version)
	case "-h", "--help", "help":
		fmt.Fprintf(e.stdout, usage, maker.Version, txNames())
	default:
		fmt.Fprintf(e.stderr, "unknown command %q\n\n", args[0])
		fmt.Fprintf(e.stderr, usage, maker.Version, txNames())
		return 2
	}
	if err == errUsage {
		return 2
	}
	if err != nil {
		fmt.Fprintln(e.stderr, "error:", err)
		return 1
	}
	return 0
}
