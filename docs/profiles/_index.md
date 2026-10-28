---
title: "Profiles"
description: "Named policies for prompts, tools, and sandbox grants."
---

# Runtime Profiles

Most people never need a profile. A profile is a named policy (prompt, tools,
skills, agents, sandbox grants) you can switch on per run, for example to give
Caretz broad machine access only when you ask for it.

```sh
caretz --profile coding
```

| Profile | Use it for |
| --- | --- |
| `general` | The default: neutral prompt, all tools, normal sandbox. |
| [`coding`](coding.md) | Coding prompt, package registries, loopback, and your existing GitHub/SSH logins. |
| [`ops`](ops.md) | Maintaining the machine itself: packages, services, disk cleanup. |
| `compact` | Bash only, no skills, agents, or tasks. Good for tiny models. |

`CARETZ_PROFILE` selects a profile too, and `"profile"` in config sets the
default. The interactive startup banner shows the resolved choice as
`profile=coding`. Profiles cannot select models or reasoning: the root
`activeModel` and provider model entry remain authoritative.

The built-in `compact` profile exposes only `Bash`, clears skills and agents,
and disables task tracking. Its tool allowlist also excludes session tools such
as schedules.
