package maker

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

var k220 = big.NewInt(220_000_000)

func TestFeeGoldensFromTheSettlerGatewayAndDesk(t *testing.T) {
	// RfqSettler.t.sol test_feeIsCappedAtTheNotional / gateway quote.TestFee: 12.5 % of the premium, at most 1 % of 10 x 220
	for _, c := range []struct {
		premium, amount  int64
		feeBps, notional uint64
		strike           *big.Int
		want             int64
	}{
		{30e6, 10, 1250, 100, k220, 3_750_000},
		{300e6, 10, 1250, 100, k220, 22e6},
		{150e6, 5, 1250, 100, k220, 11e6},
		{300e6, 10, 1250, 0, k220, 37_500_000}, // notionalFeeBps 0 = no cap
		{2_185_069, 1, 500, 0, k220, 109_253},  // the testnet E2E fill
	} {
		if got := Fee(big.NewInt(c.premium), e18(c.amount), c.strike, c.feeBps, c.notional, nil); got.Int64() != c.want {
			t.Errorf("fee(%d, %d): %s, want %d", c.premium, c.amount, got, c.want)
		}
	}
}

func TestPreviewFillProratesInTheMakersFavour(t *testing.T) {
	p, f, err := PreviewFill(goldenQuote(), e18(3), 500, 0, nil)
	if err != nil || p.Int64() != 3_098_550 || f.Int64() != 154_927 {
		t.Fatalf("previewFill %s %s %v", p, f, err)
	}
	sells := goldenQuote()
	sells.Side, sells.Premium, sells.Amount = MakerSells, big.NewInt(1_000_001), big.NewInt(3)
	if p, _, _ := PreviewFill(sells, big.NewInt(1), 500, 0, nil); p.Int64() != 333_334 {
		t.Fatalf("sell side rounds up: %s", p)
	}
	if _, _, err := PreviewFill(goldenQuote(), new(big.Int), 500, 0, nil); err == nil {
		t.Fatal("zero amount accepted")
	}
	if _, _, err := PreviewFill(goldenQuote(), e18(6), 500, 0, nil); err == nil {
		t.Fatal("amount above the quote accepted")
	}
}

func TestMakerCash(t *testing.T) {
	if got := MakerCash(MakerBuys, big.NewInt(30e6), e18(10), k220, 1250, 100, nil); got.Int64() != -33_750_000 {
		t.Fatalf("buy %s", got)
	}
	if got := MakerCash(MakerSells, big.NewInt(300e6), e18(10), k220, 1250, 100, nil); got.Int64() != 278_000_000 {
		t.Fatalf("sell %s", got)
	}
}

func TestPremiumForNet(t *testing.T) {
	amount := e18(10)
	one := big.NewInt(1)
	for _, net := range []int64{0, 1, 7, 1_000_000, 33_750_000, 250_000_000, 1e12} {
		for _, fees := range [][2]uint64{{1250, 100}, {1250, 0}, {500, 0}, {0, 0}} {
			n := big.NewInt(net)
			p, err := PremiumForNet(MakerBuys, n, amount, k220, fees[0], fees[1], nil)
			if err != nil {
				t.Fatal(err)
			}
			cost := func(p *big.Int) *big.Int {
				return new(big.Int).Neg(MakerCash(MakerBuys, p, amount, k220, fees[0], fees[1], nil))
			}
			if cost(p).Cmp(n) > 0 || cost(new(big.Int).Add(p, one)).Cmp(n) <= 0 {
				t.Fatalf("buy net %d fees %v: premium %s is not the largest within budget", net, fees, p)
			}
			p, err = PremiumForNet(MakerSells, n, amount, k220, fees[0], fees[1], nil)
			if err != nil {
				t.Fatal(err)
			}
			kept := func(p *big.Int) *big.Int { return MakerCash(MakerSells, p, amount, k220, fees[0], fees[1], nil) }
			if kept(p).Cmp(n) < 0 || (p.Sign() > 0 && kept(new(big.Int).Sub(p, one)).Cmp(n) >= 0) {
				t.Fatalf("sell net %d fees %v: premium %s is not the smallest that keeps net", net, fees, p)
			}
		}
	}
	if _, err := PremiumForNet(MakerSells, big.NewInt(1), amount, k220, 10_000, 0, nil); err == nil {
		t.Fatal("a 100 % fee accepted")
	}
}

func TestLongPayoutRoundsDown(t *testing.T) {
	a, b, c := common.HexToAddress("0x11"), common.HexToAddress("0x22"), common.HexToAddress("0x33")
	call := Series{a, b, c, 1, false, big.NewInt(215_000_000), false}
	put := Series{a, b, c, 1, true, big.NewInt(195_000_000), false}
	p := big.NewInt(227_227_000)
	want := new(big.Int).Mul(e18(3), big.NewInt(227_227_000-215_000_000))
	want.Quo(want, p)
	if LongPayout(call, e18(3), p, nil).Cmp(want) != 0 {
		t.Fatal("call payout")
	}
	if LongPayout(put, e18(2), p, nil).Sign() != 0 || LongPayout(put, e18(2), big.NewInt(150_000_000), nil).Int64() != 90_000_000 {
		t.Fatal("put payout")
	}
}

func TestUnits(t *testing.T) {
	for in, want := range map[string]string{"1000": "1000000000", "12.5": "12500000", "0.000001": "1", ".5": "500000"} {
		if got, err := ParseUnits(in, 6); err != nil || got.String() != want {
			t.Errorf("ParseUnits(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "-1", "1.0000001", "1e6", "0x10", "1,000"} {
		if _, err := ParseUnits(bad, 6); err == nil {
			t.Errorf("ParseUnits(%q) accepted", bad)
		}
	}
	if FormatUnits(big.NewInt(12_500_000), 6) != "12.5" || FormatUnits(big.NewInt(1), 6) != "0.000001" ||
		FormatUnits(MaxUint256(), 6) != "unlimited" {
		t.Fatal("FormatUnits")
	}
}
