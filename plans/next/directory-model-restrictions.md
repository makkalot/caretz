# Directory-based model restrictions

Status: implemented; unit and integration tests, lint, and documentation checks pass.

## Summary

Let users choose which models may work in which directories. Rules live only
in personal config, and the most specific directory wins. That lets a user
restrict their home directory while opening one project to every model and
locking a client project to that client's approved models.

Caretz never switches or refuses a model silently. Every time a rule changes
or blocks a model choice, the user sees which rule applied and what happened.

Restrictions apply to startup, resumed sessions, interactive and ACP model
switches, subagents, and goal roles.

## Intent

```json
{
  "modelRules": [
    { "path": "~",         "allow": ["local/*"] },
    { "path": "~/src/a/b", "allow": ["*"] },
    { "path": "~/src/a/c", "allow": ["clientco/*", "local/qwen"] }
  ]
}
```

- Home, `~/Documents`, `~/Downloads`, and anything else under `~` without a
  more specific rule: local models only.
- `~/src/a/b`: any model.
- `~/src/a/c`: only the client-approved models.

## Configuration

- `modelRules` is a root-level list in personal config
  (`~/.config/caretz/config.json`), or in the `--config-root` /
  `CARETZ_CONFIG_ROOT` config, which the user also controls.
- Workspace `.caretz/config.json` cannot define `modelRules`. If it contains
  them, caretz warns that they are ignored. This means a project can never
  loosen a user's rules.
- Each rule has:
  - `path`: required, absolute or starting with `~/`. It covers that directory
    and everything beneath it.
  - `allow`: required list of selectors: exact `provider/model`,
    `provider/*`, or `*`. An empty list means no model may run there.
- There is no `deny` list and no rule combining.

## Matching

- Resolve the actual workspace (`--workspace`, or each ACP session's
  directory) and each rule path with symlinks resolved. If a rule path does
  not exist, match it lexically.
- Compare whole path components: `~/src/a/c` covers `~/src/a/c/x` but not
  `~/src/a/cc`.
- Of all the rules whose path contains the workspace, the longest wins. If no
  rule matches, nothing is restricted, which is current behavior.
- A selector matches either the configured model ref or the underlying model
  ID for aliases.
- Invalid personal `modelRules` (missing `path`, a relative path, a bad
  selector, or two rules for the same path) is an error. Caretz refuses to
  start rather than run unrestricted.

## Enforcement

- Check every model selection against the winning rule: startup (CLI flag,
  environment variable, `activeModel`), resumed sessions, `/models`, ACP model
  changes, subagent `model:` frontmatter, and goal-role models. Compaction
  uses the session's provider, so it inherits the check.
- A blocked model that was chosen explicitly is refused, never replaced:
  `--model`, `OPENAI_API_MODEL`, a resumed session's model, `/models`, ACP
  `set_model`, `/goal` role flags and goal role config (checked before
  planning), subagent `model:` frontmatter, the `CallSubagent` `model`
  argument, and background or scheduled runs. Every refusal names the rule
  and ends with `run /allow-model <ref> to allow it here`.
- Only `activeModel` from config falls back, to the first allowed model in
  alphabetical order of provider/model reference. The candidates are the
  configured and discovered models, intersected with any existing adapter
  restrictions. The replacement brings its full model configuration with it:
  endpoint, reasoning defaults, protocol, and limits.
- `/allow-model <ref>` saves a rule for the workspace that keeps the active
  rule's allow list and adds ref (as the override prompt's "Always allow
  here" does) and applies it to the running session, its subagents and goal
  roles, and, in ACP, every session. Slash commands come only from user
  input, so the model cannot loosen a rule.
- If no model is allowed, caretz stops before sending any workspace content.
- A selection that doesn't resolve to a known provider triggers fallback when
  a rule matches.
- Model lists and completions show only allowed models.
- Session state records the model actually used. Fallback never rewrites the
  project's `activeModel`.
- The existing ACP allowed-model check (`internal/app/acp.go`) should follow
  the same fallback and notification path, so ACP and the CLI behave the same.

### Bypass guards

These apply only when a rule matches the workspace:

- Reject `--base-url` or `OPENAI_API_BASE_URL` when it differs from the
  selected provider's configured URL. An identical URL is accepted.
- Reject workspace `.caretz/config.json` overrides of a provider's `baseURL`,
  `apiKey`, or a model's underlying `model` ID. Otherwise a project could
  point `local/*` at a hosted API. The error names the overridden field and
  the rule.

## Notifications: no silent switches

Every enforcement event tells the user what happened. Each notice includes:

