// Package acp provides an in-process host for Caretz's Agent Client Protocol.
package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

var (
	ErrHostClosed        = errors.New("acp host is closed")
	ErrEndpointClosed    = errors.New("acp endpoint is closed")
	ErrWorkspaceExists   = errors.New("acp workspace already exists")
	ErrWorkspaceNotFound = errors.New("acp workspace not found")
	ErrInvalidMessage    = errors.New("invalid ACP JSON message")
)

// ServerOptions controls one workspace's ACP server.
type ServerOptions struct {
	AllowedModels []string
}

// Server serves newline-delimited ACP messages over caller-owned streams.
// Most embedders should use Host rather than invoking Server directly.
type Server interface {
	ServeACP(context.Context, io.Reader, io.Writer) error
}

// Runtime can construct an ACP server using an already configured Caretz
// runtime. *cli.Runtime implements Runtime.
type Runtime interface {
	NewACPServer(ServerOptions) (Server, error)
}

// RuntimeFactory lazily constructs a workspace-scoped Caretz runtime. The
// supplied context remains active for the lifetime of the resulting endpoint.
type RuntimeFactory func(context.Context) (Runtime, error)

// WorkspaceConfig describes one independently configured ACP workspace.
type WorkspaceConfig struct {
	Runtime RuntimeFactory
	Server  ServerOptions
}

// Host owns a dynamic collection of workspace-scoped ACP endpoints.
type Host struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	closed     bool
	done       chan struct{}
	closeErr   error
	workspaces map[string]*workspaceSlot
}

type workspaceSlot struct {
	config   WorkspaceConfig
	endpoint *Endpoint
	start    *startAttempt
	changing bool
	changed  chan struct{}
}

type startAttempt struct {
	done     chan struct{}
	cancel   context.CancelFunc
	endpoint *Endpoint
	err      error
}

// Endpoint is one live ACP stream. It multiplexes all ACP sessions belonging
// to its workspace runtime.
type Endpoint struct {
	ctx    context.Context
	cancel context.CancelFunc
	server Server

	requests chan json.RawMessage
	messages chan json.RawMessage
	done     chan struct{}

	closeOnce sync.Once
	errMu     sync.RWMutex
	err       error
}

// NewHost creates an empty host. Cancelling ctx closes every active endpoint.
func NewHost(ctx context.Context) *Host {
	if ctx == nil {
		ctx = context.Background()
	}
	hostCtx, cancel := context.WithCancel(ctx)
	h := &Host{ctx: hostCtx, cancel: cancel, done: make(chan struct{}), workspaces: make(map[string]*workspaceSlot)}
	go func() {
		<-hostCtx.Done()
		_ = h.Close()
	}()
	return h
}

// RegisterWorkspace adds a lazy workspace registration.
func (h *Host) RegisterWorkspace(id string, config WorkspaceConfig) error {
	if err := validateWorkspace(id, config); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrHostClosed
	}
	if _, ok := h.workspaces[id]; ok {
		return fmt.Errorf("%w: %s", ErrWorkspaceExists, id)
	}
	h.workspaces[id] = &workspaceSlot{config: cloneWorkspaceConfig(config)}
	return nil
}

// ReplaceWorkspace stops the current endpoint, if any, and installs a new
// lazy registration. Concurrent Endpoint calls wait for replacement to finish.
func (h *Host) ReplaceWorkspace(id string, config WorkspaceConfig) error {
	if err := validateWorkspace(id, config); err != nil {
		return err
	}
	slot, err := h.beginChange(id)
	if err != nil {
		return err
	}
	stopErr := h.stopSlot(slot)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		h.finishChangeLocked(slot)
		return ErrHostClosed
	}
	current := h.workspaces[id]
	if current != slot {
		h.finishChangeLocked(slot)
		return fmt.Errorf("%w: %s", ErrWorkspaceNotFound, id)
	}
	slot.config = cloneWorkspaceConfig(config)
	slot.endpoint = nil
	slot.start = nil
	h.finishChangeLocked(slot)
	return stopErr
}

