---
title: "Use your ChatGPT plan"
weight: 12
description: "Sign in with ChatGPT and spend your plan's usage instead of API credits."
---

# Use Your ChatGPT Plan

If you pay for ChatGPT, Caretz can run on that plan's usage instead of OpenAI
API credits. Run `caretz setup`, choose **Hosted provider**, then **ChatGPT
plan (sign in, no API key)**. Your browser opens to sign in, and you pick one
of your plan's models as the default.

To add it without changing your default model, sign in from the command line
instead:

```sh
caretz auth login chatgpt
```

```text
Opening your browser to sign in with ChatGPT. If it does not open, visit:
  https://auth.openai.com/api/accounts/authorize?...
Signed in to ChatGPT as you@example.com.
Added provider chatgpt to /Users/me/.config/caretz/config.json.
Models on your plan: gpt-6.1-sol, gpt-6-astra, gpt-6.1-terra, ...
Use one with caretz --model chatgpt/gpt-6.1-sol, or /models in a session.
```

Then pick a model the way you would for any provider:

```sh
caretz --model chatgpt/gpt-6.1-sol -p "explain this repository"
```

Either way you get a `chatgpt` provider listing the models your plan offers.
`caretz auth login` sets `activeModel` only if you have none yet. Tokens are
kept in `credentials.json` next to your API keys
(see [Keep API keys out of config.json](api-keys.md)) and refresh on their
own, so you sign in again only after logging out or revoking access.
`caretz auth list` shows the account and whether plan usage is allowed.

## Sign Out

```sh
caretz auth logout chatgpt
```

This revokes Caretz's access at OpenAI and deletes the tokens. You can also
disconnect Caretz from ChatGPT's settings; the next request then asks you to
run `caretz auth login chatgpt` again.

## When Your Plan Runs Out

When you reach the plan's usage limit, the turn stops with
`ChatGPT plan usage limit reached; see ChatGPT Settings > Usage`. Caretz
doesn't retry it, because retrying only burns through the wait. Switch to
another model with `/models`, or come back once the limit resets.

## Details

**Model rules still apply.** ChatGPT is a hosted provider, so a directory that
[keeps cloud models out](model-rules.md) blocks it. To try it once without
changing the rule, pass `--allow-model chatgpt/gpt-6.1-sol`.

**Sign-in on a remote machine.** The browser redirects to
`http://127.0.0.1:1455` on the machine running Caretz. Over SSH, forward that
port first with `ssh -L 1455:127.0.0.1:1455 host`, then open the printed URL
on your own machine.

**A second account.** `caretz auth login chatgpt work` signs another account
in as provider `work`, and `caretz auth logout work` signs it out. A signed-in
provider looks like this in `config.json`, and it can't set its own `baseURL`
or be marked `local`:

```json
{
  "provider": {
    "chatgpt": {
      "auth": "chatgpt",
      "models": {
        "gpt-6.1-sol": {}
      }
    }
  }
}
```

**No prompt caching.** Plan usage doesn't cache prompts yet, so each turn
sends, and counts, the whole conversation. [Compaction](compaction.md) keeps
long sessions from eating your usage faster than they need to.

**Other limits.** Plan usage only speaks OpenAI's Responses API, which Caretz
selects for you, and only reaches `api.openai.com`. The tokens are never sent
anywhere else, and the model and sandboxed commands never see them.
