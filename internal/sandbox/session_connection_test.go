package sandbox

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSessionConnectionCancellationDemultiplexesAndDiscardsLateAck(t *testing.T) {
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	defer func() { _ = requestReader.Close() }()
	defer func() { _ = requestWriter.Close() }()
	defer func() { _ = responseReader.Close() }()
	defer func() { _ = responseWriter.Close() }()
	c := newSessionConnection(requestWriter, bufio.NewReader(responseReader))
	id, result, err := c.prepare()
	if err != nil {
		t.Fatal(err)
	}
	firstCancel := make(chan sessionRequest, 1)
	go func() {
		var req sessionRequest
		if readSessionFrame(requestReader, &req) == nil {
			firstCancel <- req
		}
	}()
	done := make(chan error, 1)
	go func() { _, err := c.cancel(id); done <- err }()
	req := <-firstCancel
	if err := <-done; err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v", err)
	}
	// The late cancellation ack must not be consumed as the original result.
	if err := writeSessionFrame(responseWriter, sessionResponse{ID: req.ID, Type: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionFrame(responseWriter, sessionResponse{ID: id, Type: "result", Stdout: "original"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.resp.Stdout != "original" {
			t.Fatalf("reply = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("original request was blocked")
	}
	// The same connection still accepts cancellation alongside other requests.
	go func() {
		var req sessionRequest
		if readSessionFrame(requestReader, &req) == nil {
			_ = writeSessionFrame(responseWriter, sessionResponse{ID: req.ID, Type: "cancelled", PID: 123})
		}
	}()
	resp, err := c.cancel(77)
	if err != nil || resp.PID != 123 {
		t.Fatalf("ack = %+v, error = %v", resp, err)
	}
}
