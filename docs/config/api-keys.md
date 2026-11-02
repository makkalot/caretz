---
title: "Keep API keys out of config.json"
weight: 15
description: "credentials.json, caretz auth, and why workspace configs can't define providers."
---

# Keep API Keys Out of config.json

Config files hold settings, never secrets. Each provider's key lives in
`~/.config/caretz/credentials.json` under the same provider name it has in
`config.json`. `caretz setup` and `caretz auth set` write that file with mode
`0600`, Caretz refuses to read it if other users can (the same way ssh treats
private keys), and sandboxed commands and the file tools cannot see it.

Because `config.json` holds no secrets, you can keep it in a dotfiles repo or
paste it into a bug report.

## Manage Keys from the Command Line

```sh
caretz auth set openrouter         # prompts with hidden input
echo "$TAVILY_KEY" | caretz auth set search/tavily
caretz auth list                   # shows which providers have a key, masked
caretz auth rm openrouter
```

`caretz auth list` prints something like:

```text
local          not set
openrouter     credentials.json …9f3a
search/tavily  credentials.json …41c0
```

Add `--config-root DIR`, or set `CARETZ_CONFIG_ROOT`, to also list the
providers a config root defines. Their keys are stored here too.

A ChatGPT subscription needs no key: `caretz auth login chatgpt` signs in
through the browser and stores refreshing tokens here instead. See
[Use your ChatGPT plan](chatgpt.md).

A provider without a stored key, such as a local server that ignores auth,
needs nothing more. It gets the placeholder `noset`, never your
`OPENAI_API_KEY`. That env key only applies to endpoints that aren't a
configured provider: a one-off run with `--base-url` or `OPENAI_API_BASE_URL`,
or CI with no config. `--api-key` always wins, because you typed it for this
run. Model discovery (`caretz setup` and `caretz models --refresh`) sends
neither: each provider's `/models` request carries only that provider's stored
key. Web search keys (`search/tavily`, `search/brave`) are used when
`TAVILY_API_KEY` and `BRAVE_SEARCH_API_KEY` are not set.
[LocalImage](../tools/local-image.md) reuses a provider's key when its
`model` names that provider.

## Workspace Configs Can't Define Providers

A workspace `.caretz/config.json` from a cloned repository can't define or
change providers: a `provider` block there is a load error. It can only choose
among the providers you configured, by setting `activeModel`, so it can't
redirect your keys, your environment variables, or your code to a server it
picks. Projects that need their own endpoint use a `--config-root` config,
which you choose explicitly. An `apiKey` with a value in any `config.json` is
also a load error.

## Other Locations

Set `CARETZ_CREDENTIALS_FILE` to keep credentials somewhere other than
`~/.config/caretz/credentials.json`, for example a mounted secret in a
container. Set `CARETZ_ALLOW_INSECURE_CREDENTIALS=1` only when the filesystem
cannot hold Unix permissions.

## Upgrading

Upgrading from a config that still holds a key: Caretz stops with an error
naming the provider and file. Run `caretz auth set <provider>` and delete the
`apiKey` line from that `config.json`. A `toolConfig.localImage.authToken`
gets the same error: add the vision server as a provider, store its key with
`caretz auth set`, and set `localImage.model` to `<provider>/<model>` without
`endpoint`. Older `"apiKey": "noset"` and `"authToken": "noset"` lines for
local servers carry no secret and keep working.
