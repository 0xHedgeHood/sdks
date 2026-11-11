package maker

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"math/big"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// The golden vectors of tests/goldens.py, shared with the other implementations of the same encoding:
// hedgehood-options contracts/test/core/AbiCoreGolden.t.sol (Solidity, the settler itself), hedgehood-rfq-gateway
// internal/quote/quote_test.go (Go, the gateway) and hedgehood-rfq-gateway desk/tests/test_abi_core.py (our desk).
// TestGoldensMatchThePythonFile checks these copies against that file.
const (
	gSettler   = "0x5991A2dF15A8F6A256D3Ec51E99254Cd3fb576A9"
	gChain     = uint64(46630)
	gSeriesID  = "11623297058463634065704459141464122235595256587129134766422972952495797106800"
	gDigest    = "0x15adc15e4a460be6a0dd0cde1a16c01c5d1e74d73bcc5edd7a54823a7dcfd81e"
	gSignature = "0x141373907102bdbdb3f7cecfa039b039ad6e6af54fe5a7ce0f195f6709e396ed" +
		"3360ba613c6f7d76706fa18b4c6123a20896befeeb53ec9c9a2ae8afbb29577d1b"
	gFillHash     = "0x1c808620179dc62b8a80b4ae0556db4f4ac8d99a89b035f54d6b3ce1167855d0"
	gCloseHash    = "0x7cdac40d9224ced1edd8d9a3374fb657e30f9565c08287df53a890c81dab1e36"
	gTestnetSep   = "0xb21e093b8f5159c565f78c4205db5b8f1d41abad3822682cf1c2c22a3107c7b6"
	gSolSeries    = "SeriesParams(address underlying,address quote,address oracle,uint64 expiry,bool isPut,uint256 strike,bool physical)"
	gSolQuoteHead = "Quote(address maker,address signer,address taker,address externalAccount,SeriesParams series,uint8 side,uint256 amount,uint256 minFill,uint256 premium,uint64 validUntil,uint256 nonce,uint256 epoch)"
)

// 4-byte selectors from the forge artifacts (methodIdentifiers) of hedgehood-options-core 4f0cd1e.
var gSelectors = map[string]string{
	"setSigner": "07843c87", "deposit": "8340f549", "requestWithdraw": "397a1b28", "withdraw": "f940e385",
	"increaseEpoch": "ad96cd5d", "invalidateNonces": "9423b210", "linkMaker": "6aeac35f", "fill": "8476ec41",
	"close": "16ec32b8", "epoch": "8b810c36", "signerExpiry": "e57def0c", "nonceUsed": "1647795e",
	"settlerBalanceOf": "f7888aec", "pendingWithdraw": "742e1c0f", "linked": "f476ea2b", "feeBps": "24a9d853",
	"notionalFeeBps": "1faa35a9", "quoteDigest": "1d971f5d", "previewFill": "f59a193d", "isListed": "5c86bf56",
	"core.setOperator": "558a7297", "core.isOperator": "b6363cf2", "core.balanceOf": "00fdd58e",
	"core.redeemLong": "f8ad1b36", "erc20.approve": "095ea7b3", "erc20.allowance": "dd62ed3e",
	"erc20.balanceOf": "70a08231",
}

func e18(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), Share) }

func goldenSeries() Series {
	return Series{
		Underlying: common.HexToAddress("0x267ddeE02B44FDF4D073E124869DaA5BDbF1759D"),
		Quote:      common.HexToAddress("0x850DE815d41F6983Aaf09247f9a465c0aefff04C"),
		Oracle:     common.HexToAddress("0x0767e42f22cF3D0Ee3b49d5eDAad65b62CD0B3a2"),
		Expiry:     1793826000, IsPut: false, Strike: big.NewInt(215_000_000),
	}
}

