package sendspin

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
)

func TestFileStoreAtomicReplacementWhileSnapshotReaderIsOpen(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "device.json")}
	c, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	reader, err := openCredentialFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	next := c
	next.Volume = 17
	if err = store.Save(context.Background(), next); err != nil {
		t.Fatal("a snapshot reader blocked atomic credential replacement", err)
	}
	oldBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var old Credentials
	if json.Unmarshal(oldBytes, &old) != nil || old.Volume != c.Volume {
		t.Fatal("open reader lost its original complete snapshot")
	}
	current, err := store.Load(context.Background())
	if err != nil || current.Volume != 17 {
		t.Fatal("new reader did not see the replacement snapshot", err)
	}
}
