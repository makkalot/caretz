# caretz website

This is a small Hugo site for the caretz project.

The site mounts the repository's top-level `docs/` directory at `/docs/`, so
documentation pages can stay in one place.

```sh
cd site
hugo server
```

Build the static site:

```sh
cd site
hugo --minify
```

## Deployment

`.github/workflows/pages.yml` builds the site and deploys it to GitHub Pages on
every push to `main` that touches `docs/` or `site/`, and can also be run
manually. `static/CNAME` keeps the custom domain (`caretz.dev`) set across
deploys.
