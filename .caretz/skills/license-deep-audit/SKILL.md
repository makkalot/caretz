---
name: license-deep-audit
description: Full due-diligence license verification of a dependency tree — do NOT trust package metadata; verify every package's license from its actual LICENSE file at the pinned version, then recurse into each dependency's own dependencies (transitive closure) and verify those too, producing an evidence trail. Also covers repos cloned/built from source and ML model weights. Use this skill when the user asks to "verify" licenses, wants due diligence, an evidence-backed audit, asks how you actually know a package's license, wants transitive/deps-of-deps checked, mentions legal review or acquisition/compliance prep, or when a quick license-audit triage found UNKNOWNs or suspicious metadata that needs confirmation. For a fast first-pass scan, use the license-audit skill instead.
---

# License Deep Audit

Verify — not just collect — the license of every node in a dependency tree, with an evidence trail for each verdict. This is the slow, thorough counterpart to the `license-audit` triage skill. If a triage report already exists in the conversation, start from its inventory instead of re-scanning.

Severity classification (🔴🟠🟡🟢, context rules, license categories) is the same as in `license-audit`'s `references/license-matrix.md` — read that skill's matrix if available; otherwise apply standard SPDX category knowledge. This skill is about **how you know**, not how you classify.

## The trust hierarchy

The core principle: **metadata is a claim; the LICENSE file at the pinned revision is the fact.** For every package, establish the license from the highest-ranking source you can reach:

1. **LICENSE/COPYING file in the source repo at the exact released tag/commit** of the pinned version — ground truth
2. `license_expression` (SPDX) field in package metadata — good, but verify for anything non-permissive or important
3. Trove classifiers (`License :: OSI Approved :: ...`) — decent signal
4. Free-text `license` field — weakest; often stale, truncated ("LICENSE.txt"), or wrong

Record for every package **which level you reached**. When two levels disagree (metadata says MIT, repo LICENSE says GPL), the repo file wins and the mismatch itself is a 🟠 finding — misdeclared licenses are exactly what deep audits exist to catch.

Practical verification of a LICENSE file: fetch it at the version tag (`https://raw.githubusercontent.com/<org>/<repo>/<tag>/LICENSE`, trying `v<version>`, `<version>`, and the variants `LICENSE.md`, `LICENSE.txt`, `COPYING`, `LICENCE`); identify the license from its distinctive opening lines and clauses, not the filename. Check for riders appended below the main text (Commons Clause lives *under* an Apache/MIT header). If the tag can't be found, fall back to the default branch and note the weaker evidence.

## Building the transitive closure

"Package X is MIT" is not enough — X's own dependencies ship with it. Enumerate the full tree:

**Python**
- If a lockfile exists (`poetry.lock`, `uv.lock`, `Pipfile.lock`, `pdm.lock`): the closure is already enumerated — every entry is a node to verify. This is the best case.
- If only requirements/pyproject: resolve first. Options in order of preference: `pip install --dry-run --report report.json` (gives the full resolved set without installing), `pip download`, or BFS over the PyPI JSON API's `requires_dist` (use `scripts/pypi_tree.py`; note API-based resolution follows declared ranges, not a real resolver — say so in the report).
- Mind extras and environment markers: only include deps active for the target platform/extras actually used.

**Go**
- `go mod graph` gives the full module graph; `go list -m all` the resolved versions. `go.sum` alone over-approximates (contains modules not in the build) — prefer `go list`. Note `replace` directives: the *replacement* is what ships, verify that repo.

**Depth policy**: for package-manager deps, there is no depth limit — the lockfile/graph closure is finite and every node gets verified. Depth budgeting only applies to source-cloned repos (see `references/source-clones-and-ml.md`). Deduplicate: verify each (package, version) once.

