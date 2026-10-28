#!/usr/bin/env python3
"""BFS the PyPI dependency tree for a set of root packages and collect license
metadata for every node. API-based approximation: follows declared requires_dist
ranges (latest matching or exact pin), skips extras and non-matching env markers.
Use a lockfile's closure instead whenever one exists.

Usage:
  python pypi_tree.py "requests==2.31.0" "flask" --max-depth 10 -o tree.json

Output JSON per node: name, version, license fields (all metadata levels),
project_urls (for LICENSE-file verification), requires_dist, depth, via.
"""
import argparse, json, re, sys, urllib.request

API = "https://pypi.org/pypi/{name}/{ver}/json"
API_LATEST = "https://pypi.org/pypi/{name}/json"

def fetch(name, ver=None):
    url = API.format(name=name, ver=ver) if ver else API_LATEST.format(name=name)
    try:
        with urllib.request.urlopen(url, timeout=15) as r:
            return json.load(r)
    except Exception as e:
        return {"_error": str(e)}

def parse_req(req):
    """'foo[bar]>=1.0; python_version>"3"' -> (name, pin_or_None, has_marker_extra)"""
    marker = None
    if ";" in req:
        req, marker = req.split(";", 1)
    m = re.match(r"^\s*([A-Za-z0-9_.\-]+)", req)
    if not m:
        return None
    name = m.group(1).lower().replace("_", "-")
    pin = None
    em = re.search(r"==\s*([\w.\-+!]+)", req)
    if em:
        pin = em.group(1)
    # skip deps only needed for extras of the parent
    extra_only = bool(marker and "extra ==" in marker)
    return name, pin, extra_only

def norm(name):
    return name.lower().replace("_", "-")

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("roots", nargs="+", help='e.g. "requests==2.31.0" or "flask"')
    ap.add_argument("--max-depth", type=int, default=15)
    ap.add_argument("--include-extras", action="store_true",
                    help="also follow deps gated behind extras")
    ap.add_argument("-o", "--out", default="-")
    args = ap.parse_args()

    queue, seen, nodes = [], set(), {}
    for r in args.roots:
        p = parse_req(r)
        if p:
            queue.append((p[0], p[1], 0, None))

    while queue:
        name, ver, depth, via = queue.pop(0)
        key = (name, ver or "latest")
        if key in seen or depth > args.max_depth:
            continue
        seen.add(key)
        data = fetch(name, ver)
        if "_error" in data:
            nodes[f"{name}=={ver or '?'}"] = {"name": name, "version": ver,
                                              "error": data["_error"], "via": via, "depth": depth}
            continue
        info = data.get("info", {})
        resolved = info.get("version")
        classifiers = [c for c in info.get("classifiers", []) if c.startswith("License")]
        reqs = info.get("requires_dist") or []
        nodes[f"{name}=={resolved}"] = {
            "name": name, "version": resolved, "depth": depth, "via": via,
            "license_expression": info.get("license_expression"),
            "license_field": (info.get("license") or "")[:200],
            "license_classifiers": classifiers,
            "project_urls": info.get("project_urls"),
            "requires_dist": reqs,
        }
        for req in reqs:
            p = parse_req(req)
            if not p:
                continue
            cname, cpin, extra_only = p
            if extra_only and not args.include_extras:
                continue
            queue.append((cname, cpin, depth + 1, f"{name}=={resolved}"))
        print(f"[{len(nodes):>4}] depth={depth} {name}=={resolved}", file=sys.stderr)

    out = json.dumps({"node_count": len(nodes), "nodes": nodes}, indent=2)
    if args.out == "-":
        print(out)
    else:
        with open(args.out, "w") as f:
            f.write(out)
        print(f"wrote {args.out} ({len(nodes)} nodes)", file=sys.stderr)

if __name__ == "__main__":
    main()
