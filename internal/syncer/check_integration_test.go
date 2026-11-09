//go:build integration

package syncer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"owysync/internal/remote"
	"owysync/internal/standtest"
)

func runCheck(t *testing.T, p *standtest.Project) (string, *Env, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	env, err := Check(ctx, p.Dir, &out)
	if env != nil {
		t.Cleanup(func() { env.Close() })
	}
	return out.String(), env, err
}

func TestCheckOK(t *testing.T) {
	for _, target := range standtest.Targets(t) {
		for _, user := range standtest.Users {
			t.Run(target.Name+"/"+user, func(t *testing.T) {
				p := standtest.Setup(t, target, user, "/home/"+user+"/proj")
				out, env, err := runCheck(t, p)
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				if strings.Contains(out, "FAIL") || strings.Count(out, ": ok") != 8 {
					t.Fatalf("expected 8 ok:\n%s", out)
				}
				if env.Root != "/home/"+user+"/proj" || env.Stat != remote.StatGNU {
					t.Fatalf("root=%q stat=%v", env.Root, env.Stat)
				}
			})
		}
	}
}

// wantFail checks that check n is the first failure and that the run stopped there.
func wantFail(t *testing.T, out string, err error, n string, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	t.Log(last)
	if !strings.HasPrefix(last, n+". ") || !strings.Contains(last, "FAIL: ") || !strings.Contains(last, substr) {
		t.Fatalf("expected FAIL at check %s with %q:\n%s", n, substr, out)
	}
	if strings.Count(out, "FAIL") != 1 {
		t.Fatalf("checks continued after a failure:\n%s", out)
	}
}

func TestCheckFailures(t *testing.T) {
	target := standtest.Targets(t)[0]
	const user = "bashuser"
	good := "/home/" + user + "/proj"

	t.Run("missing ignore", func(t *testing.T) {
		p := standtest.Setup(t, target, user, good)
		if err := os.Remove(filepath.Join(p.Dir, ".owysync", "ignore")); err != nil {
			t.Fatal(err)
		}
		out, _, err := runCheck(t, p)
		wantFail(t, out, err, "1", "ignore")
	})
	t.Run("missing marker", func(t *testing.T) {
		out, _, err := runCheck(t, standtest.Setup(t, target, user, "/home/"+user+"/nomarker"))
		wantFail(t, out, err, "8", "marker")
	})
	t.Run("missing directory", func(t *testing.T) {
		out, _, err := runCheck(t, standtest.Setup(t, target, user, "/home/"+user+"/none"))
		wantFail(t, out, err, "7", "does not exist")
	})
	t.Run("wrong port", func(t *testing.T) {
		p := standtest.Setup(t, target, user, good)
		p.WriteConfig(t, 1, good)
		out, _, err := runCheck(t, p)
		wantFail(t, out, err, "2", "port")
	})
	t.Run("host not in known_hosts", func(t *testing.T) {
		p := standtest.Setup(t, target, user, good)
		os.WriteFile(p.KnownHosts, nil, 0o600)
		out, _, err := runCheck(t, p)
		wantFail(t, out, err, "3", "ssh -p")
	})
	t.Run("foreign host key", func(t *testing.T) {
		p := standtest.Setup(t, target, user, good)
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		key, _ := ssh.NewPublicKey(pub)
		line := knownhosts.Line([]string{knownhosts.Normalize(fmt.Sprintf("%s:%d", target.Host, target.Port))}, key)
		os.WriteFile(p.KnownHosts, []byte(line+"\n"), 0o600)
		out, _, err := runCheck(t, p)
		wantFail(t, out, err, "3", "does not match")
	})
	t.Run("foreign user key", func(t *testing.T) {
		p := standtest.Setup(t, target, user, good)
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		block, _ := ssh.MarshalPrivateKey(priv, "")
		keyFile := t.TempDir() + "/id"
		os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)
		cfg := fmt.Sprintf("host = %s\nport = %d\nuser = %s\nidentity_file = %s\nremote_root = %s\n", target.Host, target.Port, user, keyFile, good)
		os.WriteFile(p.Dir+"/.owysync/config", []byte(cfg), 0o644)
		out, _, err := runCheck(t, p)
		wantFail(t, out, err, "4", "")
	})
}
