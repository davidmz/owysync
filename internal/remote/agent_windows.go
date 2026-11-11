package remote

import (
	"os"

	"github.com/davidmz/go-pageant"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
)

// agentOpenFlags opens the agent pipe for overlapped I/O. The agent client reads and writes
// from different goroutines; on a synchronous pipe handle Windows serializes the two calls,
// and a pending read blocks the request write forever.
const agentOpenFlags = os.O_RDWR | windows.FILE_FLAG_OVERLAPPED

// pageantSigners returns the keys of a running PuTTY Pageant, if any.
func pageantSigners() []ssh.Signer {
	if !pageant.Available() {
		return nil
	}
	ss, _ := pageant.New().Signers()
	return ss
}
