package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	caretz "github.com/makkalot/caretz"
	"github.com/makkalot/caretz/lib/eventing"
)

func send(ctx context.Context, session *caretz.Session, prompt string) (string, error) {
	event := eventing.NewLogMessage("vault", prompt)
	if err := session.AppendAuditEvent(event); err != nil {
		return "", fmt.Errorf("save prompt: %w", err)
	}
	response, err := session.ProcessUserEvent(ctx, event)
	if err != nil {
		return "", err
	}
	if response == nil {
		return "", nil
	}
	return response.Content, nil
}

func execute(ctx context.Context, session *caretz.Session, o options, out io.Writer) error {
	output := newResponseOutput(out)
	if o.command != "serve" {
		text, err := send(ctx, session, commandPrompt(o.command, o.text, time.Now()))
		if err != nil {
			return err
		}
		return output.print(text, time.Time{})
	}
	sub, err := session.SubscribeContextEvents(eventing.LLMAgentResponseCreated)
	if err != nil {
		return err
	}
	defer sub.Close()
	// Consume while setup runs, so even a slow setup doesn't delay reports.
	reports, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-reports.Done():
				done <- nil
				return
			case event, ok := <-sub.Events():
				if !ok {
					done <- nil
					return
				}
				var response eventing.LLMAgentResponse
				if err := json.Unmarshal([]byte(event.Payload), &response); err != nil {
					done <- fmt.Errorf("decode response: %w", err)
					return
				}
				if err := output.print(response.Message.Content, event.OccurredOn); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	defer cancel()
	if _, err := send(ctx, session, setupPrompt(o, time.Now())); err != nil {
		cancel()
		<-done
		return fmt.Errorf("set up schedules: %w", err)
	}
	select {
	case <-ctx.Done():
		cancel()
		return <-done
	case err := <-done:
		return err
	}
}
