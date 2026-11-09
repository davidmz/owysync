// Package remote handles the SSH connection and operations on the remote (spec §4, 8, 9).
package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// AgentPipe is the Windows OpenSSH agent pipe (spec §4).
const AgentPipe = `\\.\pipe\openssh-ssh-agent`

type Options struct {
	Host         string
	Port         int
	User         string
	IdentityFile string
	// KnownHostsFile defaults to ~/.ssh/known_hosts (%USERPROFILE%\.ssh\known_hosts).
	KnownHostsFile string
	Timeout        time.Duration // for TCP and the handshake; defaults to 10s
}

// Stage is a connection phase; it matches checks 2-4 of the check command.
type Stage int

const (
	StageDial Stage = iota + 2
	StageHostKey
	StageAuth
)

// ConnError is a Connect failure; Stage says which phase failed.
type ConnError struct {
	Stage Stage
	Err   error
}

func (e *ConnError) Error() string { return e.Err.Error() }
func (e *ConnError) Unwrap() error { return e.Err }

type Client struct {
	ssh  *ssh.Client
	sftp *sftp.Client
}

func (o Options) addr() string { return net.JoinHostPort(o.Host, strconv.Itoa(o.Port)) }

// Connect establishes the SSH connection. The error is always a *ConnError.
func Connect(ctx context.Context, o Options) (*Client, error) {
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	if o.KnownHostsFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, &ConnError{StageHostKey, fmt.Errorf("home directory not found: %w", err)}
		}
		o.KnownHostsFile = filepath.Join(home, ".ssh", "known_hosts")
	}
	addr := o.addr()

	conn, err := (&net.Dialer{Timeout: o.Timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, &ConnError{StageDial, fmt.Errorf("cannot connect to %s: %w; check host and port", addr, err)}
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()

	check, err := knownhosts.New(o.KnownHostsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &ConnError{StageHostKey, fmt.Errorf("file %s not found; connect to the host once with the standard ssh client: %s", o.KnownHostsFile, o.sshHint())}
		}
		return nil, &ConnError{StageHostKey, fmt.Errorf("reading %s: %w", o.KnownHostsFile, err)}
	}
	algs, err := o.hostKeyAlgorithms(check, conn.RemoteAddr())
	if err != nil {
		return nil, &ConnError{StageHostKey, err}
	}

	var hostErr, keyErr error // causes that the handshake would otherwise hide behind a generic "handshake failed"
	var agentConn io.Closer
	defer func() {
		if agentConn != nil {
			agentConn.Close()
		}
	}()
	cfg := &ssh.ClientConfig{
		User:              o.User,
		HostKeyAlgorithms: algs,
		HostKeyCallback: func(host string, remote net.Addr, key ssh.PublicKey) error {
			if hostErr = check(host, remote, key); hostErr != nil {
				hostErr = o.explainHostKey(hostErr)
			}
			return hostErr
		},
	}
	cfg.Auth = []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		signers, err := o.signers(&agentConn)
		keyErr = err
		return signers, nil // an identity_file error matters only if login fails
	})}

	conn.SetDeadline(time.Now().Add(o.Timeout))
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		switch {
		case hostErr != nil:
			return nil, &ConnError{StageHostKey, hostErr}
		case keyErr != nil:
			return nil, &ConnError{StageAuth, keyErr}
		case strings.Contains(err.Error(), "unable to authenticate"):
			return nil, &ConnError{StageAuth, fmt.Errorf("the server rejected the keys of user %s: add a key to ssh-agent or set identity_file (%w)", o.User, err)}
		case strings.Contains(err.Error(), "no common algorithm for host key"):
			return nil, &ConnError{StageHostKey, fmt.Errorf("the server offers none of the key algorithms recorded in known_hosts (%s); connect with the standard ssh client and update the entry: %s (%w)", strings.Join(algs, ", "), o.sshHint(), err)}
		}
		return nil, &ConnError{StageAuth, fmt.Errorf("SSH handshake: %w", err)}
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return &Client{ssh: ssh.NewClient(sc, chans, reqs)}, nil
}

func (o Options) sshHint() string {
	p := ""
	if o.Port != 22 {
		p = "-p " + strconv.Itoa(o.Port) + " "
	}
	return fmt.Sprintf("ssh %s%s@%s", p, o.User, o.Host)
}

