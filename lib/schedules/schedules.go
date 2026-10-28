// Package schedules exposes Caretz's durable per-session scheduler as an
// installable session integration.
package schedules

import internalschedules "github.com/makkalot/caretz/internal/schedules"

const (
	Registered      = internalschedules.Registered
	Removed         = internalschedules.Removed
	RunStarted      = internalschedules.RunStarted
	RunFinished     = internalschedules.RunFinished
	RunSkipped      = internalschedules.RunSkipped
	Updated         = internalschedules.Updated
	MaxJobs         = internalschedules.MaxJobs
	MaxPrompt       = internalschedules.MaxPrompt
	MinInterval     = internalschedules.MinInterval
	MaxRunRetries   = internalschedules.MaxRunRetries
	RetryBaseDelay  = internalschedules.RetryBaseDelay
	IntegrationName = internalschedules.IntegrationName
	ModeCommand     = internalschedules.ModeCommand

	DefaultRunTimeout   = internalschedules.DefaultRunTimeout
	MaxRunTimeout       = internalschedules.MaxRunTimeout
	DefaultModelTimeout = internalschedules.DefaultModelTimeout
	MinTimeout          = internalschedules.MinTimeout
)

var (
	ErrMalformed        = internalschedules.ErrMalformed
	ErrIntervalTooShort = internalschedules.ErrIntervalTooShort
	ErrPast             = internalschedules.ErrPast
	ErrEmptyPrompt      = internalschedules.ErrEmptyPrompt
	ErrPromptTooLarge   = internalschedules.ErrPromptTooLarge
	ErrCapacity         = internalschedules.ErrCapacity
	ErrUnknownID        = internalschedules.ErrUnknownID
	ErrNested           = internalschedules.ErrNested
	ErrNeedsSandbox     = internalschedules.ErrNeedsSandbox
	New                 = internalschedules.New
	Parse               = internalschedules.Parse
	NewIntegration      = internalschedules.NewIntegration

	NewIntegrationWithDefaults = internalschedules.NewIntegrationWithDefaults
)

type Status = internalschedules.Status

const (
	Succeeded = internalschedules.Succeeded
	Failed    = internalschedules.Failed
	TimedOut  = internalschedules.TimedOut
	Panicked  = internalschedules.Panicked
	Cancelled = internalschedules.Cancelled
	Skipped   = internalschedules.Skipped
	Refused   = internalschedules.Refused
)

type Job = internalschedules.Job
type RegisterOptions = internalschedules.RegisterOptions
type Update = internalschedules.Update
type AppendFunc = internalschedules.AppendFunc
type Clock = internalschedules.Clock
type Manager = internalschedules.Manager
type Integration = internalschedules.Integration
type Defaults = internalschedules.Defaults

var WithScheduled = internalschedules.WithScheduled
var IsScheduled = internalschedules.IsScheduled