- the workspace;
- the matching rule, with its path and the file it came from;
- the requested model;
- the outcome: allowed through a rule, switched to X, refused, or stopped.

When notices appear:

| Event | Notice |
|---|---|
| Startup or resume in a directory covered by a rule | One line showing the active rule, e.g. `model rule ~/src/a/c (~/.config/caretz/config.json): allow clientco/*, local/qwen` |
| Startup with a blocked `activeModel` | `requested model "openai/gpt-5.5" is not allowed; using clientco/model-x; model rule ~/src/a/c ...` |
| Startup with a blocked `--model`, `OPENAI_API_MODEL` or resumed session model | Error with the rule and the `/allow-model` hint; exit non-zero |
| Interactive `/models` switch to a blocked model | Refused with the rule; the current model is kept |
| Subagent or goal role with a blocked model | Error that names the subagent or role, the rule and the `/allow-model` hint |
| `/allow-model` | The saved rule and its config file, also recorded as a session event |
| No allowed model | Error with the rule; exit non-zero before any request |
| Endpoint or workspace provider override rejected | Error naming the flag, environment variable, or config field, and the rule |
| `modelRules` in workspace config | Warning that it is ignored, and where rules belong |

Where notices go in each mode:

- **TUI:** a visible system message in the transcript, plus the status line
  showing the active rule.
- **Prompt mode:** stderr, so stdout stays clean for scripts.
- **ACP:** a session notification or error response that keeps the protocol
  stream valid.
- Every switch is also recorded as a session event, so the history shows when
  and why the model changed.

`/models` marks the active rule in its header, so it is clear why some models
are missing from the list.

## Examples

Available models are `local/qwen`, `openai/gpt-5.5`, and `clientco/model-x`;
the rules are the ones under Intent.

| Workspace | Winning rule | Requested | Result |
|---|---|---|---|
| `~/Downloads` | `~` | `openai/gpt-5.5` | Notice; switch to `local/qwen` |
| `~/Documents` | `~` | `local/qwen` | Allowed; rule shown at startup |
| `~/src/a/b/deep` | `~/src/a/b` | `openai/gpt-5.5` | Allowed |
| `~/src/a/c` | `~/src/a/c` | `openai/gpt-5.5` | Notice; switch to `clientco/model-x` |
| `~/src/a/c` | `~/src/a/c` | `/models openai/gpt-5.5` | Refused with notice; current model kept |
| `~/src/a/cc` | `~` | `clientco/model-x` | Notice; switch to `local/qwen` |
| `/tmp/scratch` | none | anything | No restriction, no notice |
| `~/src/a/c` | `~/src/a/c` | `clientco/model-x` with `--base-url https://api.openai.com/v1` | Error: endpoint override not allowed under rule |

To lock a subtree down again inside an open one, add a more specific rule:

```json
{ "path": "~/src/a/b/vendor-x", "allow": ["local/*"] }
```

## Workspace rules versus file access

Rules decide which model works in a workspace. They do not check each file
that tools read. In `~/src/a/b` with GPT-5.5, reading `~/Documents/notes.txt`
sends its contents to GPT-5.5 if file-access permissions allow the read. File
access restrictions are what protect such files. Out of scope for this plan:
per-file model restrictions and the `analyze_local_image` endpoint.

## Validation and acceptance

- Unit tests:
  - longest-prefix matching across `~`, `~/src/a/b`, and `~/src/a/c`;
  - sibling boundary (`~/src/a/cc`);
  - symlinked workspaces and rule paths;
  - rule paths that don't exist;
  - wildcards, aliases and model IDs;
  - empty allow lists;
  - invalid rules that fail closed;
  - rules in workspace config that are ignored with a warning.
- Fallback tests: alphabetical order, discovery enabled and disabled,
  unresolved raw selections, intersection with adapter restrictions, and no
  allowed model, where a fake provider must receive zero requests.
- Notification tests: each event in the notification table produces its
  notice in TUI, prompt (stderr), and ACP modes, and no switch happens without
  one. Add a session-event assertion for switches.
- Bypass tests: `--base-url` and `OPENAI_API_BASE_URL` overrides, including
  ones combined with a blocked model and with fallback; workspace
  `baseURL`/`apiKey`/`model` overrides under a rule.
- Integration tests: startup, `--workspace`, `--config-root`, resume across
  workspaces, `/models`, ACP sessions with different directories on one
  server, subagents, and goal roles.
- Document the directory-tree rules, most-specific-wins, fallback and
  notification behavior, and the file-access boundary. Run `make test`, and
  run `make doccheck` after changing user docs.
- Do not commit without user approval.
