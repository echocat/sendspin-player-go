package sendspin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// MemoryStore is intended for tests and ephemeral applications. It does not
// satisfy a device's persistence obligations across process restarts.
type MemoryStore struct {
	mu    sync.Mutex
	value *Credentials
}

func cloneCredentials(c Credentials) Credentials {
	c.Records = append([]PairingRecord(nil), c.Records...)
	if c.StaticPairingPIN != nil {
		pin := *c.StaticPairingPIN
		c.StaticPairingPIN = &pin
	}
	return c
}
func (s *MemoryStore) Load(ctx context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	if s.value == nil {
		return Credentials{}, ErrNoCredentials
	}
	return cloneCredentials(*s.value), nil
}
func (s *MemoryStore) Save(ctx context.Context, c Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c = cloneCredentials(c)
	s.value = &c
	return nil
}

// FileStore saves credentials with an atomic same-directory rename and 0600
// permissions on Unix. On Windows the application must secure the directory ACL.
// Do not share a path between devices or concurrent Client instances.
type FileStore struct{ Path string }

func (s FileStore) Load(ctx context.Context) (Credentials, error) {
	var c Credentials
	if err := ctx.Err(); err != nil {
		return c, err
	}
	f, err := openCredentialFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNoCredentials
	}
	if err != nil {
		return c, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0) {
		return c, errors.New("sendspin: credential file must be regular and private (0600)")
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, errors.New("sendspin: invalid credential file")
	}
	return c, validateCredentials(c)
}

func (s FileStore) Save(ctx context.Context, c Credentials) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCredentials(c); err != nil {
		return err
	}
	if s.Path == "" {
		return errors.New("sendspin: empty credential path")
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".sendspin-credentials-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = replaceCredentialFile(f.Name(), s.Path); err != nil {
		return fmt.Errorf("sendspin: persist credentials: %w", err)
	}
	return nil
}
