package cpace

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestDraft21X25519KnownAnswer(t *testing.T) {
	b, err := os.ReadFile("testdata/draft21-x25519.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]string
	if err = json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	get := func(key string) []byte {
		t.Helper()
		b, err := hex.DecodeString(raw[key])
		if err != nil {
			t.Fatal(key, err)
		}
		return b
	}
	if got := generator(get("PRS"), get("CI"), get("sid")); !bytes.Equal(got, get("g")) {
		t.Fatalf("generator %x", got)
	}
	yb, r, err := respondScalar(get("PRS"), get("sid"), get("Ya"), get("yb"), get("CI"), get("ADa"), get("ADb"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !bytes.Equal(yb, get("Yb")) || !bytes.Equal(r.isk[:], get("ISK_IR")) {
		t.Fatalf("draft transcript mismatch: %x", r.isk)
	}
	if _, _, err = r.Confirmed(); err == nil {
		t.Fatal("unconfirmed secret exported")
	}
	if !r.Verify(r.tag(true)) {
		t.Fatal("valid confirmation failed")
	}
	if r.Verify(r.tag(true)) {
		t.Fatal("state reused")
	}
	if _, _, err = r.Confirmed(); err != nil {
		t.Fatal(err)
	}
	oracleData, err := os.ReadFile("testdata/python-mcf.json")
	if err != nil {
		t.Fatal(err)
	}
	var tags map[string]string
	if err = json.Unmarshal(oracleData, &tags); err != nil {
		t.Fatal(err)
	}
	server, _ := hex.DecodeString(tags["server_tag"])
	client, _ := hex.DecodeString(tags["client_tag"])
	if !bytes.Equal(r.tag(true), server) || !bytes.Equal(r.tag(false), client) {
		t.Fatal("independent Python MCF disagrees")
	}
}
func TestLowOrderSharesAndWrongConfirmation(t *testing.T) {
	for _, share := range [][]byte{nil, make([]byte, 31), make([]byte, 32), append([]byte{1}, make([]byte, 31)...)} {
		if _, _, err := Respond([]byte("123456"), []byte("session"), share); err == nil {
			t.Fatal("low-order/invalid share accepted")
		}
	}
	b, err := os.ReadFile("testdata/draft21-x25519-coordinates.json")
	if err != nil {
		t.Fatal(err)
	}
	var invalid map[string]string
	if err = json.Unmarshal(b, &invalid); err != nil {
		t.Fatal(err)
	}
	// B.1.10's JSON labels all entries "Invalid", but its normative prose
	// requires abort only for 0..5 and 7. The high-bit-set cases have nonzero
	// RFC 7748 results and MUST NOT be rejected merely because of those labels.
	expected := []string{"", "", "", "", "", "", "d8e2c776bbacd510d09fd9278b7edcd25fc5ae9adfba3b6e040e8d3b71b21806", "", "c85c655ebe8be44ba9c0ffde69f2fe10194458d137f09bbff725ce58803cdb38", "db64dafa9b8fdd136914e61461935fe92aa372cb056314e1231bc4ec12417456", "e062dcd5376d58297be2618c7498f55baa07d7e03184e8aada20bca28888bf7a", "993c6ad11c4c29da9a56f7691fd0ff8d732e49de6250b6c2e80003ff4629a175"}
	scalar, _ := hex.DecodeString("af46e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449aff")
	key, _ := ecdh.X25519().NewPrivateKey(scalar)
	for i, want := range expected {
		name := fmt.Sprintf("Invalid Y%d", i)
		value := invalid[name]
		point, err := hex.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		peer, err := ecdh.X25519().NewPublicKey(point)
		if err != nil {
			t.Fatal(err)
		}
		got, err := key.ECDH(peer)
		if want == "" {
			if err == nil {
				t.Fatal("draft low-order share accepted", name)
			}
		} else if err != nil || hex.EncodeToString(got) != want {
			t.Fatal("draft noncanonical result mismatch", name, err)
		}
	}
}
