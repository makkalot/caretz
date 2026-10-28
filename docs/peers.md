---
title: "Peer bots"
description: "Long-lived agents your session can hand work to and keep going."
---

# Peer Bots

A peer bot is an agent that keeps running next to you. It has its own model,
prompt, permissions, and conversation history. Your main session can hand it
work and keep going, and the bot's answer comes back later on its own. You can
also switch to the bot's terminal and talk to it directly, like any other caretz
session.

Bots are useful for roles that work best with memory across many requests:

- a reviewer that remembers the design decisions it already approved;
- a researcher that keeps notes while your main session keeps coding;
- a planner that other bots check in with.

Unlike a [subagent](customization/subagents.md), which is a fresh child session
that lives for one task and blocks its parent, a bot is long-lived and
asynchronous.

## Define a bot

A bot is a normal agent file with `mode: peer`:

```markdown
---
description: Reviews diffs for correctness and missing tests.
mode: peer
model: local/qwen
reasoning: high
permission:
  edit: deny
---

You are the project's reviewer. Keep a running list of risks you have already
raised, and do not repeat them. Answer with findings in severity order.
```

Put it in `.caretz/agents/reviewer.md`. The filename is the bot's name. Bots
accept the same `model`, `reasoning`, and `permission` fields as subagents, plus
four bot-only fields:

```yaml
workspace: out/reviewer
accept_from: root, planner
profile: review
run_timeout: 2h
```

- `workspace` limits where the bot can write. The bot runs in that directory and
  can write only there, while the main workspace stays readable. Relative paths
  are resolved from the main workspace, and the directory is created on first
  start.
- `profile` picks the [runtime profile](profiles/_index.md) the bot runs with,
  so a reviewer can run read-only with its own tools and skills while your main
  session uses another profile.
- `run_timeout` caps how long a turn started by a message may run before it is
  cancelled and the sender is told so. It defaults to 30 minutes; raise it for a
  bot that implements and tests changes.
- `accept_from` limits who may message the bot. `root` means your interactive
  sessions; any other entry is a bot name. Without it, every session and bot in
  the workspace may send messages.

Bots stay out of `@name` completion and `CallSubagent`. A profile's `agents`
allowlist applies to them as it does to subagents.

## Run it

Start the bot in its own terminal:

```sh
caretz peer reviewer
```

This opens the normal interactive UI on the bot's saved session, so you can type
to it directly and see each incoming message as a turn. Restarting it resumes
the same history. `caretz -s reviewer` opens the bot the same way; add
`--adapter` to open an ordinary session that happens to share its name. In
the bot's own pane, `/peers` lists it as `this session`.

A bot is a normal caretz session that keeps running, so it takes every `caretz`
flag except the ones that pick a session (`-p`, `--prompt-file`, `-s`,
`--adapter`, `--incognito`):

```sh
caretz peer reviewer --unrestricted-network --read-root ../shared -v
```

A workspace can run only one instance of each bot; a second
`caretz peer reviewer` exits and shows the pid of the one already running.

Inside [Herdr](herdr.md) or tmux, let your main session bring the whole team up
at once:

```sh
caretz --start-peers
```

It opens a `bots` tab (a tmux window) with one pane per bot that isn't already
running. A plain `caretz` never starts bots, so you can still open an ordinary
session in a workspace that defines them. Outside Herdr and tmux,
`--start-peers` exits and prints the `caretz peer` command for each offline bot
so you can start them in separate terminals.

To give a coordinator its own tab and stack two implementers as rows in
another, describe the tabs in `.caretz/config.json`:

```json
{
  "profiles": {
    "general": {
      "peers": {
        "tabs": [
          { "name": "coordinator", "bots": ["coordinator"] },
          { "name": "implementers", "bots": ["impl-a", "impl-b"], "layout": "rows" }
        ]
      }
    }
  }
}
```

`layout` is `rows` (stacked top to bottom) or `columns` (side by side). If you
leave it out, Herdr stacks the panes as rows and tmux tiles them. Bots that no
tab lists still open in a `bots` tab; to choose where that tab goes and how its
panes are arranged, list a tab named `bots` with no bots. A tab whose bots are
all running already isn't opened. A bot can be listed in only one tab. The
layout is read from the root session's [profile](profiles/_index.md), so a
workspace file replaces any `peers` block in your home config.

