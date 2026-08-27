package acp

import (
	"io"
	"log"
	"os"

	claudecode "github.com/spacingmind/claude-agent-sdk-go"
)

// Run starts an ACP agent speaking NDJSON JSON-RPC over the given reader
// and writer (stdin/stdout in production) and blocks until the read side
// closes or errors.
func Run(r io.Reader, w io.Writer, clientOpts ...claudecode.Option) error {
	conn := NewConnection(r, w)
	NewAgent(conn, clientOpts...)
	<-conn.Done()

	return nil
}

// Main is the binary entrypoint for cmd/claude-code-acp.
func Main() {
	log.SetOutput(os.Stderr)

	if err := Run(os.Stdin, os.Stdout); err != nil {
		log.Fatalf("claude-code-acp: %v", err)
	}
}
