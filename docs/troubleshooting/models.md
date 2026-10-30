---
title: "Model and connection errors"
weight: 10
description: "No model selected, 404s, auth errors, failed setup tests."
---

# Model and Connection Errors

## `no model selected` or `OPENAI_API_BASE_URL not set`

Run `caretz setup` to configure a provider and select a model. Alternatively,
select a complete `activeModel` in a config file or diagnose the connection with
one-run CLI flags:

```sh
caretz --api-key noset \
  --base-url http://127.0.0.1:8080/v1 \
  --model qwen-code
```

With an existing provider configuration, an explicit model does not need to be
listed under that provider's `models` map:

```sh
caretz --no-model-discovery --model local/qwen-code
```

Caretz uses `--model` first, then `OPENAI_API_MODEL`, then `activeModel`. If an
old `activeModel` refers to a model that is no longer configured or discovered,
either select a model explicitly or update `activeModel`. `--no-model-discovery`
ignores the cached model list, so only configured models are known. Configuration errors
are reported normally and exit with a nonzero status; they should not produce a
panic stack trace.

Run from the expected workspace or pass `--workspace` if `.caretz/config.json`
is elsewhere. With `--config-root` or `CARETZ_CONFIG_ROOT`, Caretz reads
`<root>/config.json` instead of the workspace config.

## A Model Is Missing from `/models`

`/models` lists configured models plus the ones cached the last time caretz
asked each provider. After a provider adds a model, or after you change its
`baseURL`, fetch the list again:

```sh
caretz models --refresh
```

The refresh never sends `OPENAI_API_KEY`, so a provider whose key is only
there shows `key rejected (HTTP 401); run caretz auth set <provider>`. Save
the key that way, then refresh again. Providers from a config root are only
refreshed when you pass `--config-root` or set `CARETZ_CONFIG_ROOT`.

## Authentication or 404 Errors

Caretz uses OpenAI-compatible chat completions. Confirm that the base URL
includes the server's expected `/v1` prefix, the configured model is served, and
the endpoint implements chat completions and tool calls. A server described as
“OpenAI-compatible” may still omit tool calling, reasoning fields, streaming
details, or usage data.

Use `-v` to see the selected model, where that choice came from (`--model`,
`OPENAI_API_MODEL`, a resumed session, or config), its endpoint, and each
request's timing and token counts, without printing the API key. `-vv` adds
token-estimate calibration and full tool arguments. Increase `--llm-timeout` for slow local models and use
`--llm-max-attempts 1` while diagnosing deterministic request errors.

## ChatGPT Plan Errors

`not signed in` or `run caretz auth login chatgpt`: the tokens were revoked,
expired beyond refresh, or never saved. Run the command the error names. A 403 saying the
account cannot use its plan in other apps comes from OpenAI, not caretz; check
the plan in ChatGPT. For the usage limit, see
[Use your ChatGPT plan](../config/chatgpt.md#when-your-plan-runs-out).

## Local Setup Connection Test Fails

`caretz setup` tests the selected provider's `/models` endpoint before saving.
Start the local server first, and load or pull at least one model. The standard
defaults are `127.0.0.1:8080` for llama.cpp, `127.0.0.1:11434` for Ollama, and
`127.0.0.1:1234` for LM Studio; edit the URL in setup if your server differs.

A timeout can mean the server is still starting. A 404 usually means the Base
URL is missing the provider's OpenAI-compatible `/v1` prefix. Authentication
errors require the token configured by the local server. If a provider cannot
list models but you know the model ID, setup can save it as unverified after an
explicit confirmation.