func goldenQuote() Quote {
	return Quote{
		Maker:  common.HexToAddress("0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308"),
		Signer: common.HexToAddress("0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308"),
		Taker:  common.HexToAddress("0x36437b878415EdA1a24186CF79AFffBc9ecEd298"),
		Series: goldenSeries(), Side: MakerBuys, Amount: e18(5), MinFill: new(big.Int),
		Premium: big.NewInt(5_164_250), ValidUntil: 1793766600, Nonce: big.NewInt(42), Epoch: new(big.Int),
	}
}

// key7 is the golden signer: private key 7.
func key7(t *testing.T) *ecdsa.PrivateKey {
	k, err := crypto.ToECDSA(common.LeftPadBytes([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func typedHash(t *testing.T, td apitypes.TypedData) common.Hash {
	t.Helper()
	h, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		t.Fatal(err)
	}
	return common.BytesToHash(h)
}

func TestTypeStringsAreTheSettlers(t *testing.T) {
	if SeriesTypeString != gSolSeries || QuoteTypeString != gSolQuoteHead+gSolSeries {
		t.Fatalf("type strings differ from RfqSettler.sol:\n%s\n%s", SeriesTypeString, QuoteTypeString)
	}
}

func TestDigestMatchesSolidityGatewayAndDeskGoldens(t *testing.T) {
	if got := Digest(goldenQuote(), gChain, common.HexToAddress(gSettler)); got.Hex() != gDigest {
		t.Fatalf("digest %s, want %s", got.Hex(), gDigest)
	}
}

func TestDigestEqualsGoEthereumTypedData(t *testing.T) {
	if got := typedHash(t, TypedData(goldenQuote(), gChain, common.HexToAddress(gSettler))); got.Hex() != gDigest {
		t.Fatalf("typed data hash %s", got.Hex())
	}
}

func TestSeriesIDGolden(t *testing.T) {
	if got := goldenSeries().ID().String(); got != gSeriesID {
		t.Fatalf("series id %s", got)
	}
}

func TestSignatureIsByteEqualToTheGolden(t *testing.T) {
	sig, err := SignQuote(goldenQuote(), gChain, common.HexToAddress(gSettler), key7(t))
	if err != nil {
		t.Fatal(err)
	}
	if hexutil.Encode(sig) != gSignature {
		t.Fatalf("signature %x, want %s", sig, gSignature)
	}
	got, err := RecoverQuoteSigner(goldenQuote(), gChain, common.HexToAddress(gSettler), sig)
	if err != nil || got != crypto.PubkeyToAddress(key7(t).PublicKey) {
		t.Fatalf("recovered %s, %v", got.Hex(), err)
	}
}

func TestHighSTwinIsRefused(t *testing.T) {
	sig := hexutil.MustDecode(gSignature)
	twin := append([]byte(nil), sig...)
	s := new(big.Int).Sub(crypto.S256().Params().N, new(big.Int).SetBytes(sig[32:64]))
	copy(twin[32:64], common.LeftPadBytes(s.Bytes(), 32))
	twin[64] ^= 1
	if _, err := Recover(common.HexToHash(gDigest), twin); err == nil {
		t.Fatal("high-s signature accepted")
	}
	if _, err := Recover(common.HexToHash(gDigest), sig[:64]); err == nil {
		t.Fatal("64-byte signature accepted")
	}
}

func TestFillAndCloseCalldataMatchTheGatewayAndDesk(t *testing.T) {
	sig := hexutil.MustDecode(gSignature)
	fill, err := FillCalldata(goldenQuote(), sig, e18(5), false)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := FillCalldata(goldenQuote(), sig, e18(1), true)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.Keccak256Hash(fill).Hex() != gFillHash || crypto.Keccak256Hash(cl).Hex() != gCloseHash {
		t.Fatalf("calldata hashes %s %s", crypto.Keccak256Hash(fill).Hex(), crypto.Keccak256Hash(cl).Hex())
	}
}

func TestTestnetDomainSeparator(t *testing.T) {
	if got := DomainSeparator(Testnet.ChainID, Testnet.Settler).Hex(); got != gTestnetSep {
		t.Fatalf("testnet domain separator %s", got)
	}
}

func TestSelectorsMatchTheCompiledContracts(t *testing.T) {
	for name, want := range gSelectors {
		m, ok := Methods[name]
		if !ok {
			t.Fatalf("no method %s", name)
		}
		if got := hex.EncodeToString(m.ID); got != want {
			t.Errorf("%s (%s): selector %s, want %s", name, m.Sig, got, want)
		}
	}
}

func randAddr(r *mrand.Rand) common.Address {
	var a common.Address
	for i := range a {
		a[i] = byte(r.IntN(256))
	}
	return a
}

func randBits(r *mrand.Rand, bits int) *big.Int {
	v := new(big.Int)
	for i := 0; i < bits; i += 64 {
		v.Lsh(v, 64).Or(v, new(big.Int).SetUint64(r.Uint64()))
	}
	return v.Rsh(v, uint(((bits+63)/64)*64-bits))
}

func TestRandomQuotesHashLikeGoEthereumAndRecover(t *testing.T) {
	r := mrand.New(mrand.NewPCG(1, 2))
	for i := 0; i < 16; i++ {
		ext := common.Address{}
		if r.IntN(2) == 1 {
			ext = randAddr(r)
		}
		q := Quote{Maker: randAddr(r), Signer: randAddr(r), Taker: randAddr(r), ExternalAccount: ext,
			Series: Series{randAddr(r), randAddr(r), randAddr(r), r.Uint64(), r.IntN(2) == 1, randBits(r, 256), r.IntN(2) == 1},
			Side:   uint8(r.IntN(2)), Amount: randBits(r, 256), MinFill: randBits(r, 128), Premium: randBits(r, 200),
			ValidUntil: r.Uint64(), Nonce: randBits(r, 256), Epoch: randBits(r, 32)}
		settler, chain := randAddr(r), uint64(r.IntN(1<<32)+1)
		d := Digest(q, chain, settler)
		if typedHash(t, TypedData(q, chain, settler)) != d {
			t.Fatalf("case %d: digest differs from go-ethereum's typed data hash", i)
		}
		key, _ := crypto.GenerateKey()
		sig, err := SignQuote(q, chain, settler, key)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := RecoverQuoteSigner(q, chain, settler, sig); err != nil || got != crypto.PubkeyToAddress(key.PublicKey) {
			t.Fatalf("case %d: recovered %s %v", i, got.Hex(), err)
		}
		// the JSON round trip keeps the digest
		b, _ := json.Marshal(q)
		var back Quote
		if err := json.Unmarshal(b, &back); err != nil || Digest(back, chain, settler) != d {
			t.Fatalf("case %d: JSON round trip: %v", i, err)
		}
	}
}

func TestLoginMessageMatchesTheGateway(t *testing.T) {
	m := common.HexToAddress("0x17BC7b00422fdb926D408887B13e9bF3Cf9bd308")
	want := "hedgehood-rfq-login:46630:" + strings.ToLower(m.Hex()) + ":ab12"
	if got := LoginMessage(46630, m, "ab12"); got != want {
		t.Fatalf("login message %q", got)
	}
	key, _ := crypto.GenerateKey()
	sig, err := SignLogin(46630, m, "ab12", key)
	if err != nil {
		t.Fatal(err)
	}
	// recover independently, the way the gateway does (accounts.TextHash + personal_sign)
	raw := hexutil.MustDecode(sig)
	raw[64] -= 27
	pub, err := crypto.SigToPub(accounts.TextHash([]byte(want)), raw)
	if err != nil || crypto.PubkeyToAddress(*pub) != crypto.PubkeyToAddress(key.PublicKey) {
		t.Fatal("login signature does not recover to the signer")
	}
}

func TestJSONUsesDecimalStringsAndNumbers(t *testing.T) {
	b, err := json.Marshal(goldenQuote())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	s := m["series"].(map[string]any)
	if m["amount"] != "5000000000000000000" || m["premium"] != "5164250" || m["nonce"] != "42" || m["epoch"] != "0" ||
		m["minFill"] != "0" || s["strike"] != "215000000" {
		t.Fatalf("uint256 fields are not decimal strings: %s", b)
	}
	if m["validUntil"] != float64(1793766600) || m["side"] != float64(0) || s["expiry"] != float64(1793826000) ||
		s["physical"] != false {
		t.Fatalf("uint64 / uint8 fields are not numbers: %s", b)
	}
	for _, bad := range []string{
		strings.Replace(string(b), `"amount":"5000000000000000000"`, `"amount":"-1"`, 1),
		strings.Replace(string(b), `"amount":"5000000000000000000"`, `"amount":"0x10"`, 1),
		strings.Replace(string(b), `"validUntil":1793766600`, `"validUntil":"1793766600"`, 1),
		strings.Replace(string(b), `"side":0`, `"side":2`, 1),
		strings.Replace(string(b), `"premium":"5164250",`, ``, 1),
	} {
		var q Quote
		if err := json.Unmarshal([]byte(bad), &q); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	// the gateway also accepts bare numbers for uint256 fields
	var q Quote
	if err := json.Unmarshal([]byte(strings.Replace(string(b), `"nonce":"42"`, `"nonce":42`, 1)), &q); err != nil ||
		q.Nonce.Int64() != 42 {
		t.Fatalf("bare number: %v", err)
	}
}

// TestGoldensMatchThePythonFile keeps the copies above equal to tests/goldens.py, the file the other implementations
// are pinned against.
func TestGoldensMatchThePythonFile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "goldens.py"))
	if err != nil {
		t.Skip("tests/goldens.py not found (module used outside the SDK repository)")
	}
	str := func(name string) string {
		m := regexp.MustCompile(`(?m)^` + name + ` = "([^"]+)"`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("goldens.py: no %s", name)
		}
		return string(m[1])
	}
	for name, want := range map[string]string{"SETTLER": gSettler, "DIGEST": gDigest, "FILL_CALLDATA_HASH": gFillHash,
		"CLOSE_CALLDATA_HASH": gCloseHash, "TESTNET_DOMAIN_SEPARATOR": gTestnetSep} {
		if got := str(name); got != want {
			t.Errorf("goldens.py %s = %s, Go copy %s", name, got, want)
		}
	}
	if m := regexp.MustCompile(`(?m)^SERIES_ID = (\d+)`).FindSubmatch(b); m == nil || string(m[1]) != gSeriesID {
		t.Errorf("goldens.py SERIES_ID differs")
	}
	m := regexp.MustCompile(`SIGNATURE = \("(0x[0-9a-f]+)"\s*"([0-9a-f]+)"\)`).FindSubmatch(b)
	if m == nil || string(m[1])+string(m[2]) != gSignature {
		t.Errorf("goldens.py SIGNATURE differs")
	}
	sel := regexp.MustCompile(`"([a-zA-Z0-9_.]+)": "([0-9a-f]{8})"`).FindAllSubmatch(b, -1)
	if len(sel) != len(gSelectors) {
		t.Errorf("goldens.py has %d selectors, Go copy %d", len(sel), len(gSelectors))
	}
	for _, s := range sel {
		if gSelectors[string(s[1])] != string(s[2]) {
			t.Errorf("goldens.py selector %s = %s, Go copy %s", s[1], s[2], gSelectors[string(s[1])])
		}
	}
	for _, field := range []string{`expiry=1793826000`, `strike=215_000_000`, `premium=5_164_250`,
		`valid_until=1793766600`, `nonce=42`, `amount=5 * 10**18`, `min_fill=0`, `side=0`} {
		if !bytes.Contains(b, []byte(field)) {
			t.Errorf("goldens.py quote no longer has %s", field)
		}
	}
}
