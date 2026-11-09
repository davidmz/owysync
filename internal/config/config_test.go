package config

import (
	"strings"
	"testing"
)

func TestParseDefaults(t *testing.T) {
	c, err := Parse("\ufeff# comment\r\nhost = example.org\r\n  user=dev  \nremote_root = /srv/app\n\n")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Host: "example.org", Port: 22, User: "dev", RemoteRoot: "/srv/app",
		FileMode: 0o644, DirMode: 0o755, DebounceMS: 100, Concurrency: 64, BulkThreshold: 100}
	if *c != want {
		t.Fatalf("got %+v\nwant %+v", *c, want)
	}
}

func TestParseAllKeys(t *testing.T) {
	c, err := Parse(`host = h
port = 2222
user = u
identity_file = C:\keys\id
remote_root = /r
file_mode = 0600
dir_mode = 0700
debounce_ms = 5
mtime_window = 2
concurrency = 8
bulk_threshold = 0`)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Host: "h", Port: 2222, User: "u", IdentityFile: `C:\keys\id`, RemoteRoot: "/r",
		FileMode: 0o600, DirMode: 0o700, DebounceMS: 5, MTimeWindow: 2, Concurrency: 8, BulkThreshold: 0}
	if *c != want {
		t.Fatalf("got %+v\nwant %+v", *c, want)
	}
}

func TestParseHostPort(t *testing.T) {
	for in, want := range map[string]struct {
		host string
		port int
	}{
		"h":            {"h", 22},
		"h:2200":       {"h", 2200},
		"[::1]:2200":   {"::1", 2200},
		"fe80::1":      {"fe80::1", 22},
		"127.0.0.1:22": {"127.0.0.1", 22},
	} {
		c, err := Parse("host = " + in + "\nuser = u\nremote_root = /r")
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if c.Host != want.host || c.Port != want.port {
			t.Errorf("%s: got %s:%d, want %s:%d", in, c.Host, c.Port, want.host, want.port)
		}
	}
}

func TestParseErrors(t *testing.T) {
	const base = "host = h\nuser = u\nremote_root = /r\n"
	for _, tc := range []struct{ name, text, want string }{
		{"missing host", "user = u\nremote_root = /r", `"host"`},
		{"missing user", "host = h\nremote_root = /r", `"user"`},
		{"missing remote_root", "host = h\nuser = u", `"remote_root"`},
		{"not key=value", base + "oops\n", "line 4"},
		{"unknown key", base + "colour = red\n", "line 4: colour: unknown key"},
		{"duplicate", base + "user = x\n", "line 4"},
		{"empty value", base + "port =\n", "line 4"},
		{"port not a number", base + "port = ssh\n", "line 4"},
		{"port 0", base + "port = 0\n", "line 4"},
		{"port 70000", base + "port = 70000\n", "line 4"},
		{"port twice", "host = h:23\nuser = u\nremote_root = /r\nport = 22\n", "set in both host"},
		{"relative root", "host = h\nuser = u\nremote_root = srv\n", "absolute"},
		{"mode not octal", base + "file_mode = 0689\n", "octal"},
		{"mode too large", base + "dir_mode = 17777\n", "octal"},
		{"concurrency 0", base + "concurrency = 0\n", "line 4"},
		{"negative debounce", base + "debounce_ms = -1\n", "line 4"},
		{"bad host:port", "host = h:x\nuser = u\nremote_root = /r", "line 1"},
	} {
		_, err := Parse(tc.text)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want substring %q", tc.name, err, tc.want)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "owysync init") {
		t.Fatalf("err = %v", err)
	}
}
