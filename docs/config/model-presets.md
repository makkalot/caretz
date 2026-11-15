---
title: "Model presets"
weight: 11
description: "One model, several setups: thinking level, sampling, server options."
---

# Model Presets

Give one model several names with different settings: a quick-thinking
preset for chat, a thorough one for coding, both served by the same model.
Each entry under `models` is a preset. Its key is the name you pick, and
`model` is the ID sent to the server:

```json
{
  "activeModel": "local/qwen-chat",
  "provider": {
    "local": {
      "options": {
        "baseURL": "http://127.0.0.1:8080/v1"
      },
      "models": {
        "qwen": {
          "limit": { "context": 128000 }
        },
        "qwen-chat": {
          "model": "qwen",
          "reasoning": "low",
          "params": { "temperature": 0.7, "max_tokens": 8192 },
          "limit": { "context": 128000 }
        },
        "qwen-code": {
          "model": "qwen",
          "reasoning": "high",
          "limit": { "context": 128000 }
        }
      }
    }
  }
}
```

Presets work anywhere a model ref does: `/models local/qwen-code`,
`--model local/qwen-chat`, subagents, goal roles, schedules, and
`modelRules`. A resumed session goes back to the preset it used.

`reasoning` is sent as `reasoning_effort` (or `reasoning.effort` with the
Responses protocol). `--reasoning` and `/models … reasoning=` still replace it
for a run or session. `params` adds any other top-level request field your
server understands, such as sampling settings, `max_tokens`, or
`chat_template_kwargs`, and is sent with every request to that preset. Run
with `-v` to see which params a session sends.

## Rules

- `params` can't set fields caretz builds itself: `model`, `messages`,
  `input`, `instructions`, `tools`, `stream`, `stream_options`, `store`, and
  `prompt_cache_key`. Use `reasoning` for `reasoning` and `reasoning_effort`.
  Config loading fails if `params` contains any of them.
- Params belong to the preset. A raw `--model qwen` or `OPENAI_API_MODEL`
  sends none.
- Keep an entry keyed by the plain model ID (`qwen` above) when other config
  names the model by its ID. Without one, `local/qwen` matches every preset
  that uses `qwen` and fails with an error that names them.
- A config root or the home config may set `params`. A workspace
  `.caretz/config.json` can't, because it can't define providers.
- Unknown fields are passed through unchecked. A server may ignore them or
  reject the request, so check your server's docs for what it accepts.
