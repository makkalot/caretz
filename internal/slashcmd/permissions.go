package slashcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/tools"
)

// AddPathCommand adds a persistent filesystem grant to the active session.
type AddPathCommand struct{}

func (AddPathCommand) Name() string { return "addpath" }
func (AddPathCommand) Description() string {
	return "Grant read-only or read-write access to a path for this session"
}
func (AddPathCommand) Usage() string     { return "/addpath [--rw] PATH" }
func (AddPathCommand) Aliases() []string { return nil }

func (AddPathCommand) Run(_ context.Context, session *agent.AgentSession, args string) (Result, error) {
	parts, err := splitArgs(args)
	if err != nil {
		return Result{}, fmt.Errorf("usage: %s: %w", (AddPathCommand{}).Usage(), err)
	}
	write := false
	if len(parts) > 0 && parts[0] == "--rw" {
		write = true
		parts = parts[1:]
	}
	if len(parts) != 1 || parts[0] == "" || strings.HasPrefix(parts[0], "-") {
		return Result{}, fmt.Errorf("usage: %s", (AddPathCommand{}).Usage())
	}
	if session == nil || session.ToolRegistry() == nil {
		return Result{}, fmt.Errorf("no active session")
	}
	capability := tools.PermissionPathRead
	mode := "read-only"
	if write {
		capability = tools.PermissionPathWrite
		mode = "read-write"
	}
	if err := session.ToolRegistry().AddPermissionGrant(capability, parts[0], true); err != nil {
		return Result{}, fmt.Errorf("cannot add path grant: %w", err)
	}
	return Result{Message: fmt.Sprintf("Added %s access to %s for this session", mode, parts[0])}, nil
}

// AddNetCommand adds a persistent network allowlist entry to the active session.
type AddNetCommand struct{}

func (AddNetCommand) Name() string { return "addnet" }
func (AddNetCommand) Description() string {
	return "Grant HTTP or SOCKS TCP network access for this session"
}
func (AddNetCommand) Usage() string     { return "/addnet [tcp://]HOST:PORT" }
func (AddNetCommand) Aliases() []string { return nil }

func (AddNetCommand) Run(_ context.Context, session *agent.AgentSession, args string) (Result, error) {
	parts, err := splitArgs(args)
	if err != nil || len(parts) != 1 {
		return Result{}, fmt.Errorf("usage: %s", (AddNetCommand{}).Usage())
	}
	if session == nil || session.ToolRegistry() == nil {
		return Result{}, fmt.Errorf("no active session")
	}
	if err := session.ToolRegistry().AddPermissionGrant(tools.PermissionNetwork, parts[0], false); err != nil {
		return Result{}, fmt.Errorf("cannot add network grant: %w", err)
	}
	return Result{Message: fmt.Sprintf("Added network access to %s for this session", parts[0])}, nil
}
