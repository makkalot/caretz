#!/usr/bin/env python3
"""Compile-check Go examples from README/docs/site.

Full programs are compiled as-is. Fragments (struct fields, statement blocks)
are wrapped in the smallest legal host so every identifier, field name and
method signature gets type-checked against the real package.
"""
import os, re, subprocess, sys, shutil

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
BLOCKS = os.path.join(ROOT, ".doccheck", "blocks")
WORK = os.path.join(ROOT, ".doccheck", "gomod")
GOBIN = os.environ.get("GO", "go")

# name -> wrapper kind
KINDS = {
    "README__b05": "program",
    "docs_embedding__index__b03": "program",
    "docs_embedding__index__b04": "clientopts",
    "docs_embedding_strip-down__b04": "program",
    "docs_embedding_sessions__b02": "session_stmts",
    "docs_embedding_sessions__b03": "client_stmts",
    "docs_embedding_sessions__b04": "audit_stmts",
    "docs_embedding_tools-and-sandbox__b02": "clientopts",
    "docs_embedding_tools-and-sandbox__b03": "clientopts",
    "docs_extending-cli__index__b03": "program",
    "docs_extending-cli_custom-adapter__b02": "program",
    "docs_extending-cli_acp-in-process__b02": "program",
}

HDR = '''package main

import (
%(extra_imports)s)
'''

WRAPPERS = {
    "clientopts": {
        "extra": ['\t"github.com/makkalot/caretz"\n'],
        "body": 'func main() {\n\tvar _ = caretz.ClientOptions{\n%(code)s\n\t}\n}\n',
    },
    "client_stmts": {
        "extra": ['\t"context"\n', '\t"github.com/makkalot/caretz"\n'],
        "body": 'func run(ctx context.Context) {\n%(code)s\n\t_ = client\n\t_ = err\n}\n\nfunc main() {}\n',
    },
    "session_stmts": {
        "extra": ['\t"context"\n', '\t"github.com/makkalot/caretz"\n\t"github.com/makkalot/caretz/lib/llm"\n'],
        "body": 'var _ = context.Background\nvar _ []llm.Message\n\nfunc run(ctx context.Context, client *caretz.Client) error {\n%(code)s\n\t_ = messages\n\treturn nil\n}\n\nfunc main() {}\n',
    },
    "audit_stmts": {
        "extra": ['\t"context"\n', '\t"github.com/makkalot/caretz"\n\t"github.com/makkalot/caretz/lib/eventing"\n\t"github.com/makkalot/caretz/lib/types"\n'],
        "body": 'var _ = context.Background\nvar _ *types.Event\nvar _ = eventing.SubagentInvocationProgress\n\nfunc run(ctx context.Context, session *caretz.Session) error {\n%(code)s\n\treturn err\n}\n\nfunc main() {}\n',
    },
}


def indent(code, n="\t"):
    return "\n".join(n + l if l.strip() else l for l in code.split("\n"))


def write_program(dst, code):
    open(os.path.join(dst, "main.go"), "w").write(code)


def write_wrapped(dst, kind, code):
    w = WRAPPERS[kind]
    src = HDR % {"extra_imports": "".join(w["extra"])}
    body = indent(code.rstrip())
    if kind == "clientopts":
        # struct-literal field fragment: verbatim, just ensure a trailing comma
        if not body.rstrip().endswith(","):
            body = body.rstrip() + ","
    src += w["body"] % {"code": body}
    open(os.path.join(dst, "main.go"), "w").write(src)


def main():
    # GOTOOLCHAIN stays on its auto default so an older go on PATH can still
    # satisfy the go.mod toolchain line; GOPROXY is left at its default so a
    # fresh checkout (CI) can download missing modules.
    env = dict(os.environ, GOFLAGS="-mod=mod", GOCACHE=os.path.join(ROOT, ".doccheck", "gocache"))
    results = []
    for fn in sorted(os.listdir(BLOCKS)):
        if not fn.endswith(".go"):
            continue
        name = fn[:-3]
        kind = KINDS.get(name)
        if kind is None:
            head = open(os.path.join(BLOCKS, fn)).readline()
            kind = "program" if head.startswith("package ") else "UNKNOWN"
        if kind == "UNKNOWN":
            print("SKIP (no wrapper registered):", name)
            continue
        dst = os.path.join(WORK, name)
        shutil.rmtree(dst, ignore_errors=True)
        os.makedirs(dst)
        shutil.copy(os.path.join(WORK, "go.mod"), dst)
        shutil.copy(os.path.join(WORK, "go.sum"), dst)
        code = open(os.path.join(BLOCKS, fn)).read()
        if kind == "program":
            write_program(dst, code)
        else:
            write_wrapped(dst, kind, code)
        p = subprocess.run([GOBIN, "build", "./..."], cwd=dst, env=env,
                           capture_output=True, text=True)
        ok = p.returncode == 0
        results.append((name, kind, ok, (p.stderr or p.stdout).strip()))

    width = max(len(n) for n, _, _, _ in results)
    for name, kind, ok, out in results:
        print("  %s  %-*s  (%s)" % ("PASS" if ok else "FAIL", width, name, kind))
        if not ok:
            for line in out.split("\n"):
                print("        " + line)
    fails = [r for r in results if not r[2]]
    print("\n%d pass, %d fail" % (len(results) - len(fails), len(fails)))
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
