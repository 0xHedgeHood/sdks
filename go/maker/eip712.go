package maker

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// The EIP-712 types exactly as RfqSettler hashes them. Domain: {name "Hedgehood RFQ Settler", version "2", chainId,
// verifyingContract = the settler proxy}. The digest is the standard EIP-712 digest, so a wallet's signTypedData of
// TypedData(...) gives the same signature.
const (
	SeriesTypeString = "SeriesParams(address underlying,address quote,address oracle,uint64 expiry,bool isPut," +
		"uint256 strike,bool physical)"
	QuoteTypeString = "Quote(address maker,address signer,address taker,address externalAccount,SeriesParams series," +
		"uint8 side,uint256 amount,uint256 minFill,uint256 premium,uint64 validUntil,uint256 nonce,uint256 epoch)" +
		SeriesTypeString
	DomainTypeString = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
)

var (
	seriesTypeHash = crypto.Keccak256Hash([]byte(SeriesTypeString))
	quoteTypeHash  = crypto.Keccak256Hash([]byte(QuoteTypeString))
	domainTypeHash = crypto.Keccak256Hash([]byte(DomainTypeString))
)

func word(v *big.Int) []byte           { return common.LeftPadBytes(nz(v).Bytes(), 32) }
func addrWord(a common.Address) []byte { return common.LeftPadBytes(a.Bytes(), 32) }
func u64Word(v uint64) []byte          { return word(new(big.Int).SetUint64(v)) }
func boolWord(b bool) []byte {
	if b {
		return word(big.NewInt(1))
	}
	return word(nil)
}

func seriesWords(s Series) [][]byte {
	return [][]byte{addrWord(s.Underlying), addrWord(s.Quote), addrWord(s.Oracle), u64Word(s.Expiry),
		boolWord(s.IsPut), word(s.Strike), boolWord(s.Physical)}
}

// SeriesID is OptionsCore.idOf: uint256(keccak256(abi.encode(params))).
func SeriesID(s Series) *big.Int { return new(big.Int).SetBytes(crypto.Keccak256(seriesWords(s)...)) }

// SeriesHash is the EIP-712 hashStruct of the series.
func SeriesHash(s Series) common.Hash {
	return crypto.Keccak256Hash(append([][]byte{seriesTypeHash.Bytes()}, seriesWords(s)...)...)
}

// StructHash is the EIP-712 hashStruct of the quote.
func StructHash(q Quote) common.Hash {
	return crypto.Keccak256Hash(quoteTypeHash.Bytes(), addrWord(q.Maker), addrWord(q.Signer), addrWord(q.Taker),
		addrWord(q.ExternalAccount), SeriesHash(q.Series).Bytes(), u64Word(uint64(q.Side)), word(q.Amount),
		word(q.MinFill), word(q.Premium), u64Word(q.ValidUntil), word(q.Nonce), word(q.Epoch))
}

// DomainSeparator is RfqSettler.DOMAIN_SEPARATOR() of a settler on a chain.
func DomainSeparator(chainID uint64, settler common.Address) common.Hash {
	return crypto.Keccak256Hash(domainTypeHash.Bytes(), crypto.Keccak256([]byte(DomainName)),
		crypto.Keccak256([]byte(DomainVersion)), u64Word(chainID), addrWord(settler))
}

// Digest is RfqSettler.quoteDigest(q): what the maker's signer signs.
func Digest(q Quote, chainID uint64, settler common.Address) common.Hash {
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, DomainSeparator(chainID, settler).Bytes(), StructHash(q).Bytes())
}

