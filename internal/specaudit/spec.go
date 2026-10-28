package specaudit

import (
	"context"
	"crypto/sha1" // Git blob identity, not a security signature.
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
)

// VerifySpec is opt-in network verification of the frozen source fingerprints.
// Ordinary catalog checking stays offline and never silently updates a pin.
func (p *Project) VerifySpec(ctx context.Context) error {
	client := &http.Client{Timeout: 30 * time.Second}
	return p.verifySpec(ctx, client)
}

func (p *Project) verifySpec(ctx context.Context, client *http.Client) error {
	for _, source := range p.Index.Sources {
		data, err := p.fetchSpec(ctx, client, source.Path)
		if err != nil {
			return err
		}
		if err = verifySource(source, data); err != nil {
			return err
		}
	}
	for _, source := range p.Catalog.InterpretationSources {
		data, err := p.fetchSpec(ctx, client, source.Path)
		if err != nil {
			return err
		}
		if err = verifyDocument(source.Path, source.Blob, source.SHA256, source.Lines, data); err != nil {
			return err
		}
	}
	return nil
}

func (p *Project) fetchSpec(ctx context.Context, client *http.Client, path string) ([]byte, error) {
	url := "https://raw.githubusercontent.com/Sendspin/spec/" + p.Catalog.Revision + "/" + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pinned spec source unavailable: %s", path)
	}
	return data, nil
}

func verifyDocument(path, expectedBlob, expectedSHA256 string, expectedLines int, data []byte) error {
	if digest(data) != expectedSHA256 {
		return fmt.Errorf("source SHA-256 mismatch: %s", path)
	}
	blob := sha1.New()
	_, _ = fmt.Fprintf(blob, "blob %d\x00", len(data))
	_, _ = blob.Write(data)
	if hex.EncodeToString(blob.Sum(nil)) != expectedBlob {
		return fmt.Errorf("git blob mismatch: %s", path)
	}
	if len(splitLines(data)) != expectedLines {
		return fmt.Errorf("line count mismatch: %s", path)
	}
	return nil
}

func verifySource(source SpecSource, data []byte) error {
	if err := verifyDocument(source.Path, source.Blob, source.SHA256, source.Lines, data); err != nil {
		return err
	}
	lines := splitLines(data)
	if !slices.Equal(source.Blocks, paragraphs(source.ID, lines)) {
		return fmt.Errorf("source paragraph inventory changed/omitted: %s", source.Path)
	}
	return nil
}
