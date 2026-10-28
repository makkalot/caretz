---
title: "Bring your own server"
weight: 30
description: "Connect llama.cpp, Ollama, LM Studio, or another local server."
---

# Bring Your Own Server

Already running llama.cpp, Ollama, or LM Studio? Start it, then run
`caretz setup` and choose **Local provider**:

```sh
llama-server \
  --model /path/to/model.gguf \
  --host 127.0.0.1 \
  --port 8080 \
  --ctx-size 60000
```

| Local provider | Default OpenAI-compatible endpoint |
| --- | --- |
| llama.cpp | `http://127.0.0.1:8080/v1` |
| Ollama | `http://127.0.0.1:11434/v1` |
| LM Studio | `http://127.0.0.1:1234/v1` |
| Custom local | `http://127.0.0.1:8080/v1` |

Leave the API token empty unless the server requires one, then enter the same
context window used by the server. Caretz tests `/models` before saving. If the
server is unavailable, requires authentication, uses a different URL, or has no
loaded models, setup explains the failure and lets you retry or edit the
connection. You can deliberately save an unverified model ID after confirming
the warning.

Before saving, choose **Local / self-hosted** for this machine or another
server you control, including over Tailscale. These models participate in
**Use local** and **Always local here**. Choose **Hosted service** for a cloud
service, even through a private proxy. The default comes from the final URL.
Setup warns when a local choice sends workspace content over unencrypted HTTP
to a non-private host. The **Hosted provider** flow always saves a hosted
classification.

Install or build the server using the official
[llama.cpp server documentation](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md).
Choose a context size that the model, quantization, and available memory can
support. The model also needs a chat template that can reliably produce tool
calls; an OpenAI-compatible endpoint alone does not guarantee that capability.

## Configure by Hand

Save the provider in `~/.config/caretz/config.json` as your default across
workspaces, or in a `--config-root` directory for a project-specific setup:

```json
{
  "activeModel": "local/qwen-code",
  "provider": {
    "local": {
      "name": "Local llama.cpp",
      "local": true,
      "options": {
        "baseURL": "http://127.0.0.1:8080/v1"
      },
      "models": {
        "qwen-code": {
          "limit": {
            "context": 60000
          }
        }
      }
    }
  }
}
```

The 60k context size is a field-tested example, not a universal recommendation.
Match the configured value to the server's real limit. When the other limits
are omitted, caretz derives reserved, recent-history, and summary budgets from
the context window; see [Compaction budgets](../config/compaction.md).

A repository can make this model its default with a `.caretz/config.json`
holding only `{"activeModel": "local/qwen-code"}`. A workspace config can pick
among your providers but can't define one, so a cloned repository can't point
caretz at an endpoint of its own.

The optional provider-level `local` flag sets trust for model policy:
`true` means local/self-hosted and requires `options.baseURL`; `false` means
hosted. Omit it to detect loopback, private-network, and `.local` endpoints
automatically. Tailscale `100.64/10` addresses and `*.ts.net` names need
`local: true` to count as local.

The flag applies only when the effective URL matches the configured URL after
trimming whitespace and trailing slashes. A different `--base-url` or
`OPENAI_API_BASE_URL` uses URL detection. A config-root override that sets
`baseURL` without `local` resets classification to URL detection. When editing
the provider's URL by hand, revisit `local`: the flag follows the entry.
Explicit [model rules](../config/model-rules.md) still apply to local models.

Other
local servers work when they provide compatible `/models` and chat completion
endpoints, including the tool-call behavior caretz needs.
