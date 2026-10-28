# CARETZ model benchmarks

This directory holds benchmark runner configuration.

## Daily tasks: is this model good enough for me?

SWE-bench and Terminal-Bench tell you how a model does on hard engineering
work. Most days you ask for something smaller: find a number in a receipt, tidy
a folder, merge two contact lists, fix a small bug. The `daily` suite measures
exactly that, on a difficulty ladder, so one run tells you where a model stops
being reliable:

| Tier             | What it asks                                          | Example                                                     |
| ---------------- | ----------------------------------------------------- | ----------------------------------------------------------- |
| `daily-simple`   | one input, one step                                   | "How much did I pay on this receipt?"                       |
| `daily-harder`   | several inputs, light computation, structured output  | categorize a bank statement into `summary.json`             |
| `daily-advanced` | multi-step work with rules, constraints or noisy data | reconcile invoices against messy payment references         |
| `coding-simple`  | small fixes and features, checked by hidden tests     | fix an off-by-one in pagination; write a backup script      |

Each tier has 5 tasks. Everything runs offline and is graded deterministically,
with no judge model, so two runs of the same model are directly comparable.

```sh
make build build-bench
./bin/bench run --model qwen3627b-mtp-code --suite daily --caretz-bin ./bin/caretz
```

The summary ends with a per-tier breakdown:

```text
By tier
  daily-simple     5/5 100%
  daily-harder     4/5 80%
  daily-advanced   2/5 40%
  coding-simple    4/5 80%
```

A model that clears `daily-simple` and `daily-harder` but not `daily-advanced`
is a good everyday assistant that needs supervision on fiddly multi-step jobs.
`bench compare` shows the same rows side by side for two runs, which makes it
easy to pick between two local models or quantizations.

Run one tier while iterating, or one task while debugging it:

```sh
./bin/bench run --model qwen3627b-mtp-code --suite daily --tier daily-simple --caretz-bin ./bin/caretz
./bin/bench run --model qwen3627b-mtp-code --suite daily --task trip-budget --caretz-bin ./bin/caretz -v
```

### How tasks are graded

Verifiers only look at files in the task workspace, never at the model's chat
reply. Small models often wrap answers in reasoning or extra prose, so
question-style prompts end with an exact instruction such as "Save just the
amount to `answer.txt`", and the verifier reads that file. Every verifier also
fails if the model modified the task's input files.

Coding tasks are checked by tests kept in `verifiers/daily/<id>/hidden/`. They
are copied next to the model's code in a temporary directory at grading time,
so the model never sees them.

Run outputs land under `bench/runs/` inside this repository by default, and a
curious model can walk up from its workspace to the verifiers. Pass
`--output-root` with a directory outside the repository when you need runs that
can't peek.

### Adding a daily task

A task is four pieces, all named by its id:

```text
bench/tasks/daily/<id>.md               prompt, written like a real request
bench/workspaces/daily/<id>/            input files (copied fresh for each run)
bench/verifiers/daily/<id>/verify.py    exit 0 = PASS; print the reason on failure
bench/verifiers/daily/<id>/solution/    reference solution files, plus optional solve.sh
```

Then add an entry with a `tier` to `bench/suites/daily.yaml`. Verifiers use the
small stdlib helpers in `verifiers/daily/lib/check.py`, such as `answer_text`,
`load_json`, `read_csv`, `unchanged`, `same`, `workspace_copy` and `run_ok`.

Check the verifiers without a model:

```sh
make test-bench-daily
```

For every task, this makes sure the verifier fails on the untouched workspace
and passes once the reference solution is applied. That catches verifiers that
pass by default and tasks that cannot be solved as written.

## SWE-bench and Terminal-Bench

The first runnable smoke task is `sympy__sympy-20590` from SWE-bench Lite.

Install SWE-bench tooling:

```sh
bash scripts/bench-install-swebench.sh
```

Prepare the selected SWE-bench workspace and prompt:

```sh
.bench/swebench-venv/bin/python scripts/bench-prepare-swe-task.py
```

Optional: prebuild the SWE-bench Docker image for that task.

The current SWE-bench image builder requires a separate dockerfile repository
with `src/dockerfiles/*.Dockerfile`. For the smoke run, skip this step and let
the evaluator pull the published image unless you already have that checkout.

```sh
bash scripts/bench-build-swe-image.sh
```

Run CARETZ on that one task with the local Qwen MTP config:

