//go:build e2e

package conformancetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

var officialScenarios = []string{
	"client-initiated-pcm", "server-initiated-pcm",
	"server-initiated-protocol-baseline-v1", "server-initiated-audio-chunk-framing",
	"server-initiated-pcm-24bit", "server-initiated-flac", "server-initiated-opus",
	"client-initiated-state-format-pcm", "client-initiated-state-format-flac",
	"client-initiated-metadata", "server-initiated-metadata",
	"client-initiated-controller", "server-initiated-controller",
	"client-initiated-artwork", "server-initiated-artwork",
}

// Setup is lazy to respect Go's -short/-run (including subtest selection).
// TestMain owns all resources; even -count=N reuses one adapter build and one
// official harness process for the package, not a Python outer test runner.
var official struct {
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
	directory string
	err       error
	command   *exec.Cmd
	input     io.WriteCloser
	requests  *json.Encoder
	results   *json.Decoder
	done      chan error
	log       *os.File
	stopTree  func() error
	run       int
}

func TestMain(m *testing.M) {
	official.ctx, official.cancel = context.WithTimeout(context.Background(), 15*time.Minute)
	code := m.Run()
	if err := closeOfficial(); err != nil {
		fmt.Fprintln(os.Stderr, "official harness cleanup:", err)
		code = 1
	}
	os.Exit(code)
}

func setupOfficial() error {
	root := e2etest.Root()
	directory, err := os.MkdirTemp("", "sendspin-official-harness-")
	if err != nil {
		return err
	}
	official.directory = directory
	python, err := e2etest.Python(official.ctx, root, "conformancetest")
	if err != nil {
		return fmt.Errorf("prepare pinned official harness runtime: %w", err)
	}
	binary := filepath.Join(directory, "sdk-client")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// Reuse the existing public-SDK adapter; no nested module or SDK copy.
	build := exec.CommandContext(official.ctx, "go", "build", "-o", binary, "./internal/conformanceclient")
	build.Dir = root
	build.WaitDelay = 5 * time.Second
	if output, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build public-SDK harness adapter: %w\n%s", err, output)
	}
	fixtureRepo := filepath.Join(directory, "fixture")
	if err := writeFixture(filepath.Join(fixtureRepo, "tests", "fixtures", "almost_silent.flac")); err != nil {
		return fmt.Errorf("generate Go-owned harness fixture: %w", err)
	}
	official.log, err = os.Create(filepath.Join(directory, "harness.log"))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(official.ctx, python, "-u", filepath.Join(root, "internal", "conformancetest", "run.py"), "--client", binary, "--fixture-repo", fixtureRepo)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	cmd.Stderr = official.log
	cmd.WaitDelay = 5 * time.Second
	started, stopTree, err := prepareHarnessProcess(cmd)
	if err != nil {
		return err
	}
	official.stopTree = stopTree
	official.input, err = cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	official.requests = json.NewEncoder(official.input)
	official.results = json.NewDecoder(output)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pinned official harness: %w", err)
	}
	official.command = cmd
	official.done = make(chan error, 1)
	go func() { official.done <- cmd.Wait() }()
	if err := started(); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("contain official harness descendants: %w", err)
	}
	return nil
}

func closeOfficial() error {
	defer official.cancel()
	var result error
	if official.input != nil {
		_ = official.input.Close()
	}
	if official.command != nil {
		select {
		case err := <-official.done:
			if err != nil {
				result = fmt.Errorf("harness exit: %w\n%s", err, officialOutput())
			}
		case <-time.After(5 * time.Second):
			official.cancel()
			result = fmt.Errorf("harness did not stop after stdin EOF\n%s", officialOutput())
			// WaitDelay bounds the direct child; stopTree below also terminates
			// scenario descendants left by an interrupted upstream invocation.
			if official.stopTree != nil {
				_ = official.stopTree()
				official.stopTree = nil
			}
			result = errors.Join(result, <-official.done)
		}
	}
	if official.stopTree != nil {
		if err := official.stopTree(); err != nil && result == nil {
			result = err
		}
	}
	if official.log != nil {
		if err := official.log.Close(); err != nil && result == nil {
			result = err
		}
	}
	if official.directory != "" {
		if err := os.RemoveAll(official.directory); err != nil && result == nil {
			result = err
		}
	}
	return result
}

func officialOutput() string {
	if official.directory == "" {
		return ""
	}
	data, _ := os.ReadFile(filepath.Join(official.directory, "harness.log"))
	return string(data)
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return port, listener.Close()
}

func TestE2EOfficialHarness(t *testing.T) {
	if testing.Short() {
		t.Skip("official harness is disabled by -short")
	}
	for _, scenario := range officialScenarios {
		t.Run(scenario, func(t *testing.T) {
			official.once.Do(func() { official.err = setupOfficial() })
			if official.err != nil {
				t.Fatalf("official harness setup: %v\n%s", official.err, officialOutput())
			}
			serverPort, err := freePort()
			if err != nil {
				t.Fatal(err)
			}
			clientPort, err := freePort()
			if err != nil {
				t.Fatal(err)
			}
			official.run++
			resultsDir := filepath.Join(official.directory, fmt.Sprintf("case-%03d", official.run))
			request := map[string]any{"scenario_id": scenario, "server_port": serverPort, "client_port": clientPort, "results_dir": resultsDir}
			if err := official.requests.Encode(request); err != nil {
				t.Fatalf("official harness request: %v\n%s", err, officialOutput())
			}
			var result struct {
				Scenario  string `json:"scenario_id"`
				Server    string `json:"server_impl"`
				Client    string `json:"client_impl"`
				Status    string `json:"status"`
				Reason    string `json:"reason"`
				Directory string `json:"case_dir"`
			}
			if err := official.results.Decode(&result); err != nil {
				t.Fatalf("official harness verdict: %v\n%s", err, officialOutput())
			}
			if result.Scenario != scenario || result.Server != "aiosendspin" || result.Client != "sendspin-player-go" {
				t.Fatalf("unexpected official result: %+v", result)
			}
			if result.Status != "passed" {
				t.Errorf("official verdict %s: %s\n%s", result.Status, result.Reason, officialOutput())
				for _, role := range []string{"server", "client"} {
					data, _ := os.ReadFile(filepath.Join(result.Directory, role+".log"))
					t.Logf("%s adapter:\n%s", role, data)
				}
			} else {
				t.Logf("official verdict: %s", result.Reason)
			}
		})
	}
}

func writeFixture(path string) error {
	var data bytes.Buffer
	info := &meta.StreamInfo{SampleRate: 48000, NChannels: 2, BitsPerSample: 16, BlockSizeMin: 960, BlockSizeMax: 960, NSamples: 48000}
	encoder, err := flac.NewEncoder(&data, info)
	if err != nil {
		return err
	}
	for start := 0; start < 48000; start += 960 {
		samples := make([]int32, 960)
		for i := range samples {
			samples[i] = int32((start+i)%2001 - 1000)
		}
		subframe := func() *frame.Subframe {
			return &frame.Subframe{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: samples, NSamples: len(samples)}
		}
		f := &frame.Frame{Header: frame.Header{BlockSize: 960, SampleRate: 48000, Channels: frame.ChannelsLR, BitsPerSample: 16}, Subframes: []*frame.Subframe{subframe(), subframe()}}
		if err := encoder.WriteFrame(f); err != nil {
			return err
		}
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data.Bytes(), 0600)
}
