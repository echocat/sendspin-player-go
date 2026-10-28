// Command noisevectorcmd verifies the frozen independent Noise oracle's source.
package main

import (
	"bytes"
	"context"
	"crypto/sha1" // Git blob identity, not a security signature.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"regexp"
	"time"
)

type vector struct {
	Protocol string              `json:"protocol_name"`
	Hash     string              `json:"handshake_hash"`
	Messages []map[string]string `json:"messages"`
}
type fixture struct {
	Source struct {
		URL, Revision string
		Blob          string `json:"git_blob"`
		SHA256        string `json:"sha256"`
	} `json:"source"`
	Common  map[string]string `json:"common"`
	Vectors []vector          `json:"vectors"`
}

func verify(ctx context.Context, raw []byte, client *http.Client) error {
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(f.Source.Revision) || f.Source.URL != "https://raw.githubusercontent.com/centromere/cacophony/"+f.Source.Revision+"/vectors/cacophony.txt" || len(f.Vectors) != 2 {
		return errors.New("invalid independent oracle pin/inventory")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Source.URL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if response.StatusCode != http.StatusOK || len(data) > 2<<20 {
		return errors.New("independent oracle unavailable or oversized")
	}
	sha := sha256.Sum256(data)
	blob := sha1.New()
	_, _ = fmt.Fprintf(blob, "blob %d\x00", len(data))
	_, _ = blob.Write(data)
	if hex.EncodeToString(sha[:]) != f.Source.SHA256 || hex.EncodeToString(blob.Sum(nil)) != f.Source.Blob {
		return errors.New("independent oracle source fingerprint mismatch")
	}
	var source struct {
		Vectors []json.RawMessage `json:"vectors"`
	}
	if err = json.Unmarshal(data, &source); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, rawVector := range source.Vectors {
		var v vector
		if err = json.Unmarshal(rawVector, &v); err != nil {
			return err
		}
		for _, wanted := range f.Vectors {
			if wanted.Protocol != v.Protocol {
				continue
			}
			if seen[v.Protocol] || len(v.Messages) != 6 || !reflect.DeepEqual(wanted, v) {
				return errors.New("frozen oracle transcript differs from independent source")
			}
			seen[v.Protocol] = true
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(rawVector, &fields); err != nil {
				return err
			}
			for commonKey, sourceKey := range map[string]string{"init_static": "init_static", "resp_static": "resp_static", "init_ephemeral": "init_ephemeral", "resp_ephemeral": "resp_ephemeral", "init_remote_static": "init_remote_static", "resp_remote_static": "resp_remote_static", "prologue": "init_prologue"} {
				var value string
				if json.Unmarshal(fields[sourceKey], &value) != nil || value != f.Common[commonKey] {
					return errors.New("frozen oracle key/prologue differs")
				}
			}
			var prologue string
			if json.Unmarshal(fields["resp_prologue"], &prologue) != nil || prologue != f.Common["prologue"] {
				return errors.New("foreign peer prologues differ")
			}
			for _, key := range []string{"init_psks", "resp_psks"} {
				var keys []string
				if json.Unmarshal(fields[key], &keys) != nil || len(keys) != 1 || keys[0] != f.Common["psk"] {
					return errors.New("frozen oracle PSK differs")
				}
			}
		}
	}
	if len(seen) != 2 || bytes.Equal([]byte(f.Vectors[0].Protocol), []byte(f.Vectors[1].Protocol)) {
		return errors.New("missing/duplicate oracle suites")
	}
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	raw, err := os.ReadFile("internal/wire/testdata/cacophony.json")
	if err == nil {
		err = verify(ctx, raw, &http.Client{Timeout: 45 * time.Second})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Independent Cacophony source fingerprints and both frozen KKpsk2 transcripts verified.")
}