// hostKeyAlgorithms returns the key types recorded in known_hosts for the host (spec §4):
// otherwise x/crypto picks any server algorithm and reports a key mismatch.
// The callback is called with a deliberately foreign key; KeyError.Want holds the recorded ones.
func (o Options) hostKeyAlgorithms(check ssh.HostKeyCallback, remote net.Addr) ([]string, error) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	bogus, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	var ke *knownhosts.KeyError
	if !errors.As(check(o.addr(), remote, bogus), &ke) || len(ke.Want) == 0 {
		return nil, fmt.Errorf("host %s not found in %s; run `%s` once and confirm the key", o.addr(), o.KnownHostsFile, o.sshHint())
	}
	var algs []string
	seen := map[string]bool{}
	for _, w := range ke.Want {
		var list []string
		switch t := w.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			list = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		default:
			list = []string{t}
		}
		for _, a := range list {
			if !seen[a] {
				seen[a] = true
				algs = append(algs, a)
			}
		}
	}
	return algs, nil
}

func (o Options) explainHostKey(err error) error {
	var ke *knownhosts.KeyError
	if errors.As(err, &ke) && len(ke.Want) > 0 {
		w := ke.Want[0]
		return fmt.Errorf("host key of %s does not match the entry in %s:%d: the server may have been spoofed. If the key was changed legitimately, remove the old entry: ssh-keygen -R %s", o.addr(), w.Filename, w.Line, knownhosts.Normalize(o.addr()))
	}
	return err
}

// signers returns the ssh-agent keys, then identity_file. An identity_file error is returned
// together with the agent keys, if any.
func (o Options) signers(agentConn *io.Closer) ([]ssh.Signer, error) {
	var signers []ssh.Signer
	if f, err := os.OpenFile(AgentPipe, agentOpenFlags, 0); err == nil {
		*agentConn = f // closed by Connect: signing happens during the handshake
		signers, _ = agent.NewClient(f).Signers()
	}
	if o.IdentityFile == "" {
		return signers, nil
	}
	data, err := os.ReadFile(o.IdentityFile)
	if err != nil {
		return signers, fmt.Errorf("identity_file: %w", err)
	}
	s, err := ssh.ParsePrivateKey(data)
	var pm *ssh.PassphraseMissingError
	if errors.As(err, &pm) {
		return signers, fmt.Errorf("key %s is protected by a passphrase; add it to ssh-agent: ssh-add %s", o.IdentityFile, o.IdentityFile)
	}
	if err != nil {
		return signers, fmt.Errorf("identity_file %s: %w", o.IdentityFile, err)
	}
	return append(signers, s), nil
}

func (c *Client) Close() error {
	if c.sftp != nil {
		c.sftp.Close()
	}
	return c.ssh.Close()
}

// SFTP opens the SFTP subsystem (once per connection).
func (c *Client) SFTP() (*sftp.Client, error) {
	if c.sftp == nil {
		// Concurrent writes pipeline uploads: one large bulk part goes at link speed (spec §9.1).
		s, err := sftp.NewClient(c.ssh, sftp.UseConcurrentWrites(true))
		if err != nil {
			return nil, fmt.Errorf("SFTP subsystem unavailable: %w", err)
		}
		c.sftp = s
	}
	return c.sftp, nil
}

// RunScript runs script through `sh -s` (spec §8.2): the script goes to stdin, the user's login shell
// is not involved. A non-zero exit code is not returned as an error: it cannot be trusted
// (spec §8.6); the markers in the output decide the result.
func (c *Client) RunScript(ctx context.Context, script string) (stdout, stderr []byte, err error) {
	s, err := c.ssh.NewSession()
	if err != nil {
		return nil, nil, fmt.Errorf("SSH session: %w", err)
	}
	defer s.Close()
	var out, errb bytes.Buffer
	s.Stdin, s.Stdout, s.Stderr = strings.NewReader(script), &out, &errb
	if err := s.Start("sh -s"); err != nil {
		return nil, nil, fmt.Errorf("starting sh -s: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		s.Close()
		return nil, nil, ctx.Err()
	}
	var ee *ssh.ExitError
	var em *ssh.ExitMissingError
	if err != nil && !errors.As(err, &ee) && !errors.As(err, &em) {
		return nil, nil, fmt.Errorf("sh -s: %w", err)
	}
	return out.Bytes(), errb.Bytes(), nil
}
