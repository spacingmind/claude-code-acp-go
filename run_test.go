package acp

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestRunBlocksUntilReaderCloses(t *testing.T) {
	r, w := io.Pipe()

	var out bytes.Buffer

	done := make(chan error, 1)

	go func() {
		done <- Run(r, &out)
	}()

	select {
	case <-done:
		t.Fatal("Run() returned before the reader closed")
	case <-time.After(50 * time.Millisecond):
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after the reader closed")
	}
}
