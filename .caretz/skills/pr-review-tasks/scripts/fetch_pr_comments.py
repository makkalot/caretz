#!/usr/bin/env python3
"""
Fetch review comments left by a given author on a GitHub PR and print them as
normalized JSON. Requires the GitHub CLI (`gh`) to be installed and authenticated.

Usage:
  python fetch_pr_comments.py                 # PR for the current branch, current gh user
  python fetch_pr_comments.py 123             # PR number in the current repo
  python fetch_pr_comments.py https://github.com/owner/repo/pull/123
  python fetch_pr_comments.py 123 --author someone --include-resolved

Output: JSON list of comments, each with:
  id, url, kind (inline|review|conversation), path, line, diff_hunk,
  body, created_at, is_resolved, is_outdated, thread_replies
"""
import argparse
import json
import re
import subprocess
import sys

QUERY = """
query($owner:String!, $repo:String!, $number:Int!) {
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
      number title url headRefName
      reviewThreads(first:100) {
        nodes {
          isResolved isOutdated path line originalLine
          comments(first:50) {
            nodes { databaseId url body createdAt diffHunk author { login } }
          }
        }
      }
      reviews(first:100) {
        nodes { databaseId url body createdAt author { login } }
      }
      comments(first:100) {
        nodes { databaseId url body createdAt author { login } }
      }
    }
  }
}
"""


def gh(*args):
    res = subprocess.run(["gh", *args], capture_output=True, text=True)
    if res.returncode != 0:
        sys.exit(f"gh {' '.join(args)} failed:\n{res.stderr.strip()}")
    return res.stdout.strip()


def resolve_pr(arg):
    """Return (owner, repo, number)."""
    if arg:
        m = re.match(r"https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)", arg)
        if m:
            return m.group(1), m.group(2), int(m.group(3))
        if not arg.isdigit():
            sys.exit(f"Unrecognized PR reference: {arg}")
        number = int(arg)
    else:
        number = json.loads(gh("pr", "view", "--json", "number"))["number"]
    repo = json.loads(gh("repo", "view", "--json", "owner,name"))
    return repo["owner"]["login"], repo["name"], number


def main():
    p = argparse.ArgumentParser()
    p.add_argument("pr", nargs="?", help="PR number or URL (default: PR of current branch)")
    p.add_argument("--author", help="GitHub login whose comments to collect (default: current gh user)")
    p.add_argument("--include-resolved", action="store_true", help="Also include resolved threads")
    a = p.parse_args()

    owner, repo, number = resolve_pr(a.pr)
    author = a.author or gh("api", "user", "--jq", ".login")

    data = json.loads(gh(
        "api", "graphql",
        "-f", f"query={QUERY}",
        "-F", f"owner={owner}", "-F", f"repo={repo}", "-F", f"number={number}",
    ))
    pr = data["data"]["repository"]["pullRequest"]
    out = []

    def by_author(node):
        return (node.get("author") or {}).get("login") == author and node.get("body", "").strip()

    for t in pr["reviewThreads"]["nodes"]:
        if t["isResolved"] and not a.include_resolved:
            continue
        comments = t["comments"]["nodes"]
        for i, c in enumerate(comments):
            if not by_author(c):
                continue
            out.append({
                "id": c["databaseId"], "url": c["url"], "kind": "inline",
                "path": t["path"], "line": t["line"] or t["originalLine"],
                "diff_hunk": c.get("diffHunk"), "body": c["body"],
                "created_at": c["createdAt"],
                "is_resolved": t["isResolved"], "is_outdated": t["isOutdated"],
                # replies give context (e.g. the PR author already pushed back or agreed)
                "thread_replies": [
                    {"author": (r.get("author") or {}).get("login"), "body": r["body"]}
                    for r in comments[i + 1:]
                ],
            })

    for kind, nodes in (("review", pr["reviews"]["nodes"]), ("conversation", pr["comments"]["nodes"])):
        for c in nodes:
            if by_author(c):
                out.append({
                    "id": c["databaseId"], "url": c["url"], "kind": kind,
                    "path": None, "line": None, "diff_hunk": None, "body": c["body"],
                    "created_at": c["createdAt"], "is_resolved": False,
                    "is_outdated": False, "thread_replies": [],
                })

    out.sort(key=lambda c: (c["path"] or "~", c["line"] or 0, c["created_at"]))
    json.dump({
        "pr": {"owner": owner, "repo": repo, "number": number,
               "title": pr["title"], "url": pr["url"], "branch": pr["headRefName"]},
        "author": author,
        "comments": out,
    }, sys.stdout, indent=2)


if __name__ == "__main__":
    main()
