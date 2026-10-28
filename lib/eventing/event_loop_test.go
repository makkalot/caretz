package eventing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/types"
)

func TestSyncEventLoop_SingleHandler(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())

	var received *types.Event
	handler := func(e *types.Event) error {
		received = e
		return nil
	}

	if err := loop.AddHandler("Test.Event", handler); err != nil {
		t.Fatalf("unexpected error adding handler: %v", err)
	}

	event := types.NewEvent(types.NewOriginator("test-id", 1), "Test.Event", `{"data":"hello"}`)
	if err := loop.RunEvent(event); err != nil {
		t.Fatalf("unexpected error running event: %v", err)
	}

	if received == nil {
		t.Fatal("expected handler to receive event")
	}
	if received.EventType != "Test.Event" {
		t.Errorf("expected event type Test.Event, got %s", received.EventType)
	}
}

func TestSyncEventLoop_SubscriptionMergesTypesInOrder(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())
	subscription, err := loop.Subscribe("Type.A", "Type.B")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	for _, eventType := range []string{"Type.A", "Type.C", "Type.B", "Type.A"} {
		if err := loop.RunEvent(types.NewEvent(types.NewOriginator("test", 0), eventType, `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"Type.A", "Type.B", "Type.A"} {
		select {
		case event := <-subscription.Events():
			if event.EventType != want {
				t.Fatalf("event type = %s, want %s", event.EventType, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestSyncEventLoop_SubscriptionsDoNotCompete(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())
	first, err := loop.Subscribe("Type.A")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := loop.Subscribe("Type.A")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	event := types.NewEvent(types.NewOriginator("test", 0), "Type.A", `{}`)
	if err := loop.RunEvent(event); err != nil {
		t.Fatal(err)
	}
	for index, subscription := range []Subscription{first, second} {
		select {
		case got := <-subscription.Events():
			if got != event {
				t.Fatalf("subscription %d received wrong event", index)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscription %d did not receive event", index)
		}
	}
}

func TestSyncEventLoop_HandlerErrorStillReachesSubscription(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())
	subscription, err := loop.Subscribe("Type.A")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	wantErr := errors.New("handler failed")
	if err := loop.AddHandler("Type.A", func(*types.Event) error { return wantErr }); err != nil {
		t.Fatal(err)
	}
	event := types.NewEvent(types.NewOriginator("test", 0), "Type.A", `{}`)
	if err := loop.RunEvent(event); !errors.Is(err, wantErr) {
		t.Fatalf("RunEvent() error = %v", err)
	}
	select {
	case got := <-subscription.Events():
		if got != event {
			t.Fatal("subscription received wrong event")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not receive committed event")
	}
}

func TestSyncEventLoop_MultipleHandlersSameType(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())

	var order []int
	handler1 := func(e *types.Event) error {
		order = append(order, 1)
		return nil
	}
	handler2 := func(e *types.Event) error {
		order = append(order, 2)
		return nil
	}
	handler3 := func(e *types.Event) error {
		order = append(order, 3)
		return nil
	}

	if err := loop.AddHandler("Test.Event", handler1); err != nil {
		t.Fatal(err)
	}
	if err := loop.AddHandler("Test.Event", handler2); err != nil {
		t.Fatal(err)
	}
	if err := loop.AddHandler("Test.Event", handler3); err != nil {
		t.Fatal(err)
	}

	event := types.NewEvent(types.NewOriginator("test-id", 1), "Test.Event", `{}`)
	if err := loop.RunEvent(event); err != nil {
		t.Fatalf("unexpected error running event: %v", err)
	}

	if len(order) != 3 {
		t.Fatalf("expected 3 handlers to run, got %d", len(order))
	}
	if order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Errorf("expected handlers to run in order [1,2,3], got %v", order)
	}
}

func TestSyncEventLoop_DifferentEventTypes(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())

	var aCalled, bCalled bool
	if err := loop.AddHandler("Type.A", func(e *types.Event) error {
		aCalled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := loop.AddHandler("Type.B", func(e *types.Event) error {
		bCalled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	eventA := types.NewEvent(types.NewOriginator("test-id", 1), "Type.A", `{}`)
	if err := loop.RunEvent(eventA); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !aCalled {
		t.Error("expected handler for Type.A to be called")
	}
	if bCalled {
		t.Error("expected handler for Type.B to NOT be called")
	}
}

func TestSyncEventLoop_HandlerErrorStopsExecution(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())

	var order []int
	expectedErr := errors.New("handler 2 failed")

	if err := loop.AddHandler("Test.Event", func(e *types.Event) error {
		order = append(order, 1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := loop.AddHandler("Test.Event", func(e *types.Event) error {
		order = append(order, 2)
		return expectedErr
	}); err != nil {
		t.Fatal(err)
	}
	if err := loop.AddHandler("Test.Event", func(e *types.Event) error {
		order = append(order, 3)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	event := types.NewEvent(types.NewOriginator("test-id", 1), "Test.Event", `{}`)
	err := loop.RunEvent(event)
	if err != expectedErr {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}

	if len(order) != 2 {
		t.Fatalf("expected 2 handlers to run before error, got %d", len(order))
	}
	if order[0] != 1 || order[1] != 2 {
		t.Errorf("expected [1,2], got %v", order)
	}
}

func TestSyncEventLoop_NoHandlers(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())

	event := types.NewEvent(types.NewOriginator("test-id", 1), "Unknown.Event", `{}`)
	if err := loop.RunEvent(event); err != nil {
		t.Fatalf("expected no error with no handlers, got %v", err)
	}
}

func TestSyncEventLoop_ConcurrentAddHandler(t *testing.T) {
	loop := NewSyncEventLoop(context.Background())

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := loop.AddHandler("Test.Event", func(e *types.Event) error {
				return nil
			})
			if err != nil {
				t.Errorf("AddHandler: %v", err)
			}
		}()
	}
	wg.Wait()

	loop.mu.RLock()
	count := len(loop.handlers["Test.Event"])
	loop.mu.RUnlock()

	if count != 100 {
		t.Errorf("expected 100 handlers, got %d", count)
	}
}
