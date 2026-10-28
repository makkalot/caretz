import os, re, json, sys

def doc_files():
    """Every Markdown page under docs/, including section folders."""
    out = []
    for root, _, names in os.walk("docs"):
        out += [os.path.join(root, n) for n in names if n.endswith(".md")]
    return sorted(out)

files = ["README.md"] + doc_files() + \
        ["AGENTS.md", "site/README.md", "site/content/_index.md", "site/content/docs/_index.md"]

fence = re.compile(r'^```(\w*)\s*$')
# Clear previous extraction so doc blocks that were deleted stop being checked.
os.makedirs(".doccheck/blocks", exist_ok=True)
for stale in os.listdir(".doccheck/blocks"):
    os.remove(os.path.join(".doccheck/blocks", stale))
inv = []
for fp in files:
    lines = open(fp).read().split("\n")
    i = 0
    while i < len(lines):
        m = fence.match(lines[i])
        if m:
            lang = m.group(1)
            start = i+1
            j = i+1
            while j < len(lines) and not lines[j].startswith("```"):
                j += 1
            body = "\n".join(lines[start:j])
            inv.append({"file": fp, "line": start+1, "lang": lang, "nlines": j-start, "block": len([x for x in inv if x["file"]==fp])+1})
            fn = ".doccheck/blocks/%s__b%02d.%s" % (fp.replace("/","_").replace(".md",""), len([x for x in inv if x["file"]==fp])+1, lang or "txt")
            os.makedirs(".doccheck/blocks", exist_ok=True)
            open(fn,"w").write(body+"\n")
            inv[-1]["path"] = fn
            i = j+1
        else:
            i += 1
json.dump(inv, open(".doccheck/inventory.json","w"), indent=1)
from collections import Counter
print(Counter(b["lang"] for b in inv))
print("total blocks:", len(inv))
for b in inv:
    if b["lang"] in ("go","json","toml","yaml"):
        print(b["lang"], b["file"], "L%d"%b["line"], b["nlines"], "lines ->", b["path"])
