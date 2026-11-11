package maker

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// Calldata for the maker's on-chain setup, as UNSIGNED transactions. Nothing here signs or sends: hand the result to
// your own wallet, keystore or multisig (each UnsignedTx also renders as a `cast send` line to run yourself).
//
// Onboarding, in order (docs/MAKER_GUIDE.md):
//
//  1. SetSigner(hotKey, expiresAt) from the maker: lets a hot key sign quotes and log in for the maker.
//  2. Funding, either
//     a. settler balance: Approve(usdg, amount) + Deposit(usdg, maker, amount), or
//     b. wallet by allowance: Approve(usdg, amount) from the paying wallet, plus LinkMaker(maker, true) from that
//     wallet when it is not the maker (quotes then carry externalAccount = that wallet).
//  3. For MakerSells quotes (selling longs you hold): SetOperator(settler, true) on OptionsCore.

// UnsignedTx is a transaction to review, sign and send yourself. Sender is who must send it (zero = not known).
type UnsignedTx struct {
	ChainID     uint64
	To          common.Address
	Data        []byte
	Value       *big.Int
	Sender      common.Address
	Description string
	Function    string   // canonical signature, e.g. "setSigner(address,uint64)"
	CastArgs    []string // the arguments as `cast send` takes them
}

// MarshalJSON is {chainId, to, data, value, from?}: add nonce and gas, then sign with your own key.
func (t UnsignedTx) MarshalJSON() ([]byte, error) {
	m := map[string]any{"chainId": t.ChainID, "to": t.To, "data": hexutil.Encode(t.Data),
		"value": hexutil.EncodeBig(nz(t.Value))}
	if t.Sender != (common.Address{}) {
		m["from"] = t.Sender
	}
	return json.Marshal(m)
}

// Cast is the equivalent Foundry command, to run yourself with your own keystore (rpc "" = "$RPC").
func (t UnsignedTx) Cast(rpc string) string {
	if rpc == "" {
		rpc = "$RPC"
	}
	parts := []string{"cast send", t.To.Hex(), `"` + t.Function + `"`}
	for _, a := range t.CastArgs {
		parts = append(parts, `"`+a+`"`)
	}
	parts = append(parts, "--rpc-url", rpc, "--account <your-keystore>")
	return strings.Join(parts, " ")
}

// Txs builds the transactions for one deployment's settler and core.
type Txs struct {
	ChainID uint64
	Settler common.Address
	Core    common.Address // zero = not set (SetOperator and RedeemLong then fail)
}

// TxsFor is the builder for a network.
func TxsFor(n Network) Txs { return Txs{n.ChainID, n.Settler, n.Core} }

func (b Txs) tx(to common.Address, name string, desc string, sender common.Address, castArgs []string, a ...any) (UnsignedTx, error) {
	data, err := Pack(name, a...)
	if err != nil {
		return UnsignedTx{}, err
	}
	return UnsignedTx{ChainID: b.ChainID, To: to, Data: data, Value: new(big.Int), Sender: sender, Description: desc,
		Function: Methods[name].Sig, CastArgs: castArgs}, nil
}

func castBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func castSeries(s Series) string {
	return fmt.Sprintf("(%s,%s,%s,%d,%s,%s,%s)", s.Underlying.Hex(), s.Quote.Hex(), s.Oracle.Hex(), s.Expiry,
		castBool(s.IsPut), nz(s.Strike).String(), castBool(s.Physical))
}

func uint256(v *big.Int, name string) (*big.Int, error) {
	if v == nil || v.Sign() < 0 || v.BitLen() > 256 {
		return nil, invalid("%s: not a uint256", name)
	}
	return v, nil
}

// -------------------------------------------------- settler

// SetSigner, from the maker: `signer` may sign quotes (and log in) for it until expiresAt (unix s); 0 revokes. A quote
// is accepted only while the signer outlives its validUntil: rotate before expiry.
func (b Txs) SetSigner(maker, signer common.Address, expiresAt uint64) (UnsignedTx, error) {
	d := fmt.Sprintf("setSigner(%s, %d)", signer.Hex(), expiresAt)
	if expiresAt == 0 {
		d += " -- revoke"
	}
	return b.tx(b.Settler, "setSigner", d, maker, []string{signer.Hex(), fmt.Sprint(expiresAt)}, signer, expiresAt)
}

// Deposit credits `account`'s settler balance (anyone may deposit for anyone; approve the settler first).
func (b Txs) Deposit(sender, token, account common.Address, amount *big.Int) (UnsignedTx, error) {
	a, err := uint256(amount, "amount")
	if err != nil {
		return UnsignedTx{}, err
	}
	return b.tx(b.Settler, "deposit", fmt.Sprintf("deposit %s raw of %s for %s", a, token.Hex(), account.Hex()), sender,
		[]string{token.Hex(), account.Hex(), a.String()}, token, account, a)
}

