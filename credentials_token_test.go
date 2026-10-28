package sendspin

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func tokenTestPayload(extra int) []byte {
	data := make([]byte, 64+extra)
	for i := range data {
		data[i] = byte(i)
	}
	return data
}

func TestPairingTokenEmitterMatchesIndependentReference(t *testing.T) {
	// Public RFC 7748 section 6.1 Alice test key, never production credentials.
	// The expected token was computed with Python's standard base32 encoder
	// from RFC Alice public key 8520f009...a9b4e6a and PSK e0..ff. Neither the
	// SDK token parser nor its ClientID function builds the expected value.
	private, err := hex.DecodeString("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
	if err != nil {
		t.Fatal(err)
	}
	var credentials Credentials
	copy(credentials.PrivateKey[:], private)
	for i := range credentials.PairingPSK {
		credentials.PairingPSK[i] = byte(224 + i)
	}
	const expected = "SP:0QUQPACMJGCTVI5ELPXOLIPXXLIG36OQNEY4BV5HLUSUY5KU3JZVOBYPC4PSOLZXH5DU6V97M5XXO74HR6LZ7J5PW674PT6X37T6757Y"
	if got := credentials.PairingToken(); got != expected {
		t.Fatal("emitted token differs from the independent fixed vector")
	}
}

func TestPairingTokenOperatorInputAndReservedExtensions(t *testing.T) {
	for extra := range 9 {
		data := tokenTestPayload(extra)
		encoded := strings.ReplaceAll(base32.StdEncoding.EncodeToString(data), "2", "9")
		body := strings.TrimRight(encoded, "=")
		padding := len(encoded) - len(body)
		for supplied := 0; supplied <= padding; supplied++ {
			for _, prefix := range []string{"", "SP:", "sp:"} {
				t.Run(fmt.Sprintf("extension-%d/padding-%d/prefix-%q", extra, supplied, prefix), func(t *testing.T) {
					token := " \t\n" + prefix + "0" + strings.ToLower(body) + strings.Repeat("=", supplied) + "\r\n "
					id, psk, err := ParsePairingToken(token)
					if err != nil {
						t.Fatal("rejected lenient operator input", err)
					}
					if id != base64.RawURLEncoding.EncodeToString(data[:32]) || psk != Secret(data[32:64]) {
						t.Fatal("padding or reserved extensions changed the credentials")
					}
				})
			}
		}
		// The ordinary RFC alphabet remains accepted alongside transliterated 9.
		if _, _, err := ParsePairingToken("0" + strings.ReplaceAll(body, "9", "2")); err != nil {
			t.Fatal("rejected untransliterated base32", err)
		}
	}
}

func TestPairingTokenRejectsMalformedOperatorInput(t *testing.T) {
	body := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(tokenTestPayload(0))
	for name, token := range map[string]string{
		"unknown-version":  "SP:10" + body,
		"unicode-prefix":   "ſP:0" + body,
		"unicode-body":     "SP:0" + strings.Replace(body, "S", "ſ", 1),
		"invalid-utf8":     "SP:0" + body + "\xff",
		"embedded-newline": "SP:0" + body[:8] + "\n" + body[8:],
		"embedded-return":  "SP:0" + body[:8] + "\r" + body[8:],
		"embedded-space":   "SP:0" + body[:8] + " " + body[8:],
		"interior-padding": "SP:0" + body[:8] + "=" + body[8:],
		"excess-padding":   "SP:0" + body + "========",
		"short-payload":    "SP:0" + base32.StdEncoding.EncodeToString(tokenTestPayload(0)[:63]),
		"invalid-alphabet": "SP:0" + body + "8",
		"missing-version":  "SP:" + body,
	} {
		t.Run(name, func(t *testing.T) {
			id, psk, err := ParsePairingToken(token)
			if err == nil || id != "" || psk != (Secret{}) {
				t.Fatal("malformed token accepted or returned partial credentials")
			}
		})
	}
}

func FuzzPairingToken(f *testing.F) {
	for _, extra := range []int{0, 1, 7, 8} {
		body := base32.StdEncoding.EncodeToString(tokenTestPayload(extra))
		f.Add("SP:0" + body)
		f.Add(" 0" + strings.ToLower(strings.TrimRight(body, "=")) + " ")
	}
	f.Add("ſP:0AAAA")
	f.Add("SP:0A\nA")
	f.Fuzz(func(t *testing.T, token string) {
		id, psk, err := ParsePairingToken(token)
		if err != nil {
			if id != "" || psk != (Secret{}) {
				t.Fatal("failed parse returned partial credentials")
			}
			return
		}
		input := strings.TrimSpace(token)
		for i := range len(input) {
			if input[i] > 127 || input[i] == '\r' || input[i] == '\n' {
				t.Fatal("accepted non-ASCII or embedded line break")
			}
		}
		pub, err := base64.RawURLEncoding.Strict().DecodeString(id)
		if err != nil || len(pub) != 32 || len(id) != 43 {
			t.Fatal("parsed identity is not canonical")
		}
		canonical := "SP:0" + strings.ReplaceAll(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(append(pub, psk[:]...)), "2", "9")
		again, secret, err := ParsePairingToken(canonical)
		if err != nil || again != id || secret != psk {
			t.Fatal("canonical token does not preserve decoded credentials")
		}
	})
}
