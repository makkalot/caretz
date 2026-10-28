package conversation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type Session interface {
	PendingUserEvents() ([]*types.Event, error)
	ProcessUserEvent(context.Context, *types.Event) (*llm.Response, error)
	OnContextEvent(eventType string, handler eventing.HandleFunc) error
	OnAuditEvent(eventType string, handler eventing.HandleFunc) error
	AppendAuditEvent(*types.Event) error
	AuditEvents() ([]*types.Event, error)
}

type Logger interface {
	Info(format string, args ...interface{})
}

type Runtime struct {
	openSession func(string) (Session, error)
	logger      Logger
	mu          sync.Mutex
	workers     map[string]*worker
}

type Task func(context.Context, Session) error

type taskRequest struct {
	ctx  context.Context
	run  Task
	done chan error
}

type Options struct {
	OpenSession func(channelID string) (Session, error)
	Logger      Logger
}

func NewRuntime(opts Options) *Runtime {
	return &Runtime{
		openSession: opts.OpenSession,
		logger:      opts.Logger,
		workers:     map[string]*worker{},
	}
}

func (r *Runtime) Session(ctx context.Context, channelID string) (Session, error) {
	worker, err := r.worker(ctx, channelID)
	if err != nil {
		return nil, err
	}
	return worker.session, nil
}

func (r *Runtime) Wake(ctx context.Context, channelID string) error {
	worker, err := r.worker(ctx, channelID)
	if err != nil {
		return err
	}
	r.logDebug("waking conversation worker: channel=%s", channelID)
	worker.Wake()
	return nil
}

// Execute runs task on the channel's conversation worker, serialized with
// inbound messages.
func (r *Runtime) Execute(ctx context.Context, channelID string, task Task) error {
	if task == nil {
		return fmt.Errorf("conversation task is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	worker, err := r.worker(ctx, channelID)
	if err != nil {
		return err
	}
	req := taskRequest{ctx: ctx, run: task, done: make(chan error, 1)}
	select {
	case worker.tasks <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-worker.ctx.Done():
		return worker.ctx.Err()
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-worker.ctx.Done():
		return worker.ctx.Err()
	}
}

func (r *Runtime) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, worker := range r.workers {
		worker.Stop()
	}
}

func (r *Runtime) worker(ctx context.Context, channelID string) (*worker, error) {
	if r == nil {
		return nil, fmt.Errorf("conversation runtime is nil")
	}
	if r.openSession == nil {
		return nil, fmt.Errorf("conversation runtime open session callback is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if worker := r.workers[channelID]; worker != nil {
		return worker, nil
	}
	session, err := r.openSession(channelID)
	if err != nil {
		return nil, err
	}
	r.logDebug("created conversation worker: channel=%s", channelID)
	worker := newWorker(ctx, channelID, session, r)
	r.workers[channelID] = worker
	return worker, nil
}

func (r *Runtime) logInfo(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Info(format, args...)
	}
}

// logDebug sends worker lifecycle detail to loggers that have a Debug level;
// a Logger with only Info drops it.
func (r *Runtime) logDebug(format string, args ...interface{}) {
	if r == nil {
		return
	}
	if debugLogger, ok := r.logger.(interface {
		Debug(format string, args ...interface{})
	}); ok {
		debugLogger.Debug(format, args...)
	}
}

type worker struct {
	ctx     context.Context
	channel string
	session Session
	runtime *Runtime
	wake    chan struct{}
	tasks   chan taskRequest
	cancel  context.CancelFunc
}

func newWorker(parent context.Context, channel string, session Session, runtime *Runtime) *worker {
	ctx, cancel := context.WithCancel(parent)
	w := &worker{ctx: ctx, channel: channel, session: session, runtime: runtime, wake: make(chan struct{}, 1), tasks: make(chan taskRequest), cancel: cancel}
	go w.run()
	return w
}

func (w *worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *worker) Stop() {
	w.cancel()
	if session, ok := w.session.(interface{ Close() error }); ok {
		_ = session.Close()
	}
}

func (w *worker) run() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
			w.drain()
		case req := <-w.tasks:
			w.execute(req)
		}
	}
}

func (w *worker) execute(req taskRequest) {
	if err := req.ctx.Err(); err != nil {
		req.done <- err
		return
	}
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("conversation task panicked: %v", recovered)
			}
		}()
		err = req.run(req.ctx, w.session)
	}()
	req.done <- err
}

func (w *worker) drain() {
	pending, err := w.session.PendingUserEvents()
	if err != nil {
		w.logInfo("failed to load pending messages: channel=%s error=%v", w.channel, err)
		return
	}
	w.runtime.logDebug("draining conversation worker: channel=%s pending=%d", w.channel, len(pending))
	for _, event := range pending {
		user, decodeErr := eventing.DecodeLogMessage(event)
		if decodeErr == nil {
			w.runtime.logDebug("processing pending message: channel=%s message_id=%s", w.channel, user.MessageID)
		}
		var processErr error
		for attempt := 0; attempt < 3; attempt++ {
			_, processErr = w.session.ProcessUserEvent(w.ctx, event)
			if processErr == nil {
				break
			}
			w.logInfo("agent processing failed; retrying: channel=%s attempt=%d error=%v", w.channel, attempt+1, processErr)
			time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond)
		}
		if processErr != nil {
			user, _ := eventing.DecodeLogMessage(event)
			failure := eventing.NewAgentLogMessageInReplyTo(w.channel, "", user.MessageID, "I couldn't process that message. Please try again.")
			_ = w.session.AppendAuditEvent(failure)
			w.logInfo("agent processing failed terminally: channel=%s message_id=%s error=%v", w.channel, user.MessageID, processErr)
		}
	}
}

func (w *worker) logInfo(format string, args ...interface{}) {
	if w.runtime != nil {
		w.runtime.logInfo(format, args...)
	}
}