// RequestWithdraw starts the 5 min delay; the requested amount keeps backing fills until Withdraw.
func (b Txs) RequestWithdraw(maker, token common.Address, amount *big.Int) (UnsignedTx, error) {
	a, err := uint256(amount, "amount")
	if err != nil {
		return UnsignedTx{}, err
	}
	return b.tx(b.Settler, "requestWithdraw",
		fmt.Sprintf("requestWithdraw %s raw of %s (withdraw between 5 min and 1 day + 5 min later)", a, token.Hex()),
		maker, []string{token.Hex(), a.String()}, token, a)
}

// Withdraw takes the requested amount of `token` to `to`.
func (b Txs) Withdraw(maker, token, to common.Address) (UnsignedTx, error) {
	return b.tx(b.Settler, "withdraw", fmt.Sprintf("withdraw the requested %s to %s", token.Hex(), to.Hex()), maker,
		[]string{token.Hex(), to.Hex()}, token, to)
}

// IncreaseEpoch voids every outstanding quote of `maker` (sent by the maker or a live signer; sender zero = the
// maker). Doing this to back out of a quote that already WON counts as a fade on the gateway scorecard.
func (b Txs) IncreaseEpoch(sender, maker common.Address) (UnsignedTx, error) {
	if sender == (common.Address{}) {
		sender = maker
	}
	return b.tx(b.Settler, "increaseEpoch", fmt.Sprintf("increaseEpoch(%s): cancel all quotes", maker.Hex()), sender,
		[]string{maker.Hex()}, maker)
}

// InvalidateNonces burns the bits of `mask` in nonce slot `slot` (see NonceMasks; sender zero = the maker).
func (b Txs) InvalidateNonces(sender, maker common.Address, slot, mask *big.Int) (UnsignedTx, error) {
	if sender == (common.Address{}) {
		sender = maker
	}
	s, err := uint256(slot, "slot")
	if err != nil {
		return UnsignedTx{}, err
	}
	m, err := uint256(mask, "mask")
	if err != nil {
		return UnsignedTx{}, err
	}
	return b.tx(b.Settler, "invalidateNonces",
		fmt.Sprintf("invalidateNonces(%s, slot %s, mask 0x%s)", maker.Hex(), s, m.Text(16)), sender,
		[]string{maker.Hex(), s.String(), m.String()}, maker, s, m)
}

// LinkMaker, from a paying wallet that is not the maker: lets the maker's quotes name it as externalAccount.
func (b Txs) LinkMaker(account, maker common.Address, linked bool) (UnsignedTx, error) {
	return b.tx(b.Settler, "linkMaker", fmt.Sprintf("linkMaker(%s, %t)", maker.Hex(), linked), account,
		[]string{maker.Hex(), castBool(linked)}, maker, linked)
}

// -------------------------------------------------- core

var errNoCore = errors.New("core address not set")

// SetOperator is OptionsCore.setOperator(operator, approved) from the maker; operator zero = the settler, which then
// delivers your longs on MakerSells fills.
func (b Txs) SetOperator(maker, operator common.Address, approved bool) (UnsignedTx, error) {
	if b.Core == (common.Address{}) {
		return UnsignedTx{}, errNoCore
	}
	if operator == (common.Address{}) {
		operator = b.Settler
	}
	return b.tx(b.Core, "core.setOperator", fmt.Sprintf("core.setOperator(%s, %t)", operator.Hex(), approved), maker,
		[]string{operator.Hex(), castBool(approved)}, operator, approved)
}

// RedeemLong, after settlement: burn `amount` long of onBehalf and pay receiver (sent by onBehalf).
func (b Txs) RedeemLong(s Series, amount *big.Int, onBehalf, receiver common.Address) (UnsignedTx, error) {
	if b.Core == (common.Address{}) {
		return UnsignedTx{}, errNoCore
	}
	a, err := uint256(amount, "amount")
	if err != nil {
		return UnsignedTx{}, err
	}
	return b.tx(b.Core, "core.redeemLong", fmt.Sprintf("core.redeemLong(series %s, %s)", SeriesID(s), a), onBehalf,
		[]string{castSeries(s), a.String(), onBehalf.Hex(), receiver.Hex()}, seriesTuple(s), a, onBehalf, receiver)
}

// -------------------------------------------------- ERC-20

// Approve is ERC-20 approve(spender, amount) from owner; spender zero = the settler.
func (b Txs) Approve(owner, token, spender common.Address, amount *big.Int) (UnsignedTx, error) {
	if spender == (common.Address{}) {
		spender = b.Settler
	}
	a, err := uint256(amount, "amount")
	if err != nil {
		return UnsignedTx{}, err
	}
	return b.tx(token, "erc20.approve", fmt.Sprintf("%s.approve(%s, %s)", token.Hex(), spender.Hex(), a), owner,
		[]string{spender.Hex(), a.String()}, spender, a)
}
