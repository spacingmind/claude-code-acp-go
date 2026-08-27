package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
)

const maxLineBytes = 1024 * 1024

// jsonrpcVersion is the JSON-RPC 2.0 envelope discriminator.
const jsonrpcVersion = "2.0"

// RequestHandler handles an inbound JSON-RPC request. It returns the result
// to marshal into the response, or an error to send as a JSON-RPC error.
type RequestHandler func(ctx context.Context, params json.RawMessage) (any, error)

// NotificationHandler handles an inbound JSON-RPC notification (no response).
type NotificationHandler func(ctx context.Context, params json.RawMessage)

// Connection speaks ACP JSON-RPC over NDJSON: one reader goroutine reads
// lines from r, dispatching inbound requests/notifications to registered
// handlers and correlating responses to outbound requests via a pending
// map. Writes are mutex-guarded so concurrent callers (e.g. several
// in-flight session/request_permission calls) are safe.
type Connection struct {
	r io.Reader
	w io.Writer

	writeMu sync.Mutex

	nextID int64

	mu            sync.Mutex
	pending       map[RequestID]chan Response
	requests      map[string]RequestHandler
	notifications map[string]NotificationHandler

	done chan struct{}
	once sync.Once
}

// NewConnection constructs a Connection and starts its single reader
// goroutine. Closing happens implicitly when r returns EOF (or errors);
// outbound calls then fail with a closed-connection error.
func NewConnection(r io.Reader, w io.Writer) *Connection {
	c := &Connection{
		r:             r,
		w:             w,
		pending:       make(map[RequestID]chan Response),
		requests:      make(map[string]RequestHandler),
		notifications: make(map[string]NotificationHandler),
		done:          make(chan struct{}),
	}
	go c.readLoop()

	return c
}

// Done returns a channel closed when the connection's read loop exits.
func (c *Connection) Done() <-chan struct{} { return c.done }

func (c *Connection) handleRequest(ctx context.Context, _ string, h RequestHandler, req Request) {
	go func() {
		result, err := h(ctx, req.Params)
		if err != nil {
			rpcErr := &RequestError{}

			ok := errors.As(err, &rpcErr)
			if !ok {
				rpcErr = &RequestError{Code: CodeInternalError, Message: err.Error()}
			}

			c.sendResponse(*req.ID, nil, rpcErr)

			return
		}

		raw, merr := json.Marshal(result)
		if merr != nil {
			c.sendResponse(*req.ID, nil, &RequestError{Code: CodeInternalError, Message: merr.Error()})
			return
		}

		c.sendResponse(*req.ID, raw, nil)
	}()
}

func (c *Connection) dispatch(req Request) {
	c.mu.Lock()
	rh := c.requests[req.Method]
	nh := c.notifications[req.Method]
	c.mu.Unlock()

	if req.ID != nil {
		if rh == nil {
			c.sendResponse(*req.ID, nil, &RequestError{Code: CodeMethodNotFound, Message: "method not found: " + req.Method})
			return
		}

		c.handleRequest(context.Background(), req.Method, rh, req)

		return
	}

	if nh != nil {
		// Dispatched synchronously in the reader goroutine so
		// notification order on the wire is preserved for handlers
		// (mirrors the SDK's sequential message handling).
		nh(context.Background(), req.Params)
	}
}

func (c *Connection) readLoop() {
	defer close(c.done)

	scanner := bufio.NewScanner(c.r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for scanner.Scan() {
		var env struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *RequestID      `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			Result  json.RawMessage `json:"result"`
			Error   *RequestError   `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			// One bad line doesn't kill the connection.
			continue
		}

		if env.JSONRPC != jsonrpcVersion {
			continue
		}

		if env.Method != "" {
			c.dispatch(Request{JSONRPC: env.JSONRPC, ID: env.ID, Method: env.Method, Params: env.Params})
			continue
		}

		if env.ID != nil {
			c.resolve(*env.ID, Response{JSONRPC: jsonrpcVersion, ID: env.ID, Result: env.Result, Error: env.Error})
		}
		// Neither method nor id: silently dropped.
	}

	c.failAllPending(scanner.Err())
}

// resolve delivers an inbound response to its waiting caller.
func (c *Connection) resolve(id RequestID, resp Response) {
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()

	if ch != nil {
		ch <- resp

		close(ch)
	}
}

// failAllPending unblocks all in-flight outbound callers (read loop exit).
func (c *Connection) failAllPending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[RequestID]chan Response)
	c.mu.Unlock()

	for _, ch := range pending {
		resp := Response{JSONRPC: jsonrpcVersion, Error: &RequestError{Code: CodeInternalError, Message: "connection closed"}}
		if err != nil {
			resp.Error.Message = fmt.Sprintf("connection closed: %v", err)
		}

		ch <- resp

		close(ch)
	}
}

func (c *Connection) writeLine(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	_, err = fmt.Fprintf(c.w, "%s\n", data)

	return err
}

func (c *Connection) sendResponse(id RequestID, result json.RawMessage, rpcErr *RequestError) {
	resp := Response{JSONRPC: jsonrpcVersion, ID: &id, Result: result, Error: rpcErr}
	if err := c.writeLine(resp); err != nil {
		log.Printf("acp: write response: %v", err)
	}
}

// RegisterRequest registers a handler for an inbound request method.
func (c *Connection) RegisterRequest(method string, h RequestHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requests[method] = h
}

// RegisterNotification registers a handler for an inbound notification method.
func (c *Connection) RegisterNotification(method string, h NotificationHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.notifications[method] = h
}

// Notify sends an outbound notification.
func (c *Connection) Notify(method string, params any) error {
	return c.writeLine(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{jsonrpcVersion, method, params})
}

// Call sends an outbound request and blocks until the correlated response
// arrives or ctx is done.
func (c *Connection) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.nextID++
	id := RequestID{kind: idNumber, num: c.nextID}
	ch := make(chan Response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.writeLine(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      RequestID       `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{jsonrpcVersion, id, method, raw}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()

		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}

		return resp.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()

		return nil, ctx.Err()
	}
}

// Shutdown stops the connection's outbound writes. The reader goroutine
// exits on its own when the underlying reader closes.
func (c *Connection) Shutdown() {
	c.once.Do(func() {})
}
