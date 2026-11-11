package maker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// Transport sends one JSON-RPC request and returns its result. Swap it in tests.
type Transport func(ctx context.Context, method string, params []any) (json.RawMessage, error)

// RPCError is a JSON-RPC error answer (e.g. an eth_call that reverted).
type RPCError struct {
	Method  string
	Code    int
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("%s: rpc error %d: %s", e.Method, e.Code, e.Message)
}

// HTTPTransport is a plain JSON-RPC over HTTP POST transport.
func HTTPTransport(url string, client *http.Client) Transport {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var ids atomic.Int64
	return func(ctx context.Context, method string, params []any) (json.RawMessage, error) {
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": ids.Add(1), "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("user-agent", "hedgehood-maker-go/"+Version)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, err
		}
		var out struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, fmt.Errorf("%s: HTTP %d, not JSON-RPC", method, resp.StatusCode)
		}
		if out.Error != nil {
			return nil, &RPCError{method, out.Error.Code, out.Error.Message}
		}
		return out.Result, nil
	}
}

// Reader makes read-only eth_calls against one deployment: the settler and core state a maker checks before and
// while quoting. It only ever sends eth_call and eth_chainId; it cannot sign or send anything.
type Reader struct {
	Settler   common.Address
	Core      common.Address
	Transport Transport
}

// NewReader reads a network over HTTP JSON-RPC (rpcURL "" = the network's public RPC).
func NewReader(n Network, rpcURL string) *Reader {
	if rpcURL == "" {
		rpcURL = n.RPC
	}
	return &Reader{Settler: n.Settler, Core: n.Core, Transport: HTTPTransport(rpcURL, nil)}
}

var readOnly = map[string]bool{"eth_call": true, "eth_chainId": true}

func (r *Reader) request(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	if !readOnly[method] {
		return nil, fmt.Errorf("reader: %s is not a read-only method", method)
	}
	return r.Transport(ctx, method, params)
}

// Call is eth_call of a method of Methods at `to`, decoded.
func (r *Reader) Call(ctx context.Context, to common.Address, name string, a ...any) ([]any, error) {
	data, err := Pack(name, a...)
	if err != nil {
		return nil, err
	}
	raw, err := r.request(ctx, "eth_call", map[string]any{"to": to, "data": hexutil.Encode(data)}, "latest")
	if err != nil {
		return nil, err
	}
	var hex string
	if err := json.Unmarshal(raw, &hex); err != nil {
		return nil, fmt.Errorf("%s: result is not a hex string", name)
	}
	out, err := hexutil.Decode(hex)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	vals, err := Methods[name].Outputs.Unpack(out)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return vals, nil
}

func (r *Reader) bigOf(ctx context.Context, to common.Address, name string, a ...any) (*big.Int, error) {
	v, err := r.Call(ctx, to, name, a...)
	if err != nil {
		return nil, err
	}
	return v[0].(*big.Int), nil
}

func (r *Reader) boolOf(ctx context.Context, to common.Address, name string, a ...any) (bool, error) {
	v, err := r.Call(ctx, to, name, a...)
	if err != nil {
		return false, err
	}
	return v[0].(bool), nil
}

func (r *Reader) core() (common.Address, error) {
	if r.Core == (common.Address{}) {
		return common.Address{}, errNoCore
	}
	return r.Core, nil
}

// ChainID is eth_chainId.
func (r *Reader) ChainID(ctx context.Context) (uint64, error) {
	raw, err := r.request(ctx, "eth_chainId")
	if err != nil {
		return 0, err
	}
	var s hexutil.Uint64
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, errors.New("eth_chainId: not a hex quantity")
	}
	return uint64(s), nil
}

// -------------------------------------------------- settler

// Epoch is settler.epoch(maker): quotes must carry it.
func (r *Reader) Epoch(ctx context.Context, maker common.Address) (*big.Int, error) {
	return r.bigOf(ctx, r.Settler, "epoch", maker)
}

// SignerExpiry is settler.signerExpiry(maker, signer) (0 = not delegated).
func (r *Reader) SignerExpiry(ctx context.Context, maker, signer common.Address) (uint64, error) {
	v, err := r.Call(ctx, r.Settler, "signerExpiry", maker, signer)
	if err != nil {
		return 0, err
	}
	return v[0].(uint64), nil
}

// NonceUsed is settler.nonceUsed(maker, nonce).
func (r *Reader) NonceUsed(ctx context.Context, maker common.Address, nonce *big.Int) (bool, error) {
	return r.boolOf(ctx, r.Settler, "nonceUsed", maker, nz(nonce))
}

// SettlerBalance is the account's settler balance of a token.
func (r *Reader) SettlerBalance(ctx context.Context, token, account common.Address) (*big.Int, error) {
	return r.bigOf(ctx, r.Settler, "settlerBalanceOf", token, account)
}

