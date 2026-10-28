# Deep Audit — cloned repos, build-from-source, and ML artifacts

Read this when the repo pulls in code or artifacts *outside* its package managers. Common in ML repos.

## Triggers — scan for these in Step 1

- `git clone` in Dockerfiles, Makefiles, `setup.sh`/`build.sh`-style scripts, CI configs
- `.gitmodules` (git submodules)
- VCS dependencies in manifests: `pip install git+https://...`, `package @ git+...` in requirements/pyproject, `replace` directives in go.mod pointing at forks
- Source builds: `cmake`, `make install`, `python setup.py build_ext`, compiling CUDA kernels, `./configure` inside RUN lines
- **Runtime artifact downloads**: `from_pretrained(...)` (HuggingFace), `torch.hub.load(...)`, `snapshot_download`, `wget`/`curl` of `*.pt`, `*.pth`, `*.onnx`, `*.safetensors`, `*.gguf`, `*.bin` weights, dataset downloads

If any trigger is present, extend the audit as below and add a **Coverage** line to the report.

## Recursion procedure for cloned repos / submodules

Treat each cloned repo as a sub-project:

1. **Resolve the exact revision** being cloned — a pinned tag/commit if the script checks one out, otherwise note it clones a moving branch (flag that: license can change under you; recommend pinning).
2. **Fetch its LICENSE** at that revision (raw.githubusercontent.com for GitHub) — this is the license of the cloned code itself.
3. **Fetch its manifests** (requirements*/pyproject/setup.py/go.mod) and fold its *runtime* dependencies into the main inventory, tagged with provenance (`via clone: <repo>`), since building/installing it makes them your dependencies too.
4. **Check its `third_party/`, `vendor/`, `csrc/`, `external/` dirs** — ML repos habitually vendor CUDA kernels, tokenizers, and single-file utilities with their own headers. Read license headers on anything that gets compiled into the artifact.
5. **Recurse at most one more level** (depth 2 total) and only into things that reach the final artifact. If a cloned repo itself clones things, list those at depth 2 without expanding further unless something looks alarming.

**Prioritization**: don't expand well-known permissive infrastructure (e.g., cloning `pybind11`, `googletest`). Spend the depth budget on unknown/small/research repos — those are where "research use only" headers and missing licenses live.

**Build-only vs shipped**: a repo cloned in a builder stage purely to produce a binary that ships → the *produced binary's* license terms still apply (compiling doesn't launder a license); a repo cloned only to run tests → dev-tier.

Static linking caveat: building C/C++/CUDA from a cloned GPL/LGPL repo and linking it into your extension or Go binary creates a combined work — classify like a static-linked library, not mere aggregation.

## ML artifacts: weights, models, datasets

**The code license and the weights license are independent.** Audit both.

1. Identify every model the repo downloads or bundles: `from_pretrained("org/model")` IDs, torch.hub URLs, hardcoded weight URLs, checkpoint files committed via git-lfs.
2. Resolve each model's license from its model card (HuggingFace pages state it in metadata) or release page.
3. Classify with extra categories beyond the standard matrix:

| Weights license | Verdict for commercial use |
|---|---|
| Apache-2.0, MIT, BSD weights | 🟢 |
| OpenRAIL / BigScience RAIL variants | 🟠 — commercial OK but use-restriction clauses (prohibited-use lists) legally bind; some orgs reject any RAIL |
| Llama 2/3 Community License | 🟠 — commercial OK below 700M MAU threshold, attribution + acceptable-use policy required; redistribution restrictions on derivatives naming |
| Gemma Terms of Use | 🟠 — commercial OK with use restrictions and flow-down terms |
| CC-BY-NC / research-only weights (many academic releases, some Stable Diffusion variants, NVIDIA research models) | 🔴 for commercial |
| No stated license on weights | 🔴 until resolved — weights are copyrightable subject matter in practice; treat like unlicensed code |

4. **Datasets** pulled at build/train time: only in scope if the repo *redistributes* data or ships models the user trains on it — then flag NC-licensed datasets (many academic benchmarks are CC-BY-NC) as a warning with a note that trained-model implications are legally unsettled; recommend counsel.
5. Runtime downloads deserve a standing note even when licenses are fine: the artifact fetched can change server-side (model cards get relicensed, repos get gated). Recommend pinning revisions (`from_pretrained(..., revision="<commit>")`) and mirroring weights the product depends on.

## Report additions

Add to the report when deep audit ran:

```
## 🔬 Cloned / built-from-source (<count>)
| Source repo | Revision pinned? | Its license | Folded-in deps flagged | Verdict |

## 🧠 Models & weights (<count>)
| Model | Where referenced | Weights license | Verdict |

**Coverage:** audited to depth <N>; expanded <list>; NOT expanded: <list + reason>.
```

The Coverage line is mandatory whenever recursion happened — an audit that silently stopped at depth 1 is worse than one that says where it stopped.
