package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/makkalot/caretz/cli"
)

const usage = `Usage: git caretz-review [base] [caretz flags...]

Review base...HEAD (default: main, falling back to master).
Build: go build -o /path/on/PATH/git-caretz-review .

  --keep-session     Save the review in caretz's session history
  --workspace DIR    Review the repository containing DIR
  -h, --help         Show this help

Model, provider, logging, and other caretz flags are forwarded, for example:
  git caretz-review main --model qwen-code -v

Prompt, session, tool, and plan flags are reserved for this read-only review.
`

type arguments struct {
	base, workspace string
	rest            []string
	keep, help      bool
}

// Parse only wrapper options. Caretz validates the forwarded flags itself.
func splitArgs(args []string) (arguments, error) {
	var result arguments
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		result.base, args = args[0], args[1:]
		switch result.base {
		case "session", "acp", "setup", "auth", "models":
			return result, fmt.Errorf("caretz subcommands are not supported: %s", result.base)
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "--" || arg == "-" {
			return result, fmt.Errorf("unexpected argument %q; put the base before flags", arg)
		}
		name, value, inline := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		switch name {
		case "h", "help":
			result.help = true
		case "p", "prompt-file", "s", "adapter", "plan", "tools", "incognito", "tasks", "no-tasks":
			return result, fmt.Errorf("--%s is reserved by git-caretz-review", name)
		case "keep-session":
			result.keep = true
			if inline {
				var err error
				result.keep, err = strconv.ParseBool(value)
				if err != nil {
					return result, fmt.Errorf("invalid --keep-session: %w", err)
				}
			}
		default:
			// These are caretz's boolean flags; other flags take one value.
			boolean := false
			switch name {
			case "v", "vv", "version", "no-model-discovery", "unrestricted-network", "allow-loopback", "allow-docker", "ask-sandbox-off", "session-exec":
				boolean = true
			}
			if !inline && !boolean {
				if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
					return result, fmt.Errorf("--%s requires a value (use --%s=VALUE for a value starting with '-')", name, name)
				}
				i++
				value = args[i]
			}
			if name == "workspace" {
				result.workspace = value
			} else {
				result.rest = append(result.rest, arg)
				if !inline && !boolean {
					result.rest = append(result.rest, value)
				}
			}
		}
	}
	return result, nil
}

type cliRunner func(context.Context, []string, io.Writer, io.Writer, cli.Options) int

func run(ctx context.Context, args []string, stdout, stderr io.Writer, launch cliRunner) int {
	parsed, err := splitArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if parsed.help {
		fmt.Fprint(stdout, usage)
		return 0
	}
	review, err := prepareReview(ctx, parsed.workspace, parsed.base)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if review.stat == "" {
		fmt.Fprintln(stdout, "Nothing to review: no committed changes against "+review.base+".")
		return 0
	}
	fmt.Fprintf(stderr, "Reviewing changes against %s...\n%s\n", review.base, review.stat)
	final := append([]string{}, parsed.rest...)
	final = append(final, "-workspace", review.root, "-p", review.prompt(), "-plan", "-no-tasks",
		"-incognito="+strconv.FormatBool(!parsed.keep), "-tools", "Read,Grep,Find,Ls,GitDiff,GitLog")
	return launch(ctx, final, stdout, stderr, cli.Options{
		Tools: review.tools(), SystemPromptSuffix: reviewRubric,
	})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, cli.Run)
	stop()
	os.Exit(code)
}
