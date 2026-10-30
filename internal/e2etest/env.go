package e2etest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var allowedEnv = map[string]bool{
	"SENDSPIN_TEST_PLAYBACK_DEVICE": true,
	"SENDSPIN_TEST_CAPTURE_DEVICE":  true,
	"SENDSPIN_TEST_REFERENCE_WAV":   true,
	"SENDSPIN_TEST_AUDIO_BACKEND":   true,
}

// loadEnv is called only by the test entry point, never by SDK initialization.
// An explicitly empty process variable overrides the file just like any value.
func loadEnv(root string) error {
	data, err := os.ReadFile(filepath.Join(root, ".env"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("e2etest: cannot read optional .env: %w", err)
	}
	values, err := parseEnv(string(data))
	if err != nil {
		return err
	}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("e2etest: cannot set .env key %s", key)
			}
		}
	}
	return nil
}

func parseEnv(data string) (map[string]string, error) {
	values := make(map[string]string)
	for index, raw := range strings.Split(strings.TrimPrefix(data, "\ufeff"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !allowedEnv[key] {
			// Ignore unrelated application configuration, even if its syntax
			// belongs to another dotenv implementation.
			fields := strings.Fields(key)
			if len(fields) == 0 || !allowedEnv[fields[0]] {
				continue
			}
			key, found = fields[0], false
		}
		invalid := func() error { return fmt.Errorf("e2etest: malformed .env entry for %s at line %d", key, index+1) }
		if !found {
			return nil, invalid()
		}
		value = strings.TrimSpace(value)
		if value != "" && (value[0] == '\'' || value[0] == '"') {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return nil, invalid()
			}
			end++
			tail := strings.TrimSpace(value[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, invalid()
			}
			value = value[1:end]
		} else if at := strings.Index(value, " #"); at >= 0 {
			value = strings.TrimSpace(value[:at])
		}
		// Deliberately literal: no interpolation, command substitution, escapes,
		// shell sourcing, variable expansion or arbitrary exported keys.
		values[key] = value
	}
	return values, nil
}
