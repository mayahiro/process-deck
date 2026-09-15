package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/mayahiro/process-deck/internal/config"
	"github.com/mayahiro/process-deck/internal/supervisor"
)

type attachment struct {
	conn      net.Conn
	queue     chan response
	done      chan struct{}
	closeOnce sync.Once
}

func (a *attachment) close() {
	a.closeOnce.Do(func() {
		close(a.done)
		_ = a.conn.Close()
	})
}

type server struct {
	sup      *supervisor.Supervisor
	listener net.Listener
	cancel   context.CancelFunc
	done     chan struct{}
	start    chan struct{}
	commands sync.Mutex
	wg       sync.WaitGroup

	mu      sync.Mutex
	active  *attachment
	started bool
	closing bool
}

func serve(ctx context.Context, listener net.Listener, cfg *config.Config, options supervisor.Options) error {
	sup, err := supervisor.New(cfg, options)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{
		sup: sup, listener: listener, cancel: cancel,
		done: make(chan struct{}), start: make(chan struct{}),
	}
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		s.accept()
	}()
	defer func() {
		close(s.done)
		_ = listener.Close()
		<-accepted
		s.wg.Wait()
	}()

	// Do not start commands until the first client can receive their output.
	// A launcher that dies before attaching must not leave an idle worker.
	timer := time.NewTimer(connectTimeout * 2)
	defer timer.Stop()
	select {
	case <-s.start:
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("runtime error: session was not attached before startup timed out")
	}
	runErr := make(chan error, 1)
	go func() { runErr <- sup.Run(ctx) }()
	for event := range sup.Events() {
		// The final response carries the result after Run has fully completed.
		if event.Kind == supervisor.EventSupervisorStopped {
			continue
		}
		message := response{Event: encodeEvent(event)}
		if event.Kind != supervisor.EventProcessLogLine {
			message.Snapshots = sup.Snapshot()
		}
		s.publish(message)
	}
	err = <-runErr
	s.mu.Lock()
	s.closing = true
	s.enqueueLocked(response{Finished: true, Error: errorText(err), Snapshots: sup.Snapshot()})
	s.mu.Unlock()
	return err
}

func (s *server) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.cancel()
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			s.handle(conn)
		}()
	}
}

func (s *server) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(connectTimeout))
	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)
	var req request
	if err := decoder.Decode(&req); err != nil {
		return
	}
	if req.Version != protocolVersion {
		_ = encoder.Encode(response{Error: "runtime error: incompatible session protocol; use the procdeck version that started this session"})
		return
	}
	if req.Method == "attach" {
		s.attach(conn, decoder, encoder)
		return
	}

	var reply response
	switch req.Method {
	case "logs":
		reply.Logs = s.sup.LogsSince(req.Process, req.Cursor)
	case "shutdown":
		// Acknowledge cancellation even while a manual stop is waiting for its
		// timeout. Completion is delivered separately on the attachment stream.
		s.cancel()
	case "start", "stop", "restart":
		// Serialize manual commands; the event stream stays independent so a
		// stop can continue to drain process output while waiting for exit.
		s.commands.Lock()
		_ = conn.SetDeadline(time.Time{})
		var err error
		switch req.Method {
		case "start":
			err = s.sup.StartProcess(req.Process)
		case "stop":
			err = s.sup.StopProcess(req.Process)
		case "restart":
			err = s.sup.RestartProcess(req.Process)
		}
		reply.Error = errorText(err)
		s.commands.Unlock()
	default:
		reply.Error = "runtime error: unknown session request"
	}
	_ = conn.SetWriteDeadline(time.Now().Add(connectTimeout))
	_ = encoder.Encode(reply)
}

func (s *server) attach(conn net.Conn, decoder *json.Decoder, encoder *json.Encoder) {
	client := &attachment{conn: conn, queue: make(chan response, eventBufferSize), done: make(chan struct{})}
	s.mu.Lock()
	if s.closing || s.active != nil {
		message := "runtime error: session is already attached to another procdeck client"
		if s.closing {
			message = "runtime error: session is stopping; retry after it has stopped"
		}
		s.mu.Unlock()
		_ = encoder.Encode(response{Error: message})
		return
	}
	s.active = client
	initial := response{
		Version: protocolVersion, Reattached: s.started,
		Snapshots: s.sup.Snapshot(), History: make(map[string]supervisor.LogBatch),
	}
	for _, snapshot := range initial.Snapshots {
		initial.History[snapshot.Name] = s.sup.LogsSince(snapshot.Name, 0)
	}
	s.mu.Unlock()
	defer func() {
		client.close()
		s.mu.Lock()
		if s.active == client {
			s.active = nil
		}
		s.mu.Unlock()
	}()
	if err := encoder.Encode(initial); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	s.mu.Lock()
	if !s.started {
		s.started = true
		close(s.start)
	}
	s.mu.Unlock()

	go func() {
		// The attachment is receive-only. EOF detects a dead client even
		// when all managed processes are quiet.
		var unused request
		_ = decoder.Decode(&unused)
		client.close()
	}()
	for {
		select {
		case message := <-client.queue:
			_ = conn.SetWriteDeadline(time.Now().Add(connectTimeout))
			if err := encoder.Encode(message); err != nil || message.Finished {
				return
			}
		case <-client.done:
			return
		case <-s.done:
			// Run finished: deliver everything already queued, including its
			// result, before closing a responsive client's stream.
			for {
				select {
				case message := <-client.queue:
					_ = conn.SetWriteDeadline(time.Now().Add(connectTimeout))
					if err := encoder.Encode(message); err != nil || message.Finished {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (s *server) publish(message response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueLocked(message)
}

func (s *server) enqueueLocked(message response) {
	if s.active == nil {
		return
	}
	select {
	case s.active.queue <- message:
	case <-s.active.done:
		// Preserve bursts for a responsive client with bounded backpressure.
		// EOF or the writer's deadline releases a stalled/dead attachment,
		// allowing detached supervision and log collection to continue.
		s.active = nil
	}
}