// PendingWithdraw is (amount, requestedAt) of the account's pending withdrawal of a token.
func (r *Reader) PendingWithdraw(ctx context.Context, token, account common.Address) (amount, at *big.Int, err error) {
	v, err := r.Call(ctx, r.Settler, "pendingWithdraw", token, account)
	if err != nil {
		return nil, nil, err
	}
	return v[0].(*big.Int), v[1].(*big.Int), nil
}

// Linked is settler.linked(account, maker): whether the maker's quotes may name account as externalAccount.
func (r *Reader) Linked(ctx context.Context, account, maker common.Address) (bool, error) {
	return r.boolOf(ctx, r.Settler, "linked", account, maker)
}

// Fees is (feeBps, notionalFeeBps).
func (r *Reader) Fees(ctx context.Context) (feeBps, notionalFeeBps uint64, err error) {
	f, err := r.bigOf(ctx, r.Settler, "feeBps")
	if err != nil {
		return 0, 0, err
	}
	n, err := r.bigOf(ctx, r.Settler, "notionalFeeBps")
	if err != nil {
		return 0, 0, err
	}
	return f.Uint64(), n.Uint64(), nil
}

// QuoteDigest is the settler's own digest of q: compare with Digest to catch domain or schema drift.
func (r *Reader) QuoteDigest(ctx context.Context, q Quote) (common.Hash, error) {
	v, err := r.Call(ctx, r.Settler, "quoteDigest", quoteTuple(q))
	if err != nil {
		return common.Hash{}, err
	}
	return common.Hash(v[0].([32]byte)), nil
}

// DomainSeparator is settler.DOMAIN_SEPARATOR().
func (r *Reader) DomainSeparator(ctx context.Context) (common.Hash, error) {
	v, err := r.Call(ctx, r.Settler, "DOMAIN_SEPARATOR")
	if err != nil {
		return common.Hash{}, err
	}
	return common.Hash(v[0].([32]byte)), nil
}

// PreviewFill is settler.previewFill(q, amount): (premium, fee).
func (r *Reader) PreviewFill(ctx context.Context, q Quote, amount *big.Int) (premium, fee *big.Int, err error) {
	v, err := r.Call(ctx, r.Settler, "previewFill", quoteTuple(q), nz(amount))
	if err != nil {
		return nil, nil, err
	}
	return v[0].(*big.Int), v[1].(*big.Int), nil
}

// IsListed is settler.isListed(series).
func (r *Reader) IsListed(ctx context.Context, s Series) (bool, error) {
	return r.boolOf(ctx, r.Settler, "isListed", seriesTuple(s))
}

// -------------------------------------------------- core and tokens

// IsOperator is core.isOperator(owner, spender); spender zero = the settler.
func (r *Reader) IsOperator(ctx context.Context, owner, spender common.Address) (bool, error) {
	c, err := r.core()
	if err != nil {
		return false, err
	}
	if spender == (common.Address{}) {
		spender = r.Settler
	}
	return r.boolOf(ctx, c, "core.isOperator", owner, spender)
}

// LongBalance is core.balanceOf(owner, seriesID).
func (r *Reader) LongBalance(ctx context.Context, owner common.Address, seriesID *big.Int) (*big.Int, error) {
	c, err := r.core()
	if err != nil {
		return nil, err
	}
	return r.bigOf(ctx, c, "core.balanceOf", owner, nz(seriesID))
}

// CanSell reports why the settler could not deliver amount of s from maker ("" = it can). The gateway rejects a close
// quote it cannot fill (`inventory`): check before quoting one. The settler moves the long as the maker's OptionsCore
// operator (setOperator, the SDK's setup path).
func (r *Reader) CanSell(ctx context.Context, maker common.Address, s Series, amount *big.Int) (string, error) {
	held, err := r.LongBalance(ctx, maker, SeriesID(s))
	if err != nil {
		return "", err
	}
	if held.Cmp(nz(amount)) < 0 {
		return fmt.Sprintf("holds %s of %s", held, nz(amount)), nil
	}
	ok, err := r.IsOperator(ctx, maker, common.Address{})
	if err != nil {
		return "", err
	}
	if !ok {
		return "the settler is not the maker's operator on OptionsCore (setOperator)", nil
	}
	return "", nil
}

// TokenBalance is ERC-20 balanceOf(account).
func (r *Reader) TokenBalance(ctx context.Context, token, account common.Address) (*big.Int, error) {
	return r.bigOf(ctx, token, "erc20.balanceOf", account)
}

// Allowance is ERC-20 allowance(owner, spender); spender zero = the settler.
func (r *Reader) Allowance(ctx context.Context, token, owner, spender common.Address) (*big.Int, error) {
	if spender == (common.Address{}) {
		spender = r.Settler
	}
	return r.bigOf(ctx, token, "erc20.allowance", owner, spender)
}
