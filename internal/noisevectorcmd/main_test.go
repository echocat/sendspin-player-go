package main

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type oracleTransport func(*http.Request) (*http.Response, error)

func (f oracleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIndependentOracleSourceCheckRejectsChangedPinsAndExtraction(t *testing.T) {
	for _, mutation := range []string{"none", "sha256", "blob", "ciphertext", "psk", "duplicate", "unavailable", "url"} {
		t.Run(mutation, func(t *testing.T) {
			f := fixture{Common: map[string]string{"prologue": "ab", "psk": "cc", "init_static": "01", "resp_static": "02", "init_ephemeral": "03", "resp_ephemeral": "04", "init_remote_static": "05", "resp_remote_static": "06"}}
			f.Source.Revision = strings.Repeat("a", 40)
			f.Source.URL = "https://raw.githubusercontent.com/centromere/cacophony/" + f.Source.Revision + "/vectors/cacophony.txt"
			var records []map[string]any
			for _, name := range []string{"Noise_KKpsk2_25519_AESGCM_SHA256", "Noise_KKpsk2_25519_ChaChaPoly_SHA256"} {
				v := vector{Protocol: name, Hash: "aa"}
				for range 6 {
					v.Messages = append(v.Messages, map[string]string{"payload": "01", "ciphertext": "02"})
				}
				f.Vectors = append(f.Vectors, v)
				r := map[string]any{"protocol_name": name, "handshake_hash": v.Hash, "messages": v.Messages, "init_prologue": f.Common["prologue"], "resp_prologue": f.Common["prologue"], "init_psks": []string{f.Common["psk"]}, "resp_psks": []string{f.Common["psk"]}}
				for k, value := range f.Common {
					if k != "prologue" && k != "psk" {
						r[k] = value
					}
				}
				records = append(records, r)
			}
			source, err := json.Marshal(map[string]any{"vectors": records})
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(source)
			blob := sha1.New()
			_, _ = fmt.Fprintf(blob, "blob %d\x00", len(source))
			_, _ = blob.Write(source)
			f.Source.SHA256 = hex.EncodeToString(sum[:])
			f.Source.Blob = hex.EncodeToString(blob.Sum(nil))
			switch mutation {
			case "sha256":
				f.Source.SHA256 = strings.Repeat("0", 64)
			case "blob":
				f.Source.Blob = strings.Repeat("0", 40)
			case "ciphertext":
				f.Vectors[0].Messages[0]["ciphertext"] = "bad"
			case "psk":
				f.Common["psk"] = "bad"
			case "duplicate":
				f.Vectors[1] = f.Vectors[0]
			case "url":
				f.Source.URL = "https://example.test/untrusted"
			}
			raw, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: oracleTransport(func(*http.Request) (*http.Response, error) {
				status := http.StatusOK
				if mutation == "unavailable" {
					status = http.StatusNotFound
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(source))), Header: http.Header{}}, nil
			})}
			if err = verify(context.Background(), raw, client); (err == nil) != (mutation == "none") {
				t.Fatal("oracle check accepted changed evidence or rejected valid fixture", err)
			}
		})
	}
}
