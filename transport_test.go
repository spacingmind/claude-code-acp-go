package acp

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestFailAllPendingUnblocksOutboundCallsOnReaderClose(t *testing.T) {
	reqR, reqW := io.Pipe() // outbound requests written here, drained below so writes don't block
	respR, _ := io.Pipe()   // no responses ever written -- readLoop blocks then EOFs on Close

	c := NewConnection(respR, reqW)

	go io.Copy(io.Discard, reqR) //nolint:errcheck // best-effort drain, only needed to unblock writeLine

	t.Cleanup(func() {
		_ = reqR.Close()
		_ = respR.Close()
		_ = reqW.Close()
	})

	errCh := make(chan error, 1)

	go func() {
		_, err := c.Call(context.Background(), "test/op", map[string]any{})
		errCh <- err
	}()

	// Let the Call register itself as pending before we close the reader.
	time.Sleep(20 * time.Millisecond)

	if err := respR.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Call() = nil error, want connection-closed error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call() did not unblock after reader closed")
	}

	select {
	case <-c.Done():
	default:
		t.Error("Done() channel not closed after reader EOF")
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	r, w := io.Pipe()
	c := NewConnection(r, w)

	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	c.Shutdown()
	c.Shutdown() // must not panic when called twice
}