// RemoveWorkspace stops and removes a workspace registration.
func (h *Host) RemoveWorkspace(id string) error {
	slot, err := h.beginChange(id)
	if err != nil {
		return err
	}
	stopErr := h.stopSlot(slot)

	h.mu.Lock()
	if h.workspaces[id] == slot {
		delete(h.workspaces, id)
	}
	h.finishChangeLocked(slot)
	h.mu.Unlock()
	return stopErr
}

// Endpoint returns the current live endpoint, lazily starting it if needed.
// A failed start is not cached; a later call retries the runtime factory.
func (h *Host) Endpoint(ctx context.Context, id string) (*Endpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return nil, ErrHostClosed
		}
		slot := h.workspaces[id]
		if slot == nil {
			h.mu.Unlock()
			return nil, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, id)
		}
		if slot.changing {
			changed := slot.changed
			h.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-h.ctx.Done():
				return nil, ErrHostClosed
			case <-changed:
				continue
			}
		}
		if endpoint := slot.endpoint; endpoint != nil {
			select {
			case <-endpoint.Done():
				slot.endpoint = nil
			default:
				h.mu.Unlock()
				return endpoint, nil
			}
		}
		attempt := slot.start
		if attempt == nil {
			lifetime, cancel := context.WithCancel(h.ctx)
			attempt = &startAttempt{done: make(chan struct{}), cancel: cancel}
			slot.start = attempt
			config := cloneWorkspaceConfig(slot.config)
			go h.startWorkspace(slot, attempt, lifetime, config)
		}
		h.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-h.ctx.Done():
			return nil, ErrHostClosed
		case <-attempt.done:
			if attempt.err != nil {
				return nil, attempt.err
			}
			return attempt.endpoint, nil
		}
	}
}

// Close stops all endpoints and prevents further host operations.
func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		done := h.done
		h.mu.Unlock()
		<-done
		h.mu.Lock()
		err := h.closeErr
		h.mu.Unlock()
		return err
	}
	h.closed = true
	h.cancel()
	slots := make([]*workspaceSlot, 0, len(h.workspaces))
	for _, slot := range h.workspaces {
		slots = append(slots, slot)
	}
	h.mu.Unlock()

	var errs []error
	for _, slot := range slots {
		if err := h.stopSlot(slot); err != nil {
			errs = append(errs, err)
		}
	}
	h.mu.Lock()
	h.closeErr = errors.Join(errs...)
	close(h.done)
	err := h.closeErr
	h.mu.Unlock()
	return err
}

func (h *Host) beginChange(id string) (*workspaceSlot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrHostClosed
	}
	slot := h.workspaces[id]
	if slot == nil {
		return nil, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, id)
	}
	if slot.changing {
		return nil, fmt.Errorf("workspace %q is already changing", id)
	}
	slot.changing = true
	slot.changed = make(chan struct{})
	return slot, nil
}

func (h *Host) finishChangeLocked(slot *workspaceSlot) {
	if slot.changing {
		slot.changing = false
		close(slot.changed)
		slot.changed = nil
	}
}

func (h *Host) stopSlot(slot *workspaceSlot) error {
	if slot == nil {
		return nil
	}
	h.mu.Lock()
	attempt := slot.start
	endpoint := slot.endpoint
	h.mu.Unlock()
	if attempt != nil {
		attempt.cancel()
		<-attempt.done
	}
	if endpoint != nil {
		return endpoint.Close()
	}
	return nil
}