### Talk to a bot instead of a root session

When you mostly talk to one bot, such as a coordinator, make it the main
session. Then `caretz --start-peers` runs that bot in your terminal instead of
a root session, while a plain `caretz` still opens a root session:

```json
{
  "profiles": {
    "general": {
      "peers": { "main": "coordinator" }
    }
  }
}
```

`caretz --start-peers` then opens the coordinator in your pane and its
teammates in their tabs. `--main-peer NAME` picks the bot for one run, with or
without `--start-peers`, and overrides the config; `--main-peer root` opens an
ordinary root session. The config setting is skipped when you pass `-p`,
`--prompt-file`, `-s`, `--adapter`, or `--incognito`. If the main bot is
already running in another pane, `caretz --start-peers` exits; use
`--main-peer root` to open a root session next to it.

Each bot gets the flags you started `caretz` with, so
`caretz --start-peers --profile work --allow-loopback` starts its bots on the
same profile and sandbox. If a bot's definition sets its own `model`,
`reasoning`, or `profile`, that wins over the inherited flag. A flag typed
directly on `caretz peer` wins over both. Herdr then shows each bot as working,
idle, or blocked, and brings its pane back after a restart.

Check which bots are running from any session:

```text
/peers
```

## Send it work

Ask your main session to involve the bot in plain language:

```text
Ask the reviewer to check internal/peers/spool.go while you write the tests.
```

The model calls the `SendMessage` tool. The call returns immediately with a
message ID, and your session keeps working. When the reviewer finishes, its
final answer arrives in your session as a new message, and your session runs a
turn to use it, even if you are idle. If the bot is offline, the message waits
and is delivered when the bot starts.

Bots can message other bots the same way. To keep agents from talking to each
other forever:

- replies never trigger an automatic reply;
- every exchange carries a hop count, and `SendMessage` refuses to go more than
  four hops from the turn that started the chain.

## How it fits together

Each bot and each interactive session owns its own session files, and only that
process writes them. Messages travel through a private per-workspace run
directory under the config root:

- **A spool of signed message files.** Every message is a file signed with a
  per-workspace key. A message stays in the spool until the receiver has
  recorded it in its own `events.jsonpl`, so a crash leads to redelivery, never
  to loss. Message IDs are checked so a message isn't processed twice.
- **A unix socket per endpoint.** The socket only carries "you have mail"
  wake-ups, never message content. It is only a shortcut: every endpoint also
  checks its inbox every five seconds. If the receiver isn't listening, or a
  sandbox prevents creating the socket, messages still arrive a little later.
- **A lock file per endpoint.** The lock is what enforces one instance per bot.
  The kernel releases it when the process exits, so a crashed bot never leaves a
  stale lock behind.

When a bot restarts, it catches up on anything left unfinished:

- messages it recorded but never ran get run;
- results it never sent back get sent;
- a turn that was cut off mid-run is reported to the sender as `interrupted`
  instead of being run a second time.

## Security

The run directory, its sockets, and its spool files are private to your user
(`0700` directories, `0600` files). Caretz refuses to use them if their owner or
mode is wrong. The socket also checks that the connecting process belongs to the
same user.

Caretz's own tools can't reach the run directory, because it is on the sandbox's
hidden-path list. A prompt-injected `Bash` command therefore can't read the
signing key, and it can't forge a message into the spool. A file that isn't
signed, or whose content was changed, is set aside and never processed.

Treat messages between agents as untrusted input:

- Caretz frames each message as coming from another agent, not from you.
- A message can't change the receiving bot's permissions.
- A bot that needs approval for a command asks in its own pane, the same way it
  would ask you.

Unsandboxed programs running as your user are outside this protection. Such a
program can already edit your config, sessions, and skills.

## Current boundaries

- Messaging works in interactive sessions and bot processes. One-shot
  `caretz -p` runs and ACP sessions can't send or receive messages yet.
- Bots and the main session share the working tree, and nothing stops two of
  them from editing the same file at once. Use `workspace:` for bots that write.
- If a bot's turn started from a message needs your approval and nobody answers
  in its pane, the approval times out like any other unanswered prompt.
- Messages stay within one workspace. Each git worktree has its own set of bots.
