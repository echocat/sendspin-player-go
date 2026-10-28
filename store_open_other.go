//go:build !windows

package sendspin

import "os"

func openCredentialFile(path string) (*os.File, error) { return os.Open(path) }

func replaceCredentialFile(from, to string) error { return os.Rename(from, to) }
