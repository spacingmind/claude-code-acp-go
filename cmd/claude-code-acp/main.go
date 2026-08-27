// Command claude-code-acp runs Claude Code as an ACP (Agent Client
// Protocol) agent over stdin/stdout.
package main

import (
	"log"
	"os"

	acp "github.com/spacingmind/claude-code-acp-go"
)

func main() {
	log.SetOutput(os.Stderr)

	if err := acp.Run(os.Stdin, os.Stdout); err != nil {
		log.Fatalf("claude-code-acp: %v", err)
	}
}
