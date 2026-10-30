//go:build unix

package peers

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// maxSocketPath stays under the smallest sun_path limit (104 on macOS).
	maxSocketPath = 100
	wakeLine      = `{"wake":true}`
	pokeTimeout   = 250 * time.Millisecond
)

// SocketPath returns where endpoint listens. It depends only on the run
// directory and endpoint, never on the environment, so every process computes
// the same path. A run directory too deep for sun_path falls back to a fixed
// per-user directory under /tmp; the socket carries only wake-ups, so its
// location does not need the spool's protection.
func (r *RunDir) SocketPath(endpoint string) string {
	name := shortHash(endpoint) + ".sock"
	path := filepath.Join(r.path, "s", name)
	if len(path) <= maxSocketPath {
		return path
	}
	return filepath.Join("/tmp", fmt.Sprintf("caretz-peers-%d", os.Getuid()), shortHash(r.path+"\x00"+endpoint)+".sock")
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

// Listener receives wake-ups for one endpoint.
type Listener struct {
	listener net.Listener
	wake     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

// Listen starts accepting wake-ups for endpoint. The caller must hold the
// endpoint's lock, which makes removing a leftover socket file safe.
func (r *RunDir) Listen(endpoint string) (*Listener, error) {
	if err := validEndpoint(endpoint); err != nil {
		return nil, err
	}
	path := r.SocketPath(endpoint)
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		// Close must not unlink: a successor session may already own the path.
		unixListener.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	l := &Listener{listener: listener, wake: make(chan struct{}, 1)}
	l.wg.Add(1)
	go l.accept()
	return l, nil
}

// Wake fires whenever a peer pokes the endpoint; bursts coalesce.
func (l *Listener) Wake() <-chan struct{} { return l.wake }

// Close stops listening.
func (l *Listener) Close() error {
	var err error
	l.once.Do(func() {
		err = l.listener.Close()
		l.wg.Wait()
	})
	return err
}

func (l *Listener) accept() {
	defer l.wg.Done()
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		l.handle(conn)
	}
}

func (l *Listener) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok || !sameUser(unixConn) {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(pokeTimeout))
	reader := bufio.NewReaderSize(conn, 64)
	line, err := reader.ReadSlice('\n')
	if err != nil || strings.TrimSpace(string(line)) != wakeLine {
		return
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Poke wakes endpoint if it is listening and reports whether it was reached.
// A failed poke is harmless: the message waits in the spool.
func (r *RunDir) Poke(endpoint string) bool {
	if validEndpoint(endpoint) != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", r.SocketPath(endpoint), pokeTimeout)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetWriteDeadline(time.Now().Add(pokeTimeout))
	_, err = conn.Write([]byte(wakeLine + "\n"))
	return err == nil
}
