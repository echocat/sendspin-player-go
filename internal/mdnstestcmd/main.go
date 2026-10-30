// Command mdnstestcmd runs real multicast tests in an isolated Linux network.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/echocat/sendspin-player-go/internal/devpath"
)

//go:embed Containerfile
var containerfile []byte

func main() {
	engine := flag.String("engine", "auto", "Container engine: auto, podman, docker")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *engine); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func selectEngine(ctx context.Context, requested string) (string, error) {
	var names []string
	if requested == "auto" {
		names = []string{"podman", "docker"}
	} else if requested == "podman" || requested == "docker" {
		names = []string{requested}
	} else {
		return "", errors.New("engine must be auto, podman or docker")
	}
	var failures []error
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		probe, stop := context.WithTimeout(ctx, 15*time.Second)
		out, err := exec.CommandContext(probe, path, "info").CombinedOutput()
		stop()
		if err == nil {
			return path, nil
		}
		failures = append(failures, fmt.Errorf("%s info: %w: %s", name, err, out))
	}
	return "", fmt.Errorf("mDNS E2E requires a running Linux Podman/Docker engine: %w", errors.Join(failures...))
}

func fixtureFile(path string) bool {
	path = filepath.ToSlash(path)
	if path == "internal/e2e/.python-version" {
		return true
	}
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") {
		return false
	}
	if strings.HasSuffix(path, ".go") {
		return true
	}
	if path == "go.mod" || path == "go.sum" || path == "LICENSE" || path == "NOTICES.md" || path == "LICENSES/Apache-2.0.txt" || path == "internal/mdns/LICENSE" {
		return true
	}
	if path == "internal/mdnstestcmd/Containerfile" {
		return true
	}
	if strings.HasPrefix(path, "internal/e2e/") {
		name := strings.TrimPrefix(path, "internal/e2e/")
		switch name {
		case "server.py", "media.py", "mdns.py", "mdns_monitor.py", "pairing_observer.py", "weak_playback.py", "source_controls.py", "pyproject.toml", "uv.lock", ".python-version", "go.mod":
			return true
		}
	}
	return false
}

func buildContext(root, destination string) (string, error) {
	hash := sha256.New()
	_, _ = hash.Write(containerfile)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if filepath.ToSlash(relative) == "audio/native" {
				return filepath.SkipDir // Independent selector module is not used by this fixture.
			}
			if relative != "." && (strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") || entry.Name() == "var" || entry.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !fixtureFile(relative) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "\x00%s\x00", filepath.ToSlash(relative)) // hash.Hash.Write cannot fail.
		_, _ = hash.Write(data)
		output := filepath.Join(destination, "src", relative)
		if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			return err
		}
		return os.WriteFile(output, data, 0600)
	})
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(destination, "Containerfile"), containerfile, 0600); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}

