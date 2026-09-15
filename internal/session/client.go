package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/mayahiro/process-deck/internal/supervisor"
)

// Client controls an existing supervisor without owning its lifetime.
// Close detaches; Shutdown explicitly stops the session and its processes.
type Client struct {
	path       string
	conn       net.Conn
	events     chan supervisor.Event
	done       chan struct{}
	closed     chan struct{}
	closeOnce  sync.Once
	reattached bool
	history    map[string]supervisor.LogBatch

	mu        sync.Mutex
	snapshots []supervisor.Snapshot
	err       error
}

func connect(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, connectTimeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(connectTimeout))
	if err := json.NewEncoder(conn).Encode(request{Version: protocolVersion, Method: "attach"}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	decoder := json.NewDecoder(conn)
	var initial response
	if err := decoder.Decode(&initial); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if initial.Error != "" || initial.Version != protocolVersion {
		_ = conn.Close()
		if initial.Error != "" {
			return nil, errors.New(initial.Error)
		}
		return nil, fmt.Errorf("runtime error: incompatible session protocol")
	}
	_ = conn.SetDeadline(time.Time{})
	client := &Client{
		path: path, conn: conn, events: make(chan supervisor.Event, eventBufferSize),
		done: make(chan struct{}), closed: make(chan struct{}),
		reattached: initial.Reattached, snapshots: initial.Snapshots, history: initial.History,
	}
	go client.read(decoder)
	return client, nil
}

func (c *Client) read(decoder *json.Decoder) {
	defer close(c.done)
	defer close(c.events)
	defer c.conn.Close()
	for {
		var message response
		if err := decoder.Decode(&message); err != nil {
			c.setError(fmt.Errorf("runtime error: session connection lost; processes may still be running; run procdeck again to reconnect: %w", err))
			return
		}
		if message.Snapshots != nil {
			c.mu.Lock()
			c.snapshots = message.Snapshots
			c.mu.Unlock()
		}
		if message.Finished {
			if message.Error != "" {
				c.setError(errors.New(message.Error))
			}
			return
		}
		if message.Event != nil {
			select {
			case c.events <- message.Event.decode():
			case <-c.closed:
				return
			}
		}
	}
}

// Reattached reports whether this connection resumed a previously started session.
func (c *Client) Reattached() bool { return c.reattached }

// Events returns live events in supervisor order until the connection closes.
func (c *Client) Events() <-chan supervisor.Event { return c.events }

// Snapshot returns the most recently received process state without socket I/O.
func (c *Client) Snapshot() []supervisor.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]supervisor.Snapshot(nil), c.snapshots...)
}

// History returns the retained logs at attachment time, ordered by timestamp.
// Live events whose cursors are already in this history are omitted by callers.
func (c *Client) History() []supervisor.Event {
	var events []supervisor.Event
	for name, batch := range c.history {
		for index, entry := range batch.Entries {
			events = append(events, supervisor.Event{
				Kind: supervisor.EventProcessLogLine, Process: name,
				Stream: entry.Stream, Line: entry.Line, Time: entry.Time,
				LogStateValid: true, LogRetained: batch.Retained,
				LogCursor: batch.Cursor - supervisor.LogCursor(len(batch.Entries)-index-1),
			})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	return events
}

// LogsSince reads retained log changes from the session's bounded buffers.
// A transport failure closes the connection and is available from Wait.
func (c *Client) LogsSince(name string, cursor supervisor.LogCursor) supervisor.LogBatch {
	reply, err := c.call(request{Method: "logs", Process: name, Cursor: cursor})
	if err != nil {
		c.setError(err)
		_ = c.Close()
	}
	return reply.Logs
}

// StartProcess starts a stopped process using the session's original configuration.
func (c *Client) StartProcess(name string) error {
	_, err := c.call(request{Method: "start", Process: name})
	return err
}

// StopProcess stops a process and its transitive dependents, cancelling their
// automatic restarts. Dependents are stopped first.
func (c *Client) StopProcess(name string) error {
	_, err := c.call(request{Method: "stop", Process: name})
	return err
}

// RestartProcess restarts a process and its previously active dependents in
// dependency order. Dependents that were already stopped remain stopped.
func (c *Client) RestartProcess(name string) error {
	_, err := c.call(request{Method: "restart", Process: name})
	return err
}

// Shutdown requests graceful termination of all processes and the session,
// acknowledging the request without waiting for pending stops to finish.
// Continue draining Events and call Wait to observe completion.
func (c *Client) Shutdown() error {
	_, err := c.call(request{Method: "shutdown"})
	return err
}

// Close disconnects this client while leaving its processes managed and running.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
	return nil
}

// Wait returns the session result or connection error after Events closes.
func (c *Client) Wait() error {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) setError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

func (c *Client) call(req request) (response, error) {
	conn, err := net.DialTimeout("unix", c.path, connectTimeout)
	if err != nil {
		return response{}, fmt.Errorf("runtime error: cannot contact session: %w", err)
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(connectTimeout))
	req.Version = protocolVersion
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, err
	}
	if req.Method == "logs" || req.Method == "shutdown" {
		_ = conn.SetReadDeadline(time.Now().Add(connectTimeout))
	}
	var reply response
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return response{}, err
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}
