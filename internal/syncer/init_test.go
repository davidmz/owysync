package syncer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"owysync/internal/config"
	"owysync/internal/ignore"
)

func TestConfigText(t *testing.T) {
	text := configText("example.com:2222", "bob", "/srv/app")
	got, err := config.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "example.com" || got.Port != 2222 || got.User != "bob" || got.RemoteRoot != "/srv/app" {
		t.Fatalf("entered values lost: %+v", got)
	}

	// The commented keys hold the defaults: uncommenting them (without the port, which would clash
	// with host:port) changes nothing.
	text = configText("example.com", "bob", "/srv/app")
	want, err := config.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	uncomment := regexp.MustCompile(`(?m)^# (\w+ = \S+)$`)
	all, err := config.Parse(uncomment.ReplaceAllString(text, "$1"))
	if err != nil {
		t.Fatalf("uncommented config: %v", err)
	}
	all.IdentityFile = "" // the example path is not a default
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("commented values differ from the defaults:\n%+v\n%+v", all, want)
	}
}

func TestIgnoreTemplate(t *testing.T) {
	set, err := ignore.Parse(ignoreTemplate)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git/config", "a/~lock", "x.swp", "Thumbs.db", "pic.pyc"} {
		if !set.Excluded(p) {
			t.Errorf("%s must be excluded by the template", p)
		}
	}
	for _, p := range []string{"node_modules/x.js", "app.log", "src/main.go"} {
		if set.Excluded(p) {
			t.Errorf("%s must not be excluded by the template", p)
		}
	}
}

func runInit(t *testing.T, dir string, remoteOnly bool, input string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	err := Init(ctx, dir, remoteOnly, strings.NewReader(input), &out)
	return out.String(), err
}

func TestInitDialogAndRepeat(t *testing.T) {
	dir := t.TempDir()
	// The first host is invalid and is asked again; port 1 refuses connections, so init stops at check 2.
	out, err := runInit(t, dir, false, "host:99999\nlocalhost:1\nbob\n/srv/app\n")
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("expected ErrFailed, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "2. TCP connection: FAIL") || !strings.Contains(out, "init --remote") || strings.Contains(out, "3. ") {
		t.Fatalf("expected the failure at check 2 and the hint:\n%s", out)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "localhost" || cfg.Port != 1 || cfg.User != "bob" || cfg.RemoteRoot != "/srv/app" {
		t.Fatalf("config: %+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(dir, ".owysync", "ignore")); err != nil {
		t.Fatal(err)
	}

	// A repeated init is an error and writes nothing.
	before, _ := os.ReadFile(config.Path(dir))
	_, err = runInit(t, dir, false, "other\nbob\n/x\n")
	if err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("expected the already-initialized error, got %v", err)
	}
	if after, _ := os.ReadFile(config.Path(dir)); !bytes.Equal(before, after) {
		t.Fatal("a repeated init changed the config")
	}
}

func TestInitInterruptedDialogWritesNothing(t *testing.T) {
	dir := t.TempDir()
	// The input ends before the last answer (what Ctrl+C or a closed terminal looks like).
	if _, err := runInit(t, dir, false, "localhost\nbob\n"); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(dir, ".owysync")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".owysync must not exist: %v", err)
	}
}

func TestInitRemoteWithoutConfig(t *testing.T) {
	out, err := runInit(t, t.TempDir(), true, "")
	if !errors.Is(err, ErrFailed) || !strings.Contains(out, "1. config and ignore: FAIL") {
		t.Fatalf("got %v\n%s", err, out)
	}
}
