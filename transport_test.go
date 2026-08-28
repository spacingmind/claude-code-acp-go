package acp

import (
	"context"
	"errors"
	"io"
	"sync"
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
	c.Shutdown() // must not panic (double channel close) when called twice

	if err := c.Notify("test/notify", map[string]any{}); !errors.Is(err, errShutdown) {
		t.Fatalf("Notify() after Shutdown() = %v, want %v", err, errShutdown)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := c.Call(ctx, "test/op", map[string]any{}); !errors.Is(err, errShutdown) {
		t.Fatalf("Call() after Shutdown() = %v, want %v", err, errShutdown)
	}

	if err := c.writeLine(struct{}{}); !errors.Is(err, errShutdown) {
		t.Fatalf("writeLine() after Shutdown() = %v, want %v", err, errShutdown)
	}
}

func TestShutdownConcurrentWithWrites(t *testing.T) {
	reqR, reqW := io.Pipe()
	respR, _ := io.Pipe()

	c := NewConnection(respR, reqW)

	go io.Copy(io.Discard, reqR) //nolint:errcheck // best-effort drain, only needed to unblock writeLine

	t.Cleanup(func() {
		_ = reqR.Close()
		_ = respR.Close()
		_ = reqW.Close()
	})

	var wg sync.WaitGroup

	wg.Go(func() {
		for range 50 {
			c.Shutdown()
		}
	})

	for range 10 {
		wg.Go(func() {
			for range 20 {
				// Either succeeds or fails with errShutdown -- must never
				// panic or race; failures are expected once Shutdown lands.
				_ = c.Notify("test/notify", map[string]any{})

				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				_, _ = c.Call(ctx, "test/op", map[string]any{})

				cancel()
			}
		})
	}

	wg.Wait()

	// After the dust settles, all writes must fail.
	if err := c.Notify("test/notify", nil); !errors.Is(err, errShutdown) {
		t.Fatalf("Notify() after concurrent Shutdown() = %v, want %v", err, errShutdown)
	}
}
