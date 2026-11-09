// Package config reads .owysync/config (spec §4).
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Dir is the service directory in the local root.
const Dir = ".owysync"

type Config struct {
	Host          string
	Port          int
	User          string
	IdentityFile  string // empty means ssh-agent only
	RemoteRoot    string
	FileMode      uint32
	DirMode       uint32
	DebounceMS    int
	MTimeWindow   int // seconds
	Concurrency   int
	BulkThreshold int  // 0 disables bulk mode
	Sound         bool // chime after a sync that changed the remote
}

func defaults() Config {
	return Config{Port: 22, FileMode: 0o644, DirMode: 0o755, DebounceMS: 100, Concurrency: 64, BulkThreshold: 100, Sound: true}
}

// Path returns the path of the config file in the local directory.
func Path(localDir string) string { return filepath.Join(localDir, Dir, "config") }

// Load reads the config from localDir/.owysync/config. It does not search parent directories.
func Load(localDir string) (*Config, error) {
	file := Path(localDir)
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file %s not found: the directory is not initialized, run `owysync init`", file)
		}
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	c, err := Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return c, nil
}

// Parse parses config text. Errors include the line number.
func Parse(text string) (*Config, error) {
	c := defaults()
	seen := map[string]bool{}
	portKey := 0 // line number of the port key; 0 means unset
	hostPort := 0
	text = strings.TrimPrefix(text, "\xEF\xBB\xBF") // BOM added by Notepad
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d: expected `key = value`, got %q", n, line)
		}
		if seen[key] {
			return nil, fmt.Errorf("line %d: key %q is set twice", n, key)
		}
		seen[key] = true
		if val == "" {
			return nil, fmt.Errorf("line %d: key %q has an empty value; remove the line or comment it out", n, key)
		}
		var err error
		switch key {
		case "host":
			var port int
			c.Host, port, err = splitHostPort(val)
			if port != 0 {
				c.Port, hostPort = port, n
			}
		case "port":
			c.Port, err = parseInt(val, 1, 65535)
			portKey = n
		case "user":
			c.User = val
		case "identity_file":
			c.IdentityFile = val
		case "remote_root":
			if !strings.HasPrefix(val, "/") {
				err = fmt.Errorf("an absolute path (starting with /) is required, got %q", val)
			}
			c.RemoteRoot = val
		case "file_mode":
			c.FileMode, err = parseMode(val)
		case "dir_mode":
			c.DirMode, err = parseMode(val)
		case "debounce_ms":
			c.DebounceMS, err = parseInt(val, 0, 1<<31-1)
		case "mtime_window":
			c.MTimeWindow, err = parseInt(val, 0, 1<<31-1)
		case "concurrency":
			c.Concurrency, err = parseInt(val, 1, 1<<31-1)
		case "bulk_threshold":
			c.BulkThreshold, err = parseInt(val, 0, 1<<31-1)
		case "sound":
			switch val {
			case "on":
				c.Sound = true
			case "off":
				c.Sound = false
			default:
				err = fmt.Errorf("expected on or off, got %q", val)
			}
		default:
			err = errors.New("unknown key")
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", n, key, err)
		}
	}
	if hostPort != 0 && portKey != 0 {
		return nil, fmt.Errorf("line %d: the port is set in both host and port; keep only one", max(hostPort, portKey))
	}
	for _, k := range []string{"host", "user", "remote_root"} {
		if !seen[k] {
			return nil, fmt.Errorf("required key %q is not set", k)
		}
	}
	return &c, nil
}

// splitHostPort accepts `host`, `host:port`, `[v6]:port` and bare IPv6. port == 0 means not given.
func splitHostPort(v string) (host string, port int, err error) {
	if strings.HasPrefix(v, "[") || strings.Count(v, ":") == 1 {
		h, p, err := net.SplitHostPort(v)
		if err != nil {
			return "", 0, fmt.Errorf("expected host or host:port, got %q", v)
		}
		port, err = parseInt(p, 1, 65535)
		return h, port, err
	}
	return v, 0, nil
}

func parseInt(s string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("expected an integer from %d to %d, got %q", lo, hi, s)
	}
	return n, nil
}

func parseMode(s string) (uint32, error) {
	m, err := strconv.ParseUint(s, 8, 32)
	if err != nil || m > 0o7777 {
		return 0, fmt.Errorf("expected an octal mode such as 0644, got %q", s)
	}
	return uint32(m), nil
}
