---
title: "Tools"
description: "What caretz can do out of the box, and how to add your own tools."
---

# Tools

Tools are how Caretz acts: it reads and edits files, runs commands, searches
the web, and calls anything else you register. You can narrow the set for one
run:

```sh
caretz --tools Read,Grep,Find,Ls -p "explain the request flow"
caretz --disable-tool Bash -p "review this diff without running anything"
```

## Built-in Tools

| Tool | What it does | Available |
| --- | --- | --- |
| `Read`, `Edit`, `Write` | Read and change files in the workspace | Always |
| `Ls`, `Find`, `Grep` | List, find, and search files | Always |
| `Bash` | Run commands in the [sandbox](../sandboxing/_index.md) | Always |
| `UpdateTasks`, `ListTasks` | Track multi-step work | Unless `--no-tasks` |
| `CallSubagent` | Hand a job to a [subagent](../customization/subagents.md) | Always |
| `AskUser` | Ask you [multiple-choice questions](../cli/questions.md) | When someone can answer |
| `WebFetch` | Read a public web page | Always |
| `WebSearch` | Search the web | With an API key, see [Web search and fetch](web.md) |
| `LocalImage` | Describe an image with a local vision model | When [configured](local-image.md) |
| `SendFile` | Send a workspace file to the user | In ACP editors and apps that support it |

Interactive sessions also get `RegisterSchedule`, `UpdateSchedule`,
`ListSchedules`, and `RemoveSchedule` for [schedules](../schedules/_index.md).

Tool names are case-insensitive in `--tools` and `--disable-tool`. To keep the
same set every time, put `tools` or `disabledTools` in a
[profile](../profiles/custom-profiles.md).

## Add Your Own

- [Add tools to the CLI](custom-cli.md): ship your own `caretz` binary with
  extra tools.
- [Add tools to your Go app](custom-go.md): register tools when embedding the
  agent.
