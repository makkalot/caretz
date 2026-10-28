package app

import (
	"context"
	"io"

	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/output"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return RunWithOptions(ctx, args, stdout, stderr, Options{})
}

func RunWithOptions(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	root := newCaretzCLICommand(cliEnvironment{stdin: stdin, stdout: stdout, stderr: stderr, options: opts})
	result, err := root.Parse(args)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		if result != nil && result.Command != nil {
			result.Command.Usage(stderr)
		} else {
			root.Usage(stderr)
		}
		return 2
	}
	if result.Help {
		result.Command.Help(stdout)
		return 0
	}
	return runSelectedCLICommand(ctx, result, stderr)
}

func runSelectedCLICommand(ctx context.Context, result *clicmd.Result, stderr io.Writer) int {
	if result == nil || result.Command == nil || result.Command.Run == nil {
		output.Errorf(stderr, "internal error: selected command has no runner")
		return 1
	}
	return result.Command.Run(ctx, result)
}
