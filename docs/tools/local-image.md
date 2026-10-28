---
title: "Analyze images locally"
weight: 20
description: "Let a text-only model read screenshots through a vision model."
---

# Analyze Images with a Vision Model

Your coding model may not see images. `LocalImage` sends a screenshot or
diagram from the workspace to a vision model and gives the coding model its
text answer. `caretz setup` offers to pick the vision model from the provider
you just connected. To set it by hand, point it at any provider model you
already configured, in `~/.config/caretz/config.json`:

```json
{
  "profiles": {
    "general": {
      "toolConfig": {
        "localImage": {
          "model": "local/qwen2.5-vl",
          "maxTimeSeconds": 90
        }
      }
    }
  }
}
```

Then ask about an image:

```text
Look at screenshots/login-error.png and fix the layout bug it shows.
```

`model` is a `provider/model` reference, like `activeModel`. LocalImage calls
that provider's `/chat/completions` with the provider's base URL and the key
stored by [`caretz auth`](../config/api-keys.md), so there is nothing extra to
authenticate. The same [model rules](../config/model-rules.md) apply: if the
workspace's rule does not allow the vision model, the tool call is refused and
the image is not sent. `maxTimeSeconds` is optional. The image must be
readable under the normal [file permissions](../sandboxing/files.md).

## Call a Vision Server Directly

For a quick local server you have not added as a provider, set `endpoint` to
its full chat completions URL. `model` is then the server's own model ID, and
no key is sent:

```json
{
  "profiles": {
    "general": {
      "toolConfig": {
        "localImage": {
          "endpoint": "http://127.0.0.1:8002/v1/chat/completions",
          "model": "qwen2.5-vl"
        }
      }
    }
  }
}
```

A direct endpoint is outside model rules, and config files can't hold its
key; a server that needs one belongs in a provider. Set `endpoint` in your own
config or a `--config-root` config: in a workspace `.caretz/config.json` it is
a load error, so a cloned repository can't send your images to a server it
picks.
