package main

import "testing"

func TestParseAddress(t *testing.T) {
	ok := "0x36437b878415EdA1a24186CF79AFffBc9ecEd298"
	if a, err := parseAddress(ok, "-external"); err != nil || a.Hex() != ok {
		t.Fatalf("valid: %v %v", a, err)
	}
	for _, bad := range []string{
		"0x1234", // short: HexToAddress would left-pad it
		"0x36437b878415EdA1a24186CF79AFffBc9ecEd29800", // long: HexToAddress would keep the last 20 bytes
		"0x36437b878415EdA1a24186CF79AFffBc9ecEd29g",   // not hex
		"self2", "",
		"0x0000000000000000000000000000000000000000",
	} {
		if _, err := parseAddress(bad, "-external"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
