//go:build integration

// Package standtest is shared code of the integration tests on the Docker stand (test/stand, spec §12.1).
// Addresses come from environment variables; defaults are the ports from test/stand/compose.yaml.
package standtest

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Target struct {
	Name, Host string
	Port       int
}

// Users are the stand users with different login shells.
var Users = []string{"bashuser", "tcshuser", "fishuser"}

// Targets reads OWYSYNC_STAND_DEBIAN (default localhost:2201) and OWYSYNC_STAND_ALPINE (default localhost:2202).
func Targets(t testing.TB) []Target {
	var ts []Target
	for _, d := range []struct{ name, env, def string }{
		{"debian", "OWYSYNC_STAND_DEBIAN", "localhost:2201"},
		{"alpine", "OWYSYNC_STAND_ALPINE", "localhost:2202"},
	} {
		addr := os.Getenv(d.env)
		if addr == "" {
			addr = d.def
		}
		h, p, err := net.SplitHostPort(addr)
		port, perr := strconv.Atoi(p)
		if err != nil || perr != nil {
			t.Fatalf("%s=%q: host:port expected", d.env, addr)
		}
		ts = append(ts, Target{d.name, h, port})
	}
	return ts
}

// KeyPath returns OWYSYNC_STAND_KEY or test/stand/key/id_ed25519 (created by test/stand/up.sh).
func KeyPath() string {
	if k := os.Getenv("OWYSYNC_STAND_KEY"); k != "" {
		return k
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "test", "stand", "key", "id_ed25519")
}

// Project is a temporary local directory with .owysync/config and an isolated known_hosts.
type Project struct {
	Dir        string
	KnownHosts string
	target     Target
	user       string
}

// Setup creates a local directory for a stand user and replaces the process home directory
// (known_hosts) with a temporary one that already holds the stand's host key.
func Setup(t *testing.T, target Target, user, remoteRoot string) *Project {
	t.Helper()
	if _, err := os.Stat(KeyPath()); err != nil {
		t.Fatalf("no stand key (%v): run test/stand/up.sh", err)
	}
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := &Project{Dir: t.TempDir(), KnownHosts: filepath.Join(home, ".ssh", "known_hosts"), target: target, user: user}
	line, err := hostKeyLine(target)
	if err != nil {
		t.Fatalf("stand %s is unavailable: %v (is it started with test/stand/up.sh?)", target.Name, err)
	}
	if err := os.WriteFile(p.KnownHosts, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.WriteConfig(t, target.Port, remoteRoot)
	return p
}

func (p *Project) WriteConfig(t *testing.T, port int, remoteRoot string) {
	t.Helper()
	cfg := fmt.Sprintf("host = %s\nport = %d\nuser = %s\nidentity_file = %s\nremote_root = %s\n",
		p.target.Host, port, p.user, KeyPath(), remoteRoot)
	if err := os.MkdirAll(filepath.Join(p.Dir, ".owysync"), 0o755); err != nil {
		t.Fatal(err)
	}
	// check requires an ignore file; an empty one is enough here.
	if err := os.WriteFile(filepath.Join(p.Dir, ".owysync", "ignore"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Dir, ".owysync", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// hostKeyLine fetches the stand's host key and returns a known_hosts line.
func hostKeyLine(t Target) (string, error) {
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	var key ssh.PublicKey
	_, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: "probe",
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			key = k
			return errors.New("key captured")
		},
	})
	if key == nil {
		return "", err
	}
	return knownhosts.Line([]string{knownhosts.Normalize(addr)}, key), nil
}
