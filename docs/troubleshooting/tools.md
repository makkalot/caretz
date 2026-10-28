---
title: "Missing tools and web search"
weight: 20
description: "A tool isn't available, or WebSearch doesn't work."
---

# Missing Tools and Web Search

## A Tool Is Missing

Tool availability is dynamic. Check for:

- `--tools`, which creates a repeatable or comma-separated allowlist.
- `--disable-tool`, which removes matching tools.
- `--plan` or Shift+Tab plan mode, which keeps Edit, Write, and SendFile listed but makes their calls fail.
- `--no-tasks` or `profiles.general.tasks.enabled: false`.
- Missing web-search or local-image configuration.
- An application that does not register `SendFile` or another custom tool.

Names are case-insensitive in CLI allow/deny policy. Run `caretz --help` for the
canonical built-in names.

## Web Search Is Unavailable

`WebSearch` requires `BRAVE_SEARCH_API_KEY` or `TAVILY_API_KEY`, or a key saved
with `caretz auth set search/brave` or `caretz auth set search/tavily`; restart
Caretz after adding the key. See [Web search and fetch](../tools/web.md). Brave takes precedence when both exist. `WebFetch`
does not require a key and can still retrieve a public text/HTML page directly.

If direct fetch reports a blocked target, verify that the URL is public.
Private, loopback, link-local, and otherwise unsafe resolved addresses are
rejected to reduce server-side request forgery risk.

Core `WebFetch` retrieves public text and HTML directly but does not execute
JavaScript. A page that only renders content in a browser may therefore return
an empty application shell.
