//go:build !windows

package remote

import "os"

// agentOpenFlags: the agent pipe exists only on Windows; elsewhere opening it simply fails.
const agentOpenFlags = os.O_RDWR
