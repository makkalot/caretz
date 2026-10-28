# vault

A personal assistant for your Markdown notes (an Obsidian vault or any folder of
`.md` files). Capture thoughts in one line, ask questions about what you've
written, and get a plan every morning and a review every Sunday, written back
into the vault as normal notes.

```text
$ vault capture "call the dentist, and idea: blog post on sandboxing"
Added to Daily/2026-10-27.md:
  - [ ] Call the dentist  #todo
  - Idea: blog post on sandboxing  #ideas → linked [[Writing ideas]]

$ vault ask "what did I decide about the kitchen renovation?"
In Home/Kitchen.md (Oct 12) you chose the oak worktop; Daily/2026-10-20.md
says the installer is booked for Nov 4.

$ vault serve
Schedules: daily 08:00 "plan my day", Sunday 18:00 "weekly review".
[08:00] Wrote Daily/2026-10-28.md → ## Plan (3 open todos, 1 meeting)
```

Build from this directory after configuring a provider with `caretz setup`:

```sh
make build
./bin/vault capture -workspace ~/Notes -- "call the dentist"
./bin/vault ask -workspace ~/Notes -- "what worktop did I pick?"
./bin/vault serve -workspace ~/Notes
```

The local Makefile also supports running and installing:

```sh
make run ARGS='serve -workspace ~/Notes'
make install
```

`make run` shows help when `ARGS` is omitted. `make install` installs to `~/bin`;
override the destination with `INSTALL_DIR=/your/bin`.

Flags come before text. Use `--` before multiple words or text starting with `-`;
a single quoted final argument also works. `ask` always enables caretz's `--plan`
mode to disable mutation tools and make sandboxed commands read-only.

Replies and scheduled summaries render Markdown in an interactive terminal:
headings, emphasis, lists, quotes, links, and code blocks. Piped or redirected
output keeps the original Markdown. Set `NO_COLOR=1` or `TERM=dumb` to keep
plain Markdown in the terminal too.

## Try the sample

Use a copy so the checked-in fixture stays unchanged:

```sh
cp -R testdata/vault /tmp/vault-demo
./bin/vault ask -workspace /tmp/vault-demo -- "what worktop did I pick?"
./bin/vault capture -workspace /tmp/vault-demo -- "buy milk"
./bin/vault serve -workspace /tmp/vault-demo --demo
```

The answer should cite `Home/Kitchen.md` and the oak worktop. Demo summaries
should appear after about two minutes, depending on model latency. Sample notes
have fixed dates in October 2026; newer plans may have no meetings.

## Schedules

`serve` defaults to a daily plan at 08:00 and Sunday review at 18:00, in your
local timezone. Override them with `--daily 09:00 --weekly "Fri 17:30"`.
`--demo` replaces both with one-minute loops and reviews on any weekday.
Restart without `--demo` to restore normal times. Press Ctrl+C to stop.

Plans append a `## Plan` section to today's daily note; reviews go in
`Reviews/YYYY-Www.md` using the ISO week year and week number, with Done,
Still open, and Themes sections. Prompts preserve existing text and avoid
duplicate sections. Scheduled agents determine the date when they run.

There is no weekly cadence: the review triggers daily and asks the scheduled
agent to skip other weekdays. Skipped days can still cost model calls.
Setup and reconciliation use the model; inspect the reported IDs, cadences, and
next run times to confirm setup. Changing cadence requires removing the old
schedule and registering a replacement; the setup prompt explains this.

## Approach

This example shows **`cli.BuildRuntime` with an adapter session**: your program
owns its commands and output, and caretz provides config, model selection,
credentials, sandbox, session storage, and schedules.

- `cli.BuildRuntime` reads caretz's normal flags and config (`-workspace`,
  `-model`, `-config-root`, `-v`), so `vault` uses whatever provider you set
  up with `caretz setup`.
- `rt.NewAdapterSession("vault", <channel>, ...)` opens a session with the
  vault as its workspace. `capture` and `ask` use short one-off channels;
  `serve` keeps a stable channel derived from the canonical vault path so its
  schedules persist, including with a shared external config root. It forces
  persistent sessions even when incognito is configured.
- **Schedules come built in.** Adapter sessions already have caretz's
  scheduler tools, so `serve` just asks the agent "every day at 08:00, plan my
  day…" and caretz registers, persists, and runs it while `serve` is up, and resumes
  it on the next start.
- `serve` subscribes to the session's context events to print each scheduled
  run's summary as it finishes.

## Safety

The vault is the workspace. Sandboxed commands can write there and in their
temporary storage; additional roots can be granted through caretz configuration.
The sample `.caretz/config.json` limits base tools to local file and shell tools,
leaves sandbox network access off, and disables sandbox escape prompts for the
`general` profile. Other profiles and CLI overrides can grant more access.
`serve` refuses to run with `CARETZ_DISABLE_SANDBOX` enabled.

The prompt tells agents to leave `.obsidian/` and `.caretz/` alone. The runtime's
public CLI/config API cannot configure arbitrary hidden paths, so `.obsidian/`
avoidance is a prompt convention, not an enforced path deny.
Notes stay on your machine, apart from what is sent to the model provider you
configured; use a local model if that matters to you.

Avoid running capture and serve concurrently: their separate sessions do not
coordinate file edits. Run only one serve process per vault.

## Checks

```sh
go build ./...
go vet ./...
go test ./...
```

Tests cover argument parsing, mode overrides, prompt dates and schedule setup,
help, and sandbox-disabled rejection without calling a model.
