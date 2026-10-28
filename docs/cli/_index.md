---
title: "Use caretz"
description: "Interactive sessions, one-shot tasks, and running commands."
---

# Use Caretz from the Terminal

Caretz works best when you give it a concrete task inside a project. Start in a
repository and open an interactive session:

```sh
cd your-project
caretz
```

Try prompts like:

```text
Explain how authentication works in this project.
Find the cause of the failing tests and fix it.
Add pagination to the users endpoint and test it.
Review my current changes for bugs and missing tests.
```

Caretz can inspect the repository, edit files, and run commands while keeping the
conversation open for follow-up requests. To see every tool result and token
counter, use `caretz --output detailed`. To change the colors, pick a
[theme](themes.md).

## Run One Task and Exit

Use `-p` for a task that does not need a back-and-forth conversation:

```sh
caretz -p "summarize the current git diff"
caretz -p "run the tests, fix the failure, and explain what changed"
caretz --prompt-file task.md
```

Start a prompt with `!` to run a command in the sandbox without calling the
model. This works with `-p` and at the interactive prompt:

```sh
caretz -p '!go test ./...'
```

## Work in Another Repository

You do not have to change directories first:

```sh
caretz --workspace ../another-project -p "review the latest commit"
```

## Find More

The installed binary is the source of truth for available commands and options:

```sh
caretz --help
caretz session --help
```

Next: [continue earlier work](../sessions/_index.md), hand over a
[goal](../goals/_index.md), [run a task on a schedule](../schedules/_index.md),
or [grant network access](../sandboxing/network.md).
