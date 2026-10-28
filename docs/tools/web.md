---
title: "Web search and fetch"
weight: 10
description: "Let caretz look things up and read documentation pages."
---

# Web Search and Fetch

With web tools, Caretz can check current documentation, release notes, or an
error message instead of guessing from what the model remembers.

`WebFetch` works without setup. To enable `WebSearch`, you need an API key for
[Brave Search](https://brave.com/search/api/) or [Tavily](https://tavily.com/).
`caretz setup` asks for one after you pick a model, or store it any time:

```sh
caretz auth set search/tavily
caretz -p "read https://go.dev/doc/modules/layout and suggest a layout for this repo"
```

An exported key works too and takes precedence over a stored one:

```sh
export BRAVE_SEARCH_API_KEY=...
caretz -p "find the latest release notes for Go and summarize the changes"
```

When both keys are set, Caretz searches with Brave. A Tavily key also makes
`WebFetch` use Tavily Extract, which returns cleaner text for complex pages.

## What the Tools Do

`WebSearch` returns titles, URLs, and snippets, five results by default and up
to 20. The model can narrow a search by domain, recency (`day`, `week`,
`month`, `year`), or topic such as news.

`WebFetch` reads one `http` or `https` URL and returns its readable content,
20 KB by default and up to 50 KB. Tavily returns Markdown or text. Without
Tavily, or when Tavily fails, Caretz fetches the page itself and turns HTML into
plain text. It doesn't run JavaScript, so a page that renders in the browser
may come back nearly empty.
Direct fetches follow up to five redirects and refuse private, loopback, and
link-local addresses.

Both tools run in the Caretz process, not in the Bash sandbox, so
[network rules](../sandboxing/network.md) for commands don't apply to them. To
turn them off, use `--disable-tool WebFetch,WebSearch`. For setup problems, see
[Missing tools and web search](../troubleshooting/tools.md).
