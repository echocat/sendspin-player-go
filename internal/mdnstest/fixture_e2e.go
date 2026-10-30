//go:build e2e

package mdnstest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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
			probe, stop := context.WithTimeout(ctx, 15*time.Second)
			field := "{{.OSType}}"
			if name == "podman" {
				field = "{{.Host.OS}}"
			}
			osType, inspectErr := exec.CommandContext(probe, path, "info", "--format", field).Output()
			stop()
			if inspectErr == nil && strings.TrimSpace(string(osType)) == "linux" {
				return path, nil
			}
			failures = append(failures, fmt.Errorf("%s must provide Linux containers", name))
			continue
		}
		failures = append(failures, fmt.Errorf("%s info: %w: %s", name, err, out))
	}
	return "", fmt.Errorf("mDNS E2E requires a running Linux Podman/Docker engine: %w", errors.Join(failures...))
}

func runFixture(parentCtx context.Context, engine, root string, testArgs []string) error {
	var err error
	if _, err = os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return errors.New("mDNS fixture repository root has no go.mod")
	}
	ctx, cancel := context.WithTimeout(parentCtx, 15*time.Minute)
	defer cancel()
	output := filepath.Join(root, "var", "mdns")
	if err = os.MkdirAll(output, 0700); err != nil {
		return err
	}
	directory, err := os.MkdirTemp(output, "fixture-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	digest, err := buildContext(root, directory)
	if err != nil {
		return err
	}
	logPath := filepath.Join(output, "build.log")
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
	if out, err := exec.CommandContext(ctx, engine, "run", "--detach", "--name", observer, "--network", extra, "--cap-drop=ALL", "--env", "SENDSPIN_MDNS_TEST_NETWORK=internal", "--env", "SENDSPIN_MDNS_ROLE=observer", image).CombinedOutput(); err != nil {
		return fmt.Errorf("start isolated observer: %w: %s", err, out)
	}
	ready, stopReady := context.WithTimeout(ctx, 15*time.Second)
	defer stopReady()
	for {
		out, err := exec.CommandContext(ready, engine, "logs", observer).CombinedOutput()
		if err == nil && strings.Contains(string(out), "[mdns-observer] ready\n") {
			break
		}
		if ready.Err() != nil {
			return fmt.Errorf("independent Go observer did not become ready: %w: %s", ready.Err(), out)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ready.Done():
			timer.Stop()
		case <-timer.C:
		}
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
	fmt.Fprintln(os.Stderr, "[mdns] Running real multicast with independent Go DNS peers in private networks (no published ports or host mounts).")
	args := []string{"run", "--name", name, "--network", name, "--cap-drop=ALL", "--env", "SENDSPIN_MDNS_TEST_NETWORK=internal", "--env", "SENDSPIN_MDNS_ROLE=tests", "--env", "SENDSPIN_MDNS_TEST_HOTPLUG=1", "--env", "SENDSPIN_MDNS_OBSERVER_URL=" + observerURL, image}
	args = append(args, testArgs...)
	command := exec.CommandContext(ctx, engine, args...)
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
		return err
	}
	if topology.stage != 0 {
		return errors.New("isolated interface was not detached")
	}
	return nil
}
