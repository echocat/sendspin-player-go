package cpace

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestConfirmationCannotReplayAcrossPasswordContextSIDOrRoles(t *testing.T) {
	data, err := os.ReadFile("testdata/draft21-x25519.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector map[string]string
	if err = json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("testdata/python-mcf.json")
	if err != nil {
		t.Fatal(err)
	}
	var tags map[string]string
	if err = json.Unmarshal(data, &tags); err != nil {
		t.Fatal(err)
	}
	get := func(k string) []byte {
		t.Helper()
		b, err := hex.DecodeString(vector[k])
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	tag, err := hex.DecodeString(tags["server_tag"])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"PRS", "CI", "sid", "ADa", "ADb", "Ya"} {
		t.Run(field, func(t *testing.T) {
			prs, ci, sid, ada, adb, ya := get("PRS"), get("CI"), get("sid"), get("ADa"), get("ADb"), get("Ya")
			selected := map[string][]byte{"PRS": prs, "CI": ci, "sid": sid, "ADa": ada, "ADb": adb, "Ya": ya}[field]
			if len(selected) == 0 {
				t.Fatal("fixture field unexpectedly empty", field)
			}
			selected[0] ^= 1
			_, result, err := respondScalar(prs, sid, ya, get("yb"), ci, ada, adb)
			if err != nil {
				return
			}
			defer result.Close()
			if result.Verify(tag) {
				t.Fatal("foreign transcript authenticated", field)
			}
			if _, _, err = result.Confirmed(); err == nil {
				t.Fatal("unconfirmed ISK exported")
			}
			if result.isk != ([64]byte{}) || result.mac != ([64]byte{}) || !result.finished {
				t.Fatal("failed confirmation retained live key state")
			}
			if result.Verify(tag) {
				t.Fatal("failed state reused")
			}
		})
	}
}
