package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/makkalot/caretz/bench/internal/bench"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "run":
		return runBench(args[1:])
	case "compare":
		return compareRuns(args[1:])
	default:
		_, _ = fmt.Fprintf(os.Stderr, "error: unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func runBench(args []string) int {
	flags := flag.NewFlagSet("bench run", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var model, suite, taskID, tier, root, caretzBin, outputRoot string
	var repeat int
	var timeout, llmTimeout time.Duration
	var verbose, evaluateOnCaretzError bool
	flags.StringVar(&model, "model", "", "Model name or YAML path")
	flags.StringVar(&suite, "suite", "", "Suite name or YAML path")
	flags.StringVar(&taskID, "task", "", "Run only the suite task with this id")
	flags.StringVar(&tier, "tier", "", "Run only suite tasks with this tier, e.g. daily-simple")
	flags.StringVar(&root, "root", "bench", "Benchmark config root")
	flags.StringVar(&caretzBin, "caretz-bin", "", "Path to caretz binary")
	flags.StringVar(&outputRoot, "output-root", "", "Run output root")
	flags.IntVar(&repeat, "repeat", 1, "Number of repeats per task")
	flags.DurationVar(&timeout, "timeout", 0, "Per-task timeout, e.g. 30m")
	flags.DurationVar(&llmTimeout, "llm-timeout", -1, "Timeout per LLM API request, e.g. 5m, 10m. Use 0 to disable.")
	flags.BoolVar(&verbose, "verbose", false, "Stream CARETZ and evaluator output while saving logs")
	flags.BoolVar(&verbose, "v", false, "Stream CARETZ and evaluator output while saving logs")
	flags.BoolVar(&evaluateOnCaretzError, "evaluate-on-caretz-error", false, "Run patch/local evaluator if CARETZ exits non-zero after producing changes; terminal-bench evaluates container state by default")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if model == "" || suite == "" {
		_, _ = fmt.Fprintln(os.Stderr, "error: --model and --suite are required")
		return 2
	}
	llmTimeoutSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "llm-timeout" {
			llmTimeoutSet = true
		}
	})
	if llmTimeoutSet && llmTimeout < 0 {
		_, _ = fmt.Fprintln(os.Stderr, "error: --llm-timeout must be >= 0")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := bench.NewRunner()
	runDir, results, err := runner.Run(ctx, bench.RunOptions{
		Root:                  root,
		ModelName:             model,
		SuiteName:             suite,
		TaskID:                taskID,
		Tier:                  tier,
		CaretzBin:             caretzBin,
		OutputRoot:            outputRoot,
		Repeat:                repeat,
		Timeout:               timeout,
		LLMTimeout:            llmTimeout,
		LLMTimeoutSet:         llmTimeoutSet,
		Verbose:               verbose,
		EvaluateOnCaretzError: evaluateOnCaretzError,
		Stdout:                os.Stdout,
		Stderr:                os.Stderr,
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("CARETZ MODEL BENCHMARK\n\nRun\n  %s\n\n%s", runDir, bench.Summarize(results))
	return 0
}

func compareRuns(args []string) int {
	flags := flag.NewFlagSet("bench compare", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 2 {
		_, _ = fmt.Fprintln(os.Stderr, "error: usage: bench compare RUN_DIR_A RUN_DIR_B")
		return 2
	}
	aDir, bDir := flags.Arg(0), flags.Arg(1)
	aResults, err := bench.LoadResults(aDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: load %s: %v\n", aDir, err)
		return 1
	}
	bResults, err := bench.LoadResults(bDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: load %s: %v\n", bDir, err)
		return 1
	}
	fmt.Print(bench.Compare(filepath.Base(aDir), aResults, filepath.Base(bDir), bResults))
	return 0
}

func usage() {
	_, _ = fmt.Fprintln(os.Stderr, "usage:")
	_, _ = fmt.Fprintln(os.Stderr, "  bench run --model MODEL --suite SUITE")
	_, _ = fmt.Fprintln(os.Stderr, "  bench compare RUN_DIR_A RUN_DIR_B")
}
