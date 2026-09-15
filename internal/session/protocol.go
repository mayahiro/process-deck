// Package session keeps a supervisor alive independently of its terminal client.
// Its Unix socket protocol is private to the procdeck executable.
package session

import (
	"errors"
	"time"

	"github.com/mayahiro/process-deck/internal/supervisor"
)

const (
	protocolVersion = 1
	connectTimeout  = 5 * time.Second
	eventBufferSize = 1024
	workerArgument  = "--internal-session"
)

type request struct {
	Version int
	Method  string
	Process string
	Cursor  supervisor.LogCursor
}

type response struct {
	Version    int
	Error      string
	Reattached bool
	Snapshots  []supervisor.Snapshot
	History    map[string]supervisor.LogBatch
	Logs       supervisor.LogBatch
	Event      *wireEvent
	Finished   bool
}

// An error interface cannot be decoded from JSON, so transport its message.
type wireEvent struct {
	supervisor.Event
	Error string
}

func encodeEvent(event supervisor.Event) *wireEvent {
	message := errorText(event.Error)
	event.Error = nil
	return &wireEvent{Event: event, Error: message}
}

func (event *wireEvent) decode() supervisor.Event {
	decoded := event.Event
	if event.Error != "" {
		decoded.Error = errors.New(event.Error)
	}
	return decoded
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
