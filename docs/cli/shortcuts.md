---
title: "Shortcuts"
weight: 20
description: "Slash commands, @ mentions, and keys."
---

# Interactive Shortcuts

Run `/help` to see every command. These are the ones you will use most:

```text
/models                         choose a configured model
/modelrules                     show model rules and the one active here
/allow-model provider/model     allow a model in this directory
/context                        check current context usage
/tasks                          show this session's task list
/compact                        summarize older conversation turns
/clear                          start a fresh sibling session
/theme                          preview and switch color themes
/exit                           leave the session
```

Type `@` to mention files and configured agents, and press Tab to complete
them. For example, `@reviewer check my diff` hands the work to a
[reviewer agent](../customization/subagents.md).

## Keys

- **Shift+Tab** toggles [plan mode](plan-mode.md).
- **Ctrl+C** cancels the running request, or exits when the prompt is idle.
- **Ctrl+D** exits from an empty prompt.
