---
title: "Questions"
weight: 15
description: "Answer the model's questions from a picker instead of typing."
---

# Questions

When Caretz is blocked on a decision that is yours to make, it asks with the
`AskUser` tool instead of burying questions in a long reply. You answer from a
picker, with the option the model recommends at the top:

```text
? [ Auth ] [ Targets ]
  Which auth flow should the CLI use?
  › 1. Device code (recommended)
         Works over SSH, no local browser needed
    2. Browser redirect
         Smoother on desktop, needs a localhost port
    3. Other…
  ↑/↓ move · enter select · esc skip · ←/→ question
```

Press **Enter** or a number to pick. On a multi-select question, **Space**
toggles and **Enter** confirms. **Other…** lets you type your own answer. With
several questions, **←/→** or **Tab** switches between them. Once every
question is answered, the answers go back to the model and the turn continues.

**Esc** skips the questions: the model continues with its own judgment and
says what it assumed. **Ctrl-C** stops the turn.

## When Caretz Asks

`AskUser` is offered only when someone can answer: the interactive CLI and
apps that answer permission requests. Prompt mode (`-p`), scheduled runs,
subagents, and goal workers never see it. To turn it off, disable it like any
other tool:

```sh
caretz --disable-tool AskUser
```

If you don't answer within the permission timeout, the model continues with
its recommended options.
