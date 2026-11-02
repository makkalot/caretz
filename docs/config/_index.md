---
title: "Configuration"
description: "Connect a model with caretz setup or a JSON file."
---

# Configuration

```sh
caretz setup
```

The guided setup works with hosted providers and with local llama.cpp, Ollama,
LM Studio, or any OpenAI-compatible server. It tests the endpoint's `/models`
route, reports actionable connection errors, selects a default, and saves the
provider to `~/.config/caretz/config.json`. A hosted provider's API key goes to
`~/.config/caretz/credentials.json` instead, so `config.json` holds no secrets
and you can keep it in a dotfiles repo or paste it into a bug report. See
[Keep API keys out of config.json](api-keys.md). To run on a ChatGPT
subscription instead of API credits, pick **ChatGPT plan** among the hosted
providers; see [Use your ChatGPT plan](chatgpt.md).

## Configure by Hand

Put providers in `~/.config/caretz/config.json`, or in a
[`--config-root`](overrides.md#locations-and-precedence) config for a
project-specific setup. A workspace `.caretz/config.json` can then pick one of
those models with `activeModel`:

```json
{
  "activeModel": "openai/gpt-5",
  "provider": {
    "openai": {
      "options": {
        "baseURL": "https://api.openai.com/v1"
      },
      "models": {
        "gpt-5": {
          "protocol": "responses",
          "reasoning": "medium",
          "limit": {
            "context": 200000
          }
        }
      }
    }
  }
}
```

The key for `openai` comes from `caretz auth set openai`; see
[Keep API keys out of config.json](api-keys.md).

A local server works the same way. Set the context limit to what your server
actually serves. 60k has held up in hours-long sessions:

```json
{
  "activeModel": "local/qwen-code",
  "provider": {
    "local": {
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