```sh
make build build-bench
./bin/bench run --model qwen3627b-mtp-code --suite swe-one --caretz-bin ./bin/caretz
```

Use `-v` or `--verbose` to stream CARETZ/evaluator output while the same output
is still written under the run's `logs/` directory:

```sh
./bin/bench run --model qwen3627b-mtp-code --suite swe-one --caretz-bin ./bin/caretz -v
```

If CARETZ exits after producing a patch, for example because the model returns
empty responses at the final step, you can still ask the benchmark evaluator to
score the captured patch:

```sh
./bin/bench run \
  --model qwen3627b-mtp-code \
  --suite swe-one \
  --caretz-bin "$PWD/bin/caretz" \
  --evaluate-on-caretz-error \
  -v
```

The runner captures CARETZ's patch and, for `benchmark: swebench` tasks without
`evaluator_command`, calls the official SWE-bench evaluator with a generated
prediction JSONL. Docker must be installed and running for that evaluation step.
For git-backed task workspaces, the configured `workspace` is treated as a seed
repo. Each task run clones it under the run directory before invoking CARETZ, so
dirty files left by an earlier attempt do not affect later results.

Run the first local terminal-style task with the repo-owned verifier:

```sh
make build build-bench
./bin/bench run --model qwen3627b-mtp-code --suite terminal-one --caretz-bin ./bin/caretz -v
```

`benchmark: local` tasks must set `evaluator_command`; the command runs from
the completed task workspace and exit code `0` means PASS.

Terminal-Bench Docker tasks can be run with `benchmark: terminal-bench` and a
task directory imported from the Terminal-Bench task format:

```yaml
# bench/suites/terminal-bench-one.yaml
name: terminal-bench-one
benchmark: terminal-bench
timeout_seconds: 900
tasks:
  - id: fix-code-vulnerability
    task_dir: bench/tasks/terminal-bench/fix-code-vulnerability
```

The first adapter supports single-container tasks with either
`[environment].docker_image` in `task.toml` or `environment/Dockerfile`. It uses
`instruction.md` as the prompt, runs CARETZ in `/app`, then copies `tests/` into
the container only for the verifier phase and reads `/logs/verifier/reward.txt`
when present.

Because Terminal-Bench containers are Linux containers, build a Linux CARETZ
binary before running from macOS:

```sh
GOOS=linux GOARCH=amd64 go build -o bin/caretz-linux-amd64 ./cmd/caretz
make build-bench
./bin/bench run --model qwen3627b-mtp-code --suite terminal-bench-one --caretz-bin ./bin/caretz-linux-amd64 -v
```

Run one task from a suite by passing its task id:

```sh
./bin/bench run \
  --model qwen3627b-mtp-code \
  --suite terminal-bench-one \
  --task break-filter-js-from-html \
  --caretz-bin ./bin/caretz-linux-amd64 \
  --llm-timeout 10m \
  -v
```

Some Terminal-Bench tasks need CARETZ's Bash tool to run without the internal
sandbox inside the task container. Opt those tasks in explicitly from the suite:

```yaml
tasks:
  - id: example-task
    task_dir: bench/tasks/terminal-bench/example-task
    sandbox_disable: true
```

This sets `CARETZ_DISABLE_SANDBOX=1` only for that task's CARETZ process.

Example model config:

```yaml
# bench/models/qwen-q6.yaml
name: qwen-27b-q6
endpoint: http://localhost:8081/v1
model: qwen-27b
context_size: 65536
llm_timeout: 10m
temperature: 0
metadata:
  quantization: Q6_K
```

`llm_timeout` controls the per-provider API request timeout passed to CARETZ.
It defaults to CARETZ's provider timeout when omitted. Use `--llm-timeout` on
`bench run` to override the model config for a run, or `--llm-timeout 0` to
disable the provider request timeout.

```yaml
# bench/suites/swe-smoke-v1.yaml
name: swe-smoke-v1
benchmark: swebench
timeout_seconds: 1800
tasks:
  - id: django__django-12345
    workspace: /path/to/prepared/workspace
    prompt_file: /path/to/problem.md
    evaluator_command: python -m pytest tests
```

Run:

```sh
make build build-bench
./bin/bench run --model qwen-q6 --suite swe-smoke-v1 --caretz-bin ./bin/caretz
./bin/bench compare bench/runs/run-a bench/runs/run-b
```

The next step is replacing the manual SWE workspace/evaluator fields with an
adapter that prepares instances and calls the official SWE-bench evaluator.
