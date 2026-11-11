//go:build !windows

package remote

import (
	"os"

	"golang.org/x/crypto/ssh"
)

// agentOpenFlags: the agent pipe exists only on Windows; elsewhere opening it simply fails.
const agentOpenFlags = os.O_RDWR

// pageantSigners: Pageant exists only on Windows.
func pageantSigners() []ssh.Signer { return nil }