func (h *Host) startWorkspace(slot *workspaceSlot, attempt *startAttempt, ctx context.Context, config WorkspaceConfig) {
	defer close(attempt.done)
	runtime, err := config.Runtime(ctx)
	if err == nil && runtime == nil {
		err = errors.New("ACP runtime factory returned nil")
	}
	var endpoint *Endpoint
	if err == nil {
		var server Server
		server, err = runtime.NewACPServer(config.Server)
		if err == nil {
			endpoint = newEndpoint(ctx, attempt.cancel, server)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || slot.start != attempt || slot.changing {
		attempt.cancel()
		if endpoint != nil {
			_ = endpoint.Close()
		}
		if err == nil {
			err = ErrEndpointClosed
		}
	} else if err == nil {
		slot.endpoint = endpoint
	}
	if slot.start == attempt {
		slot.start = nil
	}
	attempt.endpoint = endpoint
	attempt.err = err
}

func newEndpoint(ctx context.Context, cancel context.CancelFunc, server Server) *Endpoint {
	e := &Endpoint{
		ctx:      ctx,
		cancel:   cancel,
		server:   server,
		requests: make(chan json.RawMessage),
		messages: make(chan json.RawMessage, 256),
		done:     make(chan struct{}),
	}
	go e.run()
	return e
}

// Send submits one complete ACP JSON value.
func (e *Endpoint) Send(ctx context.Context, request json.RawMessage) error {
	if e == nil {
		return ErrEndpointClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !json.Valid(request) || bytes.ContainsAny(request, "\r\n") {
		return ErrInvalidMessage
	}
	request = append(json.RawMessage(nil), request...)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		return ErrEndpointClosed
	case e.requests <- request:
		return nil
	}
}

// Messages returns the ordered ACP output stream. It is intended to have one
// consumer.
func (e *Endpoint) Messages() <-chan json.RawMessage {
	if e == nil {
		closed := make(chan json.RawMessage)
		close(closed)
		return closed
	}
	return e.messages
}

// Done closes when the endpoint and its output stream have stopped.
func (e *Endpoint) Done() <-chan struct{} {
	if e == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return e.done
}

// Err reports an unexpected terminal server or transport error.
func (e *Endpoint) Err() error {
	if e == nil {
		return nil
	}
	e.errMu.RLock()
	defer e.errMu.RUnlock()
	return e.err
}

// Close stops the current endpoint. Its host registration remains available
// and can be started again with Host.Endpoint.
func (e *Endpoint) Close() error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(e.cancel)
	<-e.done
	return e.Err()
}

func (e *Endpoint) run() {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	requestDone := make(chan error, 1)
	outputDone := make(chan error, 1)

	go e.writeRequests(inputWriter, requestDone)
	go e.readMessages(outputReader, outputDone)

	serveErr := e.server.ServeACP(e.ctx, inputReader, outputWriter)
	e.cancel()
	_ = inputReader.Close()
	_ = outputWriter.Close()
	requestErr := <-requestDone
	outputErr := <-outputDone
	_ = outputReader.Close()

	err := errors.Join(normalizeEndpointError(serveErr), normalizeEndpointError(requestErr), normalizeEndpointError(outputErr))
	e.errMu.Lock()
	e.err = err
	e.errMu.Unlock()
	close(e.done)
}

func (e *Endpoint) writeRequests(writer *io.PipeWriter, done chan<- error) {
	defer func() { _ = writer.Close() }()
	for {
		select {
		case <-e.ctx.Done():
			done <- nil
			return
		case request := <-e.requests:
			_, err := writer.Write(append(request, '\n'))
			if err != nil {
				done <- err
				return
			}
		}
	}
}

func (e *Endpoint) readMessages(reader *io.PipeReader, done chan<- error) {
	defer func() { _ = reader.Close() }()
	defer close(e.messages)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 32<<20)
	for scanner.Scan() {
		message := append(json.RawMessage(nil), scanner.Bytes()...)
		select {
		case <-e.ctx.Done():
			done <- nil
			return
		case e.messages <- message:
		}
	}
	done <- scanner.Err()
}

func normalizeEndpointError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}

func validateWorkspace(id string, config WorkspaceConfig) error {
	if id == "" {
		return errors.New("ACP workspace id is required")
	}
	if config.Runtime == nil {
		return fmt.Errorf("ACP workspace %q runtime factory is required", id)
	}
	return nil
}

func cloneWorkspaceConfig(config WorkspaceConfig) WorkspaceConfig {
	config.Server.AllowedModels = append([]string(nil), config.Server.AllowedModels...)
	return config
}