**Prioritized effort**: full LICENSE-file verification of a 300-node tree may be impractical in one session. If you must sample, verify from the top of this risk order and disclose coverage honestly:
1. Everything flagged non-🟢 by metadata, all UNKNOWNs, all metadata mismatches
2. Copyleft-family packages and anything from the known-gotcha lists
3. Small/obscure packages (< ~100 stars, single-author, research code) — misdeclaration lives here
4. Big-name permissive infrastructure (numpy, requests, golang.org/x/*) — metadata level is acceptable evidence here; say so

## Recursion beyond package managers

Read `references/source-clones-and-ml.md` when the repo (or any dependency you expand) clones other repos, builds from source, uses git submodules or VCS-pinned deps, or downloads ML model weights — it covers the recursion procedure, depth budget, build-vs-shipped distinction, and the weights-license matrix (Llama, RAIL, research-only, etc.). Weights are audited *in addition to* code: the code license never covers the weights.

## Parallelizing with subagents

If the environment supports subagents, shard the verification instead of doing it inline. Prompts for each role live in `agents/` — read the relevant file before dispatching, pass its content as the subagent's instructions, and give each subagent only its slice of the inventory plus the usage context (commercial? distribution model?).

**Sharding units:**
- `agents/package-verifier.md` — batches of **10–20 (package, version) nodes** per subagent, not one per package (spawn overhead dominates below that). Group batches by risk tier so the highest-priority batch dispatches first and its results are never blocked behind bulk-green verification.
- `agents/repo-auditor.md` — **one subagent per cloned repo** (a repo is a natural, variable-sized unit). Its output includes `folded_deps` — queue those into new package-verifier batches — and `nested_clones` — decide per item whether to dispatch another repo-auditor (depth budget from the reference file applies).
- `agents/model-checker.md` — one subagent per **batch of model references** (they're cheap; 10–30 per batch).

**Orchestrator loop:**
1. Build the inventory (Step 1 / transitive closure) inline — sharding needs the full node list first.
2. Dispatch all first-wave subagents in the same turn so they run concurrently.
3. Merge returned JSON into one results set; dispatch second-wave batches for `folded_deps`/`nested_clones` discovered by repo auditors.
4. Run `agents/audit-validator.md` on {inventory + merged results}. On FAIL, re-dispatch only the listed gaps — don't rerun completed work.
5. Apply final context-dependent verdicts yourself (subagents return provisional verdicts assuming strict commercial use) and write the report.

Subagent results are evidence, not conclusions: the orchestrator owns severity classification, the "via" chains, and the coverage statement. If subagents are unavailable, do the same work inline in the same priority order — the sharding plan doubles as a to-do list.

## Evidence trail and report

Every verdict must be reproducible. Structure the report:

```
# Deep License Audit — <repo>
**Context:** <commercial? distribution model?>  ·  **Tree size:** <N> unique (package, version) nodes
**Verification coverage:** <X> verified at LICENSE-file level, <Y> at metadata level, <Z> unresolved
<if sampled: exactly what was and wasn't verified, and by what rule>

## 🔴 / 🟠 Findings
| Package | Version | Direct or via | License (verified) | Evidence | Why flagged | Suggested action |
<"via" = the direct dep that pulls it in — people need to know what to remove/replace>

## ⚠️ Metadata mismatches
| Package | Metadata claims | LICENSE file says | Evidence URLs |

## 🔬 Cloned / built-from-source  (if any — see reference)
## 🧠 Models & weights            (if any — see reference)

## Full evidence table
| Package | Version | License | Evidence level (1-4) | Source URL/method |
<every node; this table is the artifact a lawyer or acquirer will want — offer to
 save it as CSV/JSON alongside the report>

*Automated due-diligence support, not legal advice.*
```

The "via" column matters: a flagged transitive dep is actionable only if you know which direct dependency drags it in (`pipdeptree --reverse`, `go mod why <module>`, or invert the graph you built).

## Pitfalls specific to deep audits

- Verify at the **pinned version's tag**, not HEAD — relicensing (BUSL switches, GPL→MIT and vice versa) makes HEAD actively misleading in both directions.
- A repo-root LICENSE doesn't always cover everything: check for per-directory licenses (`third_party/`, differently-licensed subpackages in monorepos — the PyPI package may be built from one subdirectory).
- PyPI packages can bundle code absent from the source repo (generated code, vendored wheels' binary deps like statically-linked openssl in manylinux wheels). For critical audits, note that wheel *contents* can exceed repo contents.
- `requires_dist` reflects the version you queried — an old pinned version may have different deps than the latest metadata shown on the package page.
- Yanked or deleted packages: if a pinned version vanished from the index, that's a supply-chain finding of its own.
