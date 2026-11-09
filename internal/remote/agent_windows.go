package remote

import (
	"os"

	"golang.org/x/sys/windows"
)

// agentOpenFlags opens the agent pipe for overlapped I/O. The agent client reads and writes
// from different goroutines; on a synchronous pipe handle Windows serializes the two calls,
// and a pending read blocks the request write forever.
const agentOpenFlags = os.O_RDWR | windows.FILE_FLAG_OVERLAPPED
