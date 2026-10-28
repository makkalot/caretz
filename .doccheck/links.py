#!/usr/bin/env python3
"""Check relative links + anchors in README, docs/, and Hugo content."""
import os, re, sys, unicodedata

DOCS = sorted(os.path.join(r, n) for r, _, ns in os.walk("docs") for n in ns if n.endswith(".md"))
FILES = ["README.md", "AGENTS.md"] + DOCS + \
        ["site/content/_index.md", "site/content/docs/_index.md"] + \
        ["site/layouts/index.html"]

def anchors(text):
    """GitHub-style anchors for headings."""
    out = set()
    in_fence = False
    for line in text.split("\n"):
        if line.startswith("```"):
            in_fence = not in_fence; continue
        if in_fence: continue
        m = re.match(r'^(#{1,6})\s+(.*)$', line)
        if m:
            h = m.group(2).strip()
            h = re.sub(r'`', '', h)
            h = re.sub(r'\[([^\]]+)\]\([^)]*\)', r'\1', h)
            a = h.lower()
            a = ''.join(c for c in a if c.isalnum() or c in ' -_')
            a = a.replace(' ', '-')
            out.add(a)
    return out

info = {}
for f in FILES:
    if not os.path.exists(f): continue
    t = open(f).read()
    info[f] = anchors(t)

bad = []
LINK = re.compile(r'\[([^\]]*)\]\(([^)\s]+)\)')
for f in FILES:
    if not os.path.exists(f): continue
    text = open(f).read()
    d = os.path.dirname(f)
    for n, line in enumerate(text.split("\n"), 1):
        for _, target in LINK.findall(line):
            if target.startswith(("http://", "https://", "mailto:", "#")):
                continue
            path, _, anchor = target.partition("#")
            if path:
                # Hugo page-relative links (site/content, trailing slash) resolve
                # against generated output under site/public, not the filesystem.
                if f.startswith("site/content") and path.endswith("/"):
                    gen = os.path.join("site", "public", d.replace("site/content", "").strip("/"), path)
                    if os.path.exists(os.path.join(gen, "index.html")) or os.path.exists(gen.rstrip("/") + ".html"):
                        continue
                cand = os.path.normpath(os.path.join(d, path))
                cand_bs = os.path.normpath(os.path.join(d, path.rstrip("/")))
                if not (os.path.exists(cand) or os.path.exists(cand_bs) or
                        os.path.exists(cand + ".md") or os.path.exists(os.path.join(cand_bs, "_index.md"))):
                    bad.append((f, n, "missing target: %s" % target))
                    continue
                tgt = cand if cand.endswith(".md") else cand_bs + ".md"
                if os.path.isdir(cand_bs):
                    tgt = os.path.join(cand_bs, "_index.md")
                    if not os.path.exists(tgt):
                        tgt = os.path.join(cand_bs, "index.md")
                if anchor and os.path.exists(tgt):
                    if anchor not in info.get(tgt, set()) and os.path.exists(tgt):
                        a2 = anchors(open(tgt).read())
                        if anchor not in a2:
                            bad.append((f, n, "missing anchor #%s in %s" % (anchor, tgt)))
            else:
                tgt = f
                if anchor and anchor not in info.get(tgt, set()):
                    bad.append((f, n, "missing local anchor #%s" % anchor))

# Hugo internal relURL refs
for f in ["site/hugo.yaml"] + [p for p in ["site/layouts/index.html","site/layouts/partials/header.html","site/layouts/partials/footer.html"] if os.path.exists(p)]:
    for n, line in enumerate(open(f).read().split("\n"), 1):
        for m in re.finditer(r'relURL\)\s*"(/[^"]*)"' , line):
            pass
        for m in re.finditer(r'href=\{\{\s*"(/[^"]*)"\s*\|\s*relURL', line):
            ref = m.group(1).strip("/")
            if not ref: continue
            base = os.path.join("site", "public", ref)
            if not (os.path.exists(base) or os.path.exists(base + ".html") or os.path.isdir(base)):
                bad.append((f, n, " Hugo ref has no generated page: %s" % m.group(1)))

for f, n, msg in bad:
    print("  %s:%d  %s" % (f, n, msg))
print("\n%d link issue(s)" % len(bad))
sys.exit(1 if bad else 0)
