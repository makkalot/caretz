package log

import (
	"fmt"
	"os"
	"sync"
	"time"
)

type Logger struct {
	verbosity int
	sender    func(prefix, content string)
	now       func() time.Time
	mu        sync.RWMutex
}

func New() *Logger {
	return &Logger{now: time.Now}
}

func (l *Logger) SetVerbosity(v int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.verbosity = v
}

func (l *Logger) Verbosity() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.verbosity
}

func (l *Logger) SetSender(fn func(prefix, content string)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sender = fn
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.log(1, "[INFO]", format, args...)
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(2, "[DEBUG]", format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(0, "[WARN]", format, args...)
}

func (l *Logger) log(minVerbosity int, prefix, format string, args ...interface{}) {
	l.mu.RLock()
	v := l.verbosity
	sender := l.sender
	now := l.now
	l.mu.RUnlock()

	if v < minVerbosity {
		return
	}
	if now == nil {
		now = time.Now
	}
	text := now().UTC().Format(time.RFC3339Nano) + " " + fmt.Sprintf(format, args...)
	if sender != nil {
		sender(prefix, text)
	} else {
		_, _ = fmt.Fprintln(os.Stderr, prefix+" "+text)
	}
}
