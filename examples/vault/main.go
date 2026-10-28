package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/makkalot/caretz/cli"
)

const usage = `Usage:
  vault capture [caretz flags] -- text...
  vault ask [caretz flags] -- question...
  vault serve [caretz flags] [--daily HH:MM] [--weekly "Sun HH:MM"] [--demo]

A single quoted text argument may also follow the flags without --.
Runtime flags include -workspace, -model, -config-root, -v, and -incognito.
ask always enables --plan. serve requires persistent sessions and the sandbox.
`

type options struct {
	command string
	flags   []string
	text    string
	daily   string
	weekly  string
	demo    bool
}

// Boolean runtime flags take their optional value with =, just like caretz.
func booleanFlag(name string) bool {
	switch name {
	case "v", "vv", "incognito", "plan", "no-model-discovery", "tasks", "no-tasks",
		"unrestricted-network", "allow-loopback", "allow-docker", "ask-sandbox-off", "session-exec":
		return true
	}
	return false
}

func parseArgs(args []string) (options, error) {
	o := options{daily: "08:00", weekly: "Sun 18:00"}
	if len(args) == 0 {
		return o, fmt.Errorf("expected capture, ask, or serve")
	}
	o.command = args[0]
	if o.command != "capture" && o.command != "ask" && o.command != "serve" {
		return o, fmt.Errorf("unknown command %q", o.command)
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			o.text = strings.Join(args[i+1:], " ")
			break
		}
		if !strings.HasPrefix(arg, "-") {
			if i != len(args)-1 {
				return o, fmt.Errorf("put multiword text after -- or quote it as the final argument")
			}
			o.text = arg
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "daily" || name == "weekly" || name == "demo" {
			if o.command != "serve" {
				return o, fmt.Errorf("--%s is only supported by serve", name)
			}
			if name == "demo" {
				if hasValue {
					return o, fmt.Errorf("use --demo without a value")
				}
				o.demo = true
				continue
			}
			if !hasValue {
				i++
				if i == len(args) {
					return o, fmt.Errorf("--%s needs a value", name)
				}
				value = args[i]
			}
			if name == "daily" {
				o.daily = value
			} else {
				o.weekly = value
			}
			continue
		}
		o.flags = append(o.flags, arg)
		if !hasValue && !booleanFlag(name) {
			i++
			if i == len(args) || strings.HasPrefix(args[i], "-") {
				return o, fmt.Errorf("%s needs a value (use = for values beginning with -)", arg)
			}
			o.flags = append(o.flags, args[i])
		}
	}
	if o.command == "serve" {
		if strings.TrimSpace(o.text) != "" {
			return o, fmt.Errorf("serve does not accept text")
		}
		if _, err := time.Parse("15:04", o.daily); err != nil {
			return o, fmt.Errorf("invalid daily time: use HH:MM")
		}
		if _, err := time.Parse("Mon 15:04", o.weekly); err != nil {
			return o, fmt.Errorf("invalid weekly time: use a weekday and HH:MM, e.g. Sun 18:00")
		}
		// Persistent schedules are the purpose of serve, even if config says incognito.
		o.flags = append(o.flags, "--incognito=false")
	} else if strings.TrimSpace(o.text) == "" {
		return o, fmt.Errorf("%s needs text", o.command)
	}
	if o.command == "ask" {
		o.flags = append(o.flags, "--plan=true")
	}
	return o, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") ||
		len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprint(stdout, usage)
		return 0
	}
	o, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n\n%s", err, usage)
		return 2
	}
	if o.command == "serve" {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("CARETZ_DISABLE_SANDBOX"))) {
		case "1", "true", "yes", "on":
			fmt.Fprintln(stderr, "serve requires the sandbox; unset CARETZ_DISABLE_SANDBOX")
			return 2
		}
	}
	rt, code := cli.BuildRuntime(ctx, o.flags, stderr, cli.Options{SystemPromptSuffix: vaultConventions})
	if rt == nil || code != 0 {
		return code
	}
	if err := checkNotes(rt.CWD(), stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Scope the durable serve channel to the canonical vault, including when
	// several vaults share an external config root.
	workspace, err := filepath.EvalSymlinks(rt.CWD())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	id := fmt.Sprintf("serve-%x", sha256.Sum256([]byte(workspace)))
	if o.command != "serve" {
		id = fmt.Sprintf("%s-%d", o.command, time.Now().UnixNano())
	}
	session, err := rt.NewAdapterSession("vault", id, cli.AdapterSessionOptions{WorkspaceDir: workspace})
	if err != nil {
		fmt.Fprintf(stderr, "open vault session: %v\n", err)
		return 1
	}
	err = execute(ctx, session, o, stdout)
	closeErr := session.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "vault: %v\n", err)
		return 1
	}
	return 0
}

func checkNotes(root string, stderr io.Writer) error {
	found := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect vault: %w", err)
	}
	if !found {
		fmt.Fprintf(stderr, "warning: no Markdown notes found in %s\n", root)
	}
	return nil
}
