---
title: "Switch models"
weight: 10
description: "Several providers, /models, and caretz models --refresh."
---

# Switch Models

Use `--model provider/model` for one run, or `/models` in a session to pick and
save a new default. When a provider adds models, fetch its new list; to remove
a provider you no longer use, or walk through the ones that are gone:

```sh
caretz models --refresh
caretz models --remove openrouter
caretz models --prune
```

## Several Providers

Configure several providers side by side and switch between them:

```json
{
  "activeModel": "openai/gpt-5",
  "provider": {
    "openai": {
      "options": {
        "baseURL": "https://api.openai.com/v1"
      }
    },
    "local": {
      "options": {
        "baseURL": "http://127.0.0.1:8080/v1"
      }
    }
  }
}
```

Here `openai` uses the key saved with `caretz auth set openai`, and the local
server, which has no stored key, needs none.

A model entry is only needed when you want to customize discovered metadata or
use a model the endpoint does not advertise. Caretz uses the broadly compatible
`chat-completions` protocol when `protocol` is omitted. Set
`"protocol": "responses"` for models that require the OpenAI Responses API.

`/models provider/model` saves the selection as `activeModel` in the project or
config-root config file. Model IDs may contain `/`; only the first slash
separates the provider key from the model ID.

## Model Discovery

Caretz never contacts providers at startup, so it starts instantly, even on a
laptop away from its home server. The models each provider advertises are
cached in `~/.config/caretz/models.json`. `caretz setup` fills the cache for
the provider it saves, and `caretz models --refresh` asks every provider again:

```text
home-gpu  ○ unreachable: no route to host  http://10.0.0.5:8001/v1
    home-gpu/qwen-coder
openai  ● online, 42 models  https://api.openai.com/v1
  * openai/gpt-5
    openai/gpt-5-mini
```

A provider that fails to answer keeps its cached models. Configured model
entries are always listed, whether or not they were fetched. Each request
carries only that provider's key from `caretz auth`. With `--config-root DIR`
or `CARETZ_CONFIG_ROOT`, the config root's providers are listed and refreshed
too.

`caretz models` prints the same list from the cache without any network
request, with when each provider was last fetched. `caretz models --prune`
asks before deleting each provider that cannot be reached from your home
config. It writes nothing until you have answered every question, keeps the
rest of the file as it was. A provider that answers with an error such as 401
is reachable and is not offered. A local server that is only stopped shows as
unreachable too, so answer no for providers you still use.

`caretz models --remove NAME` (repeatable) deletes a provider from your home
config, whether or not it is reachable. It lists everything it will delete and
asks once before writing. Both `--remove` and `--prune` also
delete the provider's API key and every reference to it: an `activeModel` on
it, its selectors in `modelRules`, and profile goal roles and schedule models
that use it, which then fall back to the session's model. A model rule left
with nothing allowed is kept and reported, so that directory keeps to local
models until you allow a hosted one. Providers from a config root are never removed;
edit that file instead.

Use `--no-model-discovery`, or `"modelDiscovery": {"enabled": false}` in a
profile, to ignore the cache. Then configure a model under `models` or pass
`--model provider/model`. The `OPENAI_API_MODEL` environment override is also
available.