// TypedData is the quote as EIP-712 typed data for a wallet (eth_signTypedData_v4, viem / ethers signTypedData).
func TypedData(q Quote, chainID uint64, settler common.Address) apitypes.TypedData {
	s := q.Series
	return apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {{Name: "name", Type: "string"}, {Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"}},
			"SeriesParams": {{Name: "underlying", Type: "address"}, {Name: "quote", Type: "address"},
				{Name: "oracle", Type: "address"}, {Name: "expiry", Type: "uint64"}, {Name: "isPut", Type: "bool"},
				{Name: "strike", Type: "uint256"}, {Name: "physical", Type: "bool"}},
			"Quote": {{Name: "maker", Type: "address"}, {Name: "signer", Type: "address"},
				{Name: "taker", Type: "address"}, {Name: "externalAccount", Type: "address"},
				{Name: "series", Type: "SeriesParams"}, {Name: "side", Type: "uint8"},
				{Name: "amount", Type: "uint256"}, {Name: "minFill", Type: "uint256"},
				{Name: "premium", Type: "uint256"}, {Name: "validUntil", Type: "uint64"},
				{Name: "nonce", Type: "uint256"}, {Name: "epoch", Type: "uint256"}},
		},
		PrimaryType: "Quote",
		Domain: apitypes.TypedDataDomain{Name: DomainName, Version: DomainVersion,
			ChainId: (*math.HexOrDecimal256)(new(big.Int).SetUint64(chainID)), VerifyingContract: settler.Hex()},
		Message: apitypes.TypedDataMessage{
			"maker": q.Maker.Hex(), "signer": q.Signer.Hex(), "taker": q.Taker.Hex(),
			"externalAccount": q.ExternalAccount.Hex(),
			"series": map[string]any{"underlying": s.Underlying.Hex(), "quote": s.Quote.Hex(), "oracle": s.Oracle.Hex(),
				"expiry": fmt.Sprint(s.Expiry), "isPut": s.IsPut, "strike": nz(s.Strike).String(), "physical": s.Physical},
			"side": fmt.Sprint(q.Side), "amount": nz(q.Amount).String(), "minFill": nz(q.MinFill).String(),
			"premium": nz(q.Premium).String(), "validUntil": fmt.Sprint(q.ValidUntil), "nonce": nz(q.Nonce).String(),
			"epoch": nz(q.Epoch).String(),
		},
	}
}

// SignDigest is the 65-byte r || s || v signature (v = 27 / 28, low s) of a 32-byte digest.
func SignDigest(digest common.Hash, key *ecdsa.PrivateKey) ([]byte, error) {
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		return nil, err
	}
	sig[64] += 27
	return sig, nil
}

// SignQuote is the signature the settler checks (SignatureChecker: ECDSA for an EOA signer).
func SignQuote(q Quote, chainID uint64, settler common.Address, key *ecdsa.PrivateKey) ([]byte, error) {
	return SignDigest(Digest(q, chainID, settler), key)
}

// Recover returns the signer of a 65-byte r || s || v signature over digest (v 27/28 or 0/1). High s is refused, as
// on chain (OpenZeppelin ECDSA) and in the gateway.
func Recover(digest common.Hash, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, errors.New("signature must be 65 bytes")
	}
	s := append([]byte(nil), sig...)
	if s[64] >= 27 {
		s[64] -= 27
	}
	if s[64] > 1 || !crypto.ValidateSignatureValues(s[64], new(big.Int).SetBytes(s[:32]), new(big.Int).SetBytes(s[32:64]), true) {
		return common.Address{}, errors.New("bad r, s or v")
	}
	pub, err := crypto.SigToPub(digest.Bytes(), s)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// RecoverQuoteSigner is the address that signed q.
func RecoverQuoteSigner(q Quote, chainID uint64, settler common.Address, sig []byte) (common.Address, error) {
	return Recover(Digest(q, chainID, settler), sig)
}

// ---------------------------------------------------------------- gateway login (EIP-191)

// LoginMessage is what the signer personal_signs to log in: hedgehood-rfq-login:<chainId>:<maker lower-case>:<nonce>.
func LoginMessage(chainID uint64, maker common.Address, nonce string) string {
	return fmt.Sprintf("%s:%d:%s:%s", LoginPrefix, chainID, strings.ToLower(maker.Hex()), nonce)
}

// SignLogin is the personal_sign signature of the login message (0x hex).
func SignLogin(chainID uint64, maker common.Address, nonce string, key *ecdsa.PrivateKey) (string, error) {
	sig, err := SignDigest(common.BytesToHash(accounts.TextHash([]byte(LoginMessage(chainID, maker, nonce)))), key)
	if err != nil {
		return "", err
	}
	return hexutil.Encode(sig), nil
}

// RecoverLogin is the address that signed a login.
func RecoverLogin(chainID uint64, maker common.Address, nonce string, sig []byte) (common.Address, error) {
	return Recover(common.BytesToHash(accounts.TextHash([]byte(LoginMessage(chainID, maker, nonce)))), sig)
}
