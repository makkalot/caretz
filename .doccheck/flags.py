#!/usr/bin/env python3
"""Extract every flag name defined in the Go source, grouped by function."""
import os, re, json, sys
pat = re.compile(r'\.(String|Bool|Int|Uint64|Float64|Duration)Var\(&[^,]+,\s*"([a-z0-9-]+)"')
pat2 = re.compile(r'\.(String|Bool|Int|Uint64|Float64|Duration|Var)\("([a-z0-9-]+)"')
res = {}
for root, dirs, files in os.walk("internal"):
    for fn in files:
        if not fn.endswith(".go") or fn.endswith("_test.go"): continue
        p = os.path.join(root, fn)
        src = open(p).read()
        # split into funcs
        cur = "<module>"
        for line in src.split("\n"):
            m = re.match(r'func (\w+)', line)
            if m: cur = m.group(1)
            for mm in pat.finditer(line) or []:
                res.setdefault(cur, set()).add(mm.group(2))
            for mm in pat2.finditer(line):
                res.setdefault(cur, set()).add(mm.group(2))
for k in sorted(res):
    print("%-40s %s" % (k, " ".join(sorted(res[k]))))
json.dump({k: sorted(v) for k,v in res.items()}, open(".doccheck/flags.json","w"), indent=1)