func run(parentCtx context.Context, requested string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return errors.New("run mDNS E2E from the SDK repository root")
	}
	ctx, cancel := context.WithTimeout(parentCtx, 15*time.Minute)
	defer cancel()
	engine, err := selectEngine(ctx, requested)
	if err != nil {
		return err
	}
	if err = devpath.Prepare(root); err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "sendspin-mdns-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	digest, err := buildContext(root, directory)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(root, "var", "mdns"), 0700); err != nil {
		return err
	}
	logPath := filepath.Join(root, "var", "mdns", "build.log")
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	image := "localhost/sendspin-player-go-mdns:" + digest
	fmt.Fprintln(os.Stderr, "[mdns] Building isolated fixture image; registry access is needed only during setup.")
	build := exec.CommandContext(ctx, engine, "build", "--tag", image, "--file", filepath.Join(directory, "Containerfile"), directory)
	build.Stdout = log
	build.Stderr = log
	err = build.Run()
	closeErr := log.Close()
	if err != nil {
		return fmt.Errorf("mDNS fixture build: %w (log: %s)", err, logPath)
	}
	if closeErr != nil {
		return closeErr
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	name := "sendspin-mdns-" + hex.EncodeToString(random[:])
	cleanup := func(args ...string) {
		clean, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = exec.CommandContext(clean, engine, args...).Run()
	}
	subnet := fmt.Sprintf("fd%02x:%02x%02x:%02x%02x:1::/64", random[0], random[1], random[2], random[3], random[4])
	if out, err := exec.CommandContext(ctx, engine, "network", "create", "--internal", "--ipv6", "--subnet", subnet, name).CombinedOutput(); err != nil {
		return fmt.Errorf("create isolated network: %w: %s", err, out)
	}
	defer cleanup("network", "rm", name)
	inspection, err := exec.CommandContext(ctx, engine, "network", "inspect", name).Output()
	if err != nil {
		return err
	}
	var networks []map[string]any
	if err = json.Unmarshal(inspection, &networks); err != nil {
		return err
	}
	if len(networks) != 1 || (networks[0]["internal"] != true && networks[0]["Internal"] != true) {
		return errors.New("refusing mDNS test: container network is not internal")
	}
	extra := name + "-extra"
	extraSubnet := strings.TrimSuffix(subnet, ":1::/64") + ":2::/64"
	if out, err := exec.CommandContext(ctx, engine, "network", "create", "--internal", "--ipv6", "--subnet", extraSubnet, extra).CombinedOutput(); err != nil {
		return fmt.Errorf("create second isolated network: %w: %s", err, out)
	}
	defer cleanup("network", "rm", extra)
	inspection, err = exec.CommandContext(ctx, engine, "network", "inspect", extra).Output()
	if err != nil {
		return err
	}
	networks = nil
	if err = json.Unmarshal(inspection, &networks); err != nil {
		return err
	}
	if len(networks) != 1 || (networks[0]["internal"] != true && networks[0]["Internal"] != true) {
		return errors.New("refusing topology test: second network is not internal")
	}
	observer := name + "-observer"
	defer cleanup("rm", "--force", observer)
	if out, err := exec.CommandContext(ctx, engine, "run", "--detach", "--name", observer, "--network", extra, "--cap-drop=ALL", "--entrypoint", "/workspace/var/python/e2e/bin/python", image, "internal/e2e/mdns_monitor.py").CombinedOutput(); err != nil {
		return fmt.Errorf("start isolated observer: %w: %s", err, out)
	}
	observed, err := exec.CommandContext(ctx, engine, "inspect", observer).Output()
	if err != nil {
		return err
	}
	var containers []struct {
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if err = json.Unmarshal(observed, &containers); err != nil {
		return err
	}
	if len(containers) != 1 || len(containers[0].NetworkSettings.Networks) != 1 {
		return errors.New("observer is not restricted to one private network")
	}
	observerIP := containers[0].NetworkSettings.Networks[extra].IPAddress
	if ip := net.ParseIP(observerIP); ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		return errors.New("observer has no private IPv4 control address")
	}
	observerURL := "http://" + net.JoinHostPort(observerIP, "8081")
	defer cleanup("rm", "--force", name)
	fmt.Fprintln(os.Stderr, "[mdns] Running real multicast and independent Python discovery in an internal network (no published ports or host mounts).")
	command := exec.CommandContext(ctx, engine, "run", "--name", name, "--network", name, "--cap-drop=ALL", "--env", "SENDSPIN_MDNS_TEST_NETWORK=internal", "--env", "SENDSPIN_MDNS_TEST_HOTPLUG=1", "--env", "SENDSPIN_MDNS_OBSERVER_URL="+observerURL, image)
	testLog, err := os.Create(filepath.Join(root, "var", "mdns", "tests.log"))
	if err != nil {
		return err
	}
	defer func() { _ = testLog.Close() }()
	topology := &topologyWriter{destination: io.MultiWriter(os.Stdout, testLog), change: func(attach bool) error {
		action := "disconnect"
		if attach {
			action = "connect"
		}
		changeCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		out, err := exec.CommandContext(changeCtx, engine, "network", action, extra, name).CombinedOutput()
		if err != nil {
			return fmt.Errorf("isolated interface %s: %w: %s", action, err, out)
		}
		return nil
	}}
	command.Stdout = topology
	command.Stderr = io.MultiWriter(os.Stderr, testLog)
	if err = command.Run(); err != nil {
		return fmt.Errorf("isolated mDNS tests: %w", err)
	}
	if topology.stage != 2 {
		return errors.New("isolated interface attach/remove evidence missing")
	}
	return nil
}
