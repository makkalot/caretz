// Package tui exposes the pieces of Caretz's interactive terminal that a
// custom slash command needs to take over the screen: the terminal and its
// key events, the full-screen component runner, and the active color theme.
//
// A command reaches the terminal through ScreenRunnerFrom, because the
// interactive loop owns it while the command runs:
//
//	run := tui.ScreenRunnerFrom(ctx)
//	if run == nil {
//		return errors.New("needs an interactive terminal")
//	}
//	return run(func(t *tui.Terminal) error {
//		return tui.RunFullScreen(t, view).Err
//	})
package tui

import (
	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/slashcmd"
)

type Terminal = ansi.Terminal
type Event = ansi.Event
type EventKind = ansi.EventKind
type Size = ansi.Size
type Frame = ansi.Frame
type Result = ansi.Result
type Component = ansi.Component

// ScreenRunner lends the terminal to view for the duration of the call.
type ScreenRunner = slashcmd.ScreenRunner

// Style applies the active color theme; a disabled Style returns text as is.
type Style = output.Style

// PermissionPrompt answers a permission request on the terminal in place of
// the built-in prompt; register it with cli.Options.PermissionPrompts.
type PermissionPrompt = slashcmd.PermissionPrompt

// MessageRenderer draws user and tool messages it recognises; register it
// with cli.Options.MessageRenderers.
type MessageRenderer = output.MessageRenderer

// MarkdownRenderer renders Markdown with the active theme.
type MarkdownRenderer = output.MarkdownRenderer

const (
	EventRune      = ansi.EventRune
	EventEnter     = ansi.EventEnter
	EventBackspace = ansi.EventBackspace
	EventDelete    = ansi.EventDelete
	EventLeft      = ansi.EventLeft
	EventRight     = ansi.EventRight
	EventUp        = ansi.EventUp
	EventDown      = ansi.EventDown
	EventTab       = ansi.EventTab
	EventShiftTab  = ansi.EventShiftTab
	EventEscape    = ansi.EventEscape
	EventCtrlA     = ansi.EventCtrlA
	EventCtrlC     = ansi.EventCtrlC
	EventCtrlD     = ansi.EventCtrlD
	EventCtrlE     = ansi.EventCtrlE
	EventCtrlL     = ansi.EventCtrlL
	EventCtrlW     = ansi.EventCtrlW
	EventPaste     = ansi.EventPaste
	EventUnknown   = ansi.EventUnknown
	EventPageUp    = ansi.EventPageUp
	EventPageDown  = ansi.EventPageDown
	EventHome      = ansi.EventHome
	EventEnd       = ansi.EventEnd
	EventCtrlU     = ansi.EventCtrlU
	EventCtrlS     = ansi.EventCtrlS
)

var (
	// ScreenRunnerFrom returns the interactive terminal's runner, or nil when
	// the command is not running in one.
	ScreenRunnerFrom = slashcmd.ScreenRunnerFrom
	// WithScreenRunner attaches runner to ctx, for tests of a command.
	WithScreenRunner = slashcmd.WithScreenRunner
	NewTerminal      = ansi.NewTerminal
	RunFullScreen    = ansi.RunFullScreen
	VisibleWidth     = ansi.VisibleWidth
	CellWidth        = ansi.CellWidth
	Strip            = ansi.Strip
	NewStyle         = output.NewStyle
	NewStyleEnabled  = output.NewStyleEnabled
	// NewMarkdownRenderer renders Markdown wrapped to the width it reports.
	NewMarkdownRenderer = output.NewMarkdownRendererWithWidth
)
