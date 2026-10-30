package e2etest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvAllowlistQuotesAndLiteralValues(t *testing.T) {
	values, err := parseEnv("# comment\nUNRELATED=\"unterminated\nSENDSPIN_TEST_PLAYBACK_DEVICE='literal $(secret) ${HOME}'\nSENDSPIN_TEST_CAPTURE_DEVICE=hw:1 # comment\nexport SENDSPIN_TEST_AUDIO_BACKEND=\"alsa\"\nSENDSPIN_TEST_REFERENCE_WAV=\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 4 || values["SENDSPIN_TEST_PLAYBACK_DEVICE"] != "literal $(secret) ${HOME}" || values["SENDSPIN_TEST_CAPTURE_DEVICE"] != "hw:1" || values["SENDSPIN_TEST_AUDIO_BACKEND"] != "alsa" || values["SENDSPIN_TEST_REFERENCE_WAV"] != "" {
		t.Fatal("allowlisted literal dotenv parsing failed")
	}
}

func TestEnvMalformedRelevantEntryIsRedacted(t *testing.T) {
	for _, line := range []string{
		"SENDSPIN_TEST_CAPTURE_DEVICE='private-value",
		"SENDSPIN_TEST_CAPTURE_DEVICE=\"private-value\" trailing-private-value",
		"SENDSPIN_TEST_CAPTURE_DEVICE private-value",
	} {
		_, err := parseEnv(line)
		if err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatal("malformed relevant value was accepted or exposed")
		}
	}
}

func TestEnvProcessIncludingEmptyOverridesOptionalFile(t *testing.T) {
	root := t.TempDir()
	if err := loadEnv(root); err != nil {
		t.Fatal(err)
	}
	for key := range allowedEnv {
		t.Setenv(key, "process")
	}
	t.Setenv("SENDSPIN_TEST_PLAYBACK_DEVICE", "")
	data := []byte("SENDSPIN_TEST_PLAYBACK_DEVICE=file\nSENDSPIN_TEST_CAPTURE_DEVICE=file\n")
	if err := os.WriteFile(filepath.Join(root, ".env"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnv(root); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SENDSPIN_TEST_PLAYBACK_DEVICE") != "" || os.Getenv("SENDSPIN_TEST_CAPTURE_DEVICE") != "process" {
		t.Fatal("file overrode existing process environment")
	}
}
