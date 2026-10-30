package cpace

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type oracleInput struct {
	PRS    string `json:"prs"`
	SID    string `json:"sid"`
	CI     string `json:"ci"`
	AD     string `json:"ad"`
	PeerAD string `json:"peer_ad"`
	Scalar string `json:"scalar"`
}
type oracleCase struct {
	ID, Category   string
	A, B           oracleInput
	GeneratorA     string `json:"generator_a"`
	GeneratorB     string `json:"generator_b"`
	Ya, Yb         string
	ReceivedYa     string `json:"received_ya"`
	ReceivedYb     string `json:"received_yb"`
	SharedA        string `json:"shared_a"`
	SharedB        string `json:"shared_b"`
	ISKA           string `json:"isk_a"`
	ISKB           string `json:"isk_b"`
	ServerTag      string `json:"server_tag"`
	ClientTag      string `json:"client_tag"`
	ServerAccepted bool   `json:"server_accepted"`
	ClientAccepted bool   `json:"client_accepted"`
}

func TestPinnedPythonDifferentialCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/python-differential.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "3e8f17dbfc8e363d8eb60d0f655ff512f236e94ed9306006fa688670e162836d" {
		t.Fatal("frozen independent corpus fingerprint changed")
	}
	var corpus struct {
		Version int
		Scope   string
		Source  struct {
			Package, Version, Draft string
			ModuleSHA256            string `json:"module_sha256"`
			WheelSHA256             string `json:"wheel_sha256"`
		}
		Cases []oracleCase
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Version != 1 || corpus.Source.Package != "cpace" || corpus.Source.Version != "0.1.0" || corpus.Source.Draft != "draft-irtf-cfrg-cpace-21" || corpus.Source.ModuleSHA256 != "ee207059c29923d1b0aad50ab3b252a475e44424e130849cef85b86f7755bbd9" || corpus.Source.WheelSHA256 != "9fabb60a711a85934225be4081b3f5994e1ca4cd1335c5b9171770c8f1a2fda3" || len(corpus.Cases) != 172 {
		t.Fatal("oracle pin/inventory mismatch")
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, v := range corpus.Cases {
		if v.ID == "" || seen[v.ID] {
			t.Fatal("missing/duplicate case ID")
		}
		seen[v.ID] = true
		counts[v.Category]++
		t.Run(v.ID, func(t *testing.T) {
			decode := func(s string) []byte {
				t.Helper()
				b, err := hex.DecodeString(s)
				if err != nil {
					t.Fatal("invalid fixture hex", err)
				}
				return b
			}
			check := func(field string, got []byte, want string) {
				t.Helper()
				if !bytes.Equal(got, decode(want)) {
					t.Fatal("independent differential mismatch", v.ID, field)
				}
			}
			check("generator-a", generator(decode(v.A.PRS), decode(v.A.CI), decode(v.A.SID)), v.GeneratorA)
			check("generator-b", generator(decode(v.B.PRS), decode(v.B.CI), decode(v.B.SID)), v.GeneratorB)
			yb, result, err := respondScalar(decode(v.B.PRS), decode(v.B.SID), decode(v.ReceivedYa), decode(v.B.Scalar), decode(v.B.CI), decode(v.B.PeerAD), decode(v.B.AD))
			if err != nil {
				t.Fatal(err)
			}
			defer result.Close()
			check("public-b", yb, v.Yb)
			check("isk-b", result.isk[:], v.ISKB)
			if v.Category != "mismatch" {
				check("tag-a-as-seen-by-b", result.tag(true), v.ServerTag)
			}
			// The peer's tag may intentionally come from a different transcript.
			// Compare the local client tag separately, then test actual acceptance.
			check("client-tag", result.tag(false), v.ClientTag)
			if _, _, err = result.Confirmed(); err == nil {
				t.Fatal("unconfirmed key exported")
			}
			accepted := result.Verify(decode(v.ServerTag))
			if accepted != v.ServerAccepted {
				t.Fatal("server confirmation acceptance differs from independent Python", v.Category)
			}
			if accepted {
				isk, tag, err := result.Confirmed()
				if err != nil {
					t.Fatal(err)
				}
				check("confirmed-isk", isk[:], v.ISKB)
				check("confirmed-client-tag", tag, v.ClientTag)
			} else {
				if _, _, err = result.Confirmed(); err == nil {
					t.Fatal("rejected confirmation exported key")
				}
				if result.isk != ([64]byte{}) || result.mac != ([64]byte{}) {
					t.Fatal("failed confirmation retained live key")
				}
			}
			if result.Verify(decode(v.ServerTag)) {
				t.Fatal("confirmation state reused")
			}
			keyA, err := ecdh.X25519().NewPrivateKey(decode(v.A.Scalar))
			if err != nil {
				t.Fatal(err)
			}
			gA, err := ecdh.X25519().NewPublicKey(decode(v.GeneratorA))
			if err != nil {
				t.Fatal(err)
			}
			ya, err := keyA.ECDH(gA)
			if err != nil {
				t.Fatal(err)
			}
			check("public-a", ya, v.Ya)
			peerB, err := ecdh.X25519().NewPublicKey(decode(v.ReceivedYb))
			if err != nil {
				t.Fatal(err)
			}
			shared, err := keyA.ECDH(peerB)
			if err != nil {
				t.Fatal(err)
			}
			check("shared-a", shared, v.SharedA)
			input := append(lv([]byte("CPace255_ISK"), decode(v.A.SID), shared), append(lv(ya, decode(v.A.AD)), lv(decode(v.ReceivedYb), decode(v.A.PeerAD))...)...)
			iskA := sha512.Sum512(input)
			check("isk-a", iskA[:], v.ISKA)
			macInput := append(append([]byte("CPaceMac"), decode(v.A.SID)...), iskA[:]...)
			macKey := sha512.Sum512(macInput)
			h := hmac.New(sha512.New, macKey[:])
			_, _ = h.Write(lv(ya, decode(v.A.AD)))
			check("server-tag", h.Sum(nil), v.ServerTag)
			h = hmac.New(sha512.New, macKey[:])
			_, _ = h.Write(lv(decode(v.ReceivedYb), decode(v.A.PeerAD)))
			reflection := bytes.Equal(ya, decode(v.ReceivedYb)) && bytes.Equal(decode(v.A.AD), decode(v.A.PeerAD))
			if accepted := !reflection && hmac.Equal(h.Sum(nil), decode(v.ClientTag)); accepted != v.ClientAccepted {
				t.Fatal("client confirmation acceptance differs from Python")
			}
			keyB, err := ecdh.X25519().NewPrivateKey(decode(v.B.Scalar))
			if err != nil {
				t.Fatal(err)
			}
			peerA, err := ecdh.X25519().NewPublicKey(decode(v.ReceivedYa))
			if err != nil {
				t.Fatal(err)
			}
			shared, err = keyB.ECDH(peerA)
			if err != nil {
				t.Fatal(err)
			}
			check("shared-b", shared, v.SharedB)
		})
	}
	if counts["sendspin-profile"] != 64 || counts["draft-boundary"] != 64 || counts["mismatch"] != 42 || counts["reflection"] != 2 {
		t.Fatal("differential category coverage changed", counts)
	}
}
