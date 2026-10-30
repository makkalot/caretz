---
title: "Keep cloud models out"
weight: 20
description: "Choose which models may work in which directories."
---

# Keep Cloud Models Out of Private Directories

You don't need any configuration for this. If you start Caretz in, say,
`~/Documents` while a hosted model is active, it asks before sending anything:

```text
Cloud model requested; no model rule covers this directory
  directory: /Users/me/Documents
  model:     openai/gpt-5.5 (api.openai.com)
  local:     ollama/qwen
  "Always" choices save a model rule to /Users/me/.config/caretz/config.json
  [ Allow once ] [ Always allow here ] [>Use local<] [ Always local here ]
```

Shortcuts are `y` (allow once), `a` (always allow), `n` (use local) and `l`
(always local). Enter alone picks the safe answer: local models for this run.
Ctrl-C or Escape cancels the turn before it sends anything.

## Write Rules Once

Add `modelRules` to your personal `~/.config/caretz/config.json`, or to the
config selected with `--config-root` or `CARETZ_CONFIG_ROOT`:

```json
{
  "modelRules": [
    { "path": "~", "allow": [] },
    { "path": "~/src/public", "allow": ["*"] },
    { "path": "~/src/client", "allow": ["clientco/*"] },
    { "path": "~/src/public/private", "allow": [] }
  ]
}
```

Local models are always allowed; a rule lists which hosted models may also
run. Here everything under your home directory keeps to local models,
`src/public` permits every model, and `src/client` adds only the client's
provider. The private subtree inside the public project goes back to local
models. The most specific directory wins; rules do not combine.

Run `/modelrules` to see every rule and the one active here:

```text
workspace: /Users/me/src/medoit
model rules (the most specific covering path wins):
  ~                     allow [ollama/*]  (/Users/me/.config/caretz/config.json)  ← active here
  /Users/me/src/public  allow [*]  (/Users/me/.config/caretz/config.json)
```

## Override a Rule on Purpose

In interactive mode, switching with `/models` to a model the rule blocks asks
first:

```text
Model not allowed here by model rule ~
  directory: /Users/me/src/medoit
  model:     openai/gpt-5.5 (api.openai.com)
  rule:      allow [ollama/*]
  "Always allow here" saves a rule for this directory to /Users/me/.config/caretz/config.json
  [ Allow once ] [ Always allow here ] [>Keep current<]
```

**Allow once** switches for this session only; when you resume it later, the
rule applies again. **Always allow here** saves a rule for this directory that
keeps the blocking rule's allow list and adds the model you picked. **Keep
current** is the default, and it is also what happens if you don't answer
within a minute. Prompt mode and ACP can't ask, so they refuse a blocked switch
and keep the current model.

To allow a model without switching to it, for example before a `/goal` whose
worker should use it, run `/allow-model`:

```text
/allow-model openai/gpt-5.5
Allowed openai/gpt-5.5 in /Users/me/src/medoit; saved model rule to /Users/me/.config/caretz/config.json: allow [ollama/*, openai/gpt-5.5].
```

It saves the same rule as **Always allow here** and works in prompt mode and
ACP too. Every refusal ends with the `/allow-model` command to run.

To try a model in one run without saving anything, pass `--allow-model`. It
takes the same selectors as a rule and can be repeated:

```sh
caretz --allow-model chatgpt/gpt-6.1-sol --model chatgpt/gpt-6.1-sol
```

The model is allowed in this directory on top of its rule, for subagents and
goal roles too, and `/modelrules` marks the rule `(this run)`. In a directory
no rule covers, it also answers the cloud prompt for the models you named.

If you chose local models at the cloud prompt, the rest of the run follows the
same enforcement as a rule: switching to a hosted model asks to override it,
and hosted subagent models are refused. If no local model is configured, the
local answers stop Caretz before it sends any request.

## Rule Details

**Matching.** A rule covers its directory and descendants, with whole path
components: `src/client` does not cover `src/client-old`. Existing symlinks
resolve to their real paths. Paths must be absolute, `~`, or start with `~/`.
An empty `allow` list allows only local models there. Directories without a matching
rule retain normal behavior. Selectors are an exact `provider/model`,
`provider/*`, or `*`; exact selectors also match the underlying model ID of a
configured alias.

**Refusal.** A model that was chosen explicitly is refused, never swapped for
another one. That covers `--model`, `OPENAI_API_MODEL`, the model a resumed
session last used, `/models`, `/goal` role flags and `goal` role config,
subagent `model:` frontmatter, a model the agent passes to `CallSubagent`,
and background or scheduled runs. Goal role models are checked before planning
starts. The error names the rule and the `/allow-model` command. To resume a
session whose model is now blocked, pass `--model` with an allowed model.

**Fallback.** Only `activeModel` from config falls back. A blocked one
switches to the alphabetically first allowed configured or discovered model,
with that model's endpoint, protocol, reasoning defaults, and limits, and
Caretz says so. Adapter restrictions also apply. Fallback saves the model
actually used in session state and leaves the project's `activeModel` alone.
If no model is allowed, Caretz stops before sending workspace content.

**What counts as local.** Providers can explicitly be local/self-hosted or
hosted, including servers you control elsewhere and cloud proxies on loopback.
Without an explicit classification, URL detection applies; see
[provider classification](../local-models/own-server.md#configure-by-hand)
for configuration and endpoint override rules. Because local models pass every
rule, mark a loopback proxy to a cloud service `"local": false`.
When nobody can answer the cloud prompt, as in piped prompt mode, ACP
editors and embedded sessions, Caretz prints one warning that names the
directory and endpoint, and continues. Saved rules go to your home config, or
to the `--config-root` config when that file already defines `modelRules`.

**Saved local rules.** **Always local here** saves a snapshot of the current
local providers as `provider/*` selectors. Local providers you add later are
allowed without changing the rule. Reclassifying one as hosted leaves existing
rules unchanged. Caretz shows a notice when an active rule allows
the hosted model; review that rule if you intended local-only access.

**Notices.** Caretz shows the rule, its source config, the workspace, and the
model outcome. Prompt mode writes notices to stderr. Interactive mode shows
system notices and the active rule alongside its startup status and in
`/models`. ACP sends session notifications or protocol errors, and ACP model
lists show only allowed models. Model changes are recorded in session events.

**Safeguards.** Workspace `.caretz/config.json` rules are ignored with a
warning, so a workspace cannot loosen personal rules, and since it can't define
providers, it can't re-point an allowed model at another endpoint.
`--base-url` and `OPENAI_API_BASE_URL`
must equal the selected provider's configured endpoint, including after
fallback. Invalid personal rules cause startup to fail.

**Limits.** Rules govern the model for a workspace, rather than individual file
reads. For example, a hosted model in `src/public` can receive a file from
Documents if file-access permissions allow reading it. Use
[sandbox read permissions](../sandboxing/files.md) to protect those files. The
separate `analyze_local_image` endpoint is outside this model policy.
