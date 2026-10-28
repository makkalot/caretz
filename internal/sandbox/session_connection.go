package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// sessionConnection owns one supervisor generation. Old callbacks cannot send
// cancellation to a replacement supervisor, even if request IDs are reused.
type sessionConnection struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	in      io.Writer
	next    uint64
	pending map[uint64]chan sessionReply
	err     error
}

type sessionReply struct {
	resp sessionResponse
	err  error
}

func newSessionConnection(in io.Writer, out *bufio.Reader) *sessionConnection {
	c := &sessionConnection{in: in, pending: make(map[uint64]chan sessionReply)}
	go c.read(out)
	return c
}

func (c *sessionConnection) read(out *bufio.Reader) {
	for {
		var resp sessionResponse
		err := readSessionFrame(out, &resp)
		c.mu.Lock()
		if err != nil {
			c.err = err
			for id, ch := range c.pending {
				ch <- sessionReply{err: err}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		if ch := c.pending[resp.ID]; ch != nil {
			ch <- sessionReply{resp: resp}
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
	}
}

func (c *sessionConnection) prepare() (uint64, <-chan sessionReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, nil, c.err
	}
	c.next++
	ch := make(chan sessionReply, 1)
	c.pending[c.next] = ch
	return c.next, ch, nil
}

func (c *sessionConnection) abandon(id uint64) { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }
func (c *sessionConnection) write(req sessionRequest) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeSessionFrame(c.in, req)
}

func (c *sessionConnection) cancel(target uint64) (sessionResponse, error) {
	id, reply, err := c.prepare()
	if err != nil {
		return sessionResponse{}, err
	}
	defer c.abandon(id)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	written := make(chan error, 1)
	go func() { written <- c.write(sessionRequest{ID: id, CancelID: target}) }()
	select {
	case err = <-written:
		if err != nil {
			return sessionResponse{}, err
		}
	case <-ctx.Done():
		return sessionResponse{}, fmt.Errorf("cancellation acknowledgement timed out for request %d", target)
	}
	select {
	case got := <-reply:
		if got.err != nil {
			return got.resp, got.err
		}
		if got.resp.Type != "cancelled" {
			return got.resp, fmt.Errorf("invalid cancellation acknowledgement for request %d", target)
		}
		if got.resp.Error != "" {
			return got.resp, fmt.Errorf("cancel request %d: %s", target, got.resp.Error)
		}
		return got.resp, nil
	case <-ctx.Done():
		return sessionResponse{}, fmt.Errorf("cancellation acknowledgement timed out for request %d", target)
	}
}

// synchronizedOutput allows cancellation to snapshot output without waiting for
// descendants to close inherited pipes.
type synchronizedOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *synchronizedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if left := sandboxMaxOutput - len(b.data); left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *synchronizedOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
