package main

import (
	"crypto/ecdsa"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0xHedgeHood/sdks/go/maker"
)

var errUsage = errors.New("usage")

// parse parses a flag set; -h prints the flags and returns errUsage.
func parse(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		fs.Usage()
		return errUsage
	}
	return nil
}

// netFlags select the deployment and let each address be overridden.
type netFlags struct {
	network, rpc, settler, core, usdg string
	chainID                           uint64
}

func (n *netFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&n.network, "network", "testnet", "deployment (testnet)")
	fs.StringVar(&n.rpc, "rpc", "", "JSON-RPC URL (default: the network's public RPC)")
	fs.StringVar(&n.settler, "settler", "", "RfqSettler address (default: the network's)")
	fs.StringVar(&n.core, "core", "", "OptionsCore address (default: the network's)")
	fs.StringVar(&n.usdg, "usdg", "", "quote token address (default: the network's)")
	fs.Uint64Var(&n.chainID, "chain-id", 0, "chain id (default: the network's)")
}

func (n *netFlags) resolve() (maker.Network, error) {
	net, ok := maker.Networks[n.network]
	if !ok {
		return maker.Network{}, fmt.Errorf("unknown network %q", n.network)
	}
	var err error
	set := func(dst *common.Address, v, name string) {
		if v != "" && err == nil {
			*dst, err = addr(v, name)
		}
	}
	set(&net.Settler, n.settler, "--settler")
	set(&net.Core, n.core, "--core")
	set(&net.QuoteToken, n.usdg, "--usdg")
	if n.rpc != "" {
		net.RPC = n.rpc
	}
	if n.chainID != 0 {
		net.ChainID = n.chainID
	}
	return net, err
}

func addr(s, name string) (common.Address, error) {
	if s == "" {
		return common.Address{}, fmt.Errorf("%s is required", name)
	}
	if !common.IsHexAddress(s) {
		return common.Address{}, fmt.Errorf("%s: not an address: %q", name, s)
	}
	return common.HexToAddress(s), nil
}

func optAddr(s, name string) (common.Address, error) {
	if s == "" {
		return common.Address{}, nil
	}
	return addr(s, name)
}

func required(v, name string) error {
	if v == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

// amount reads a decimal token amount (`raw` = an integer in raw units; "max" = 2^256 - 1 when allowMax).
func amount(s string, decimals int, raw, allowMax bool, name string) (*big.Int, error) {
	if s == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	if allowMax && s == "max" {
		return maker.MaxUint256(), nil
	}
	if raw {
		v, err := maker.ParseUint256(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return v, nil
	}
	v, err := maker.ParseUnits(s, decimals)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

// loadSigner reads the signer key from a file or an environment variable. The key, or any part of the file, never
// appears in an error or a log line.
func loadSigner(path, envName string, getenv func(string) string, stderr io.Writer) (*ecdsa.PrivateKey, error) {
	var raw, from string
	switch {
	case path != "":
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("key file %s: cannot read it", path)
		}
		if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(stderr, "warning: key file %s is readable by other users (chmod 600 it)\n", path)
		}
		raw, from = string(b), "key file "+path
	case envName != "" && getenv(envName) != "":
		raw, from = getenv(envName), "environment variable "+envName
	default:
		return nil, fmt.Errorf("no signer key: pass --key-file PATH or set %s", envName)
	}
	k, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(raw), "0x"))
	if err != nil {
		return nil, fmt.Errorf("%s: not a hex private key", from)
	}
	return k, nil
}
