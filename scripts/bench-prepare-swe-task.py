#!/usr/bin/env python3
import argparse
import subprocess
from pathlib import Path

from datasets import load_dataset


DEFAULT_INSTANCE = "sympy__sympy-20590"
DEFAULT_DATASET = "SWE-bench/SWE-bench_Lite"
DEFAULT_SPLIT = "test"


def run(args, cwd=None):
    subprocess.run(args, cwd=cwd, check=True)


def main():
    parser = argparse.ArgumentParser(description="Prepare one local SWE-bench workspace for CARETZ bench smoke runs.")
    parser.add_argument("--instance-id", default=DEFAULT_INSTANCE)
    parser.add_argument("--dataset-name", default=DEFAULT_DATASET)
    parser.add_argument("--split", default=DEFAULT_SPLIT)
    parser.add_argument("--output-root", default="bench/workspaces/swebench")
    parser.add_argument("--prompt-root", default="bench/tasks/swebench")
    args = parser.parse_args()

    dataset = load_dataset(args.dataset_name, split=args.split)
    matches = [row for row in dataset if row["instance_id"] == args.instance_id]
    if not matches:
        raise SystemExit(f"instance {args.instance_id!r} not found in {args.dataset_name}:{args.split}")
    instance = matches[0]

    workspace_root = Path(args.output_root) / args.instance_id
    repo_dir = workspace_root / "repo"
    prompt_dir = Path(args.prompt_root)
    prompt_path = prompt_dir / f"{args.instance_id}.md"
    workspace_root.mkdir(parents=True, exist_ok=True)
    prompt_dir.mkdir(parents=True, exist_ok=True)

    repo_url = f"https://github.com/{instance['repo']}.git"
    if not (repo_dir / ".git").exists():
        run(["git", "clone", repo_url, str(repo_dir)])
    else:
        run(["git", "fetch", "--all", "--prune"], cwd=repo_dir)

    run(["git", "checkout", "--force", instance["base_commit"]], cwd=repo_dir)
    run(["git", "clean", "-fdx"], cwd=repo_dir)

    prompt = f"""We need solve this SWE-bench task in the repository at the current working directory.

Instance: {instance['instance_id']}
Repository: {instance['repo']}
Base commit: {instance['base_commit']}

Rules:
- Edit the repository files to fix the issue.
- Do not create a patch file manually; the benchmark runner will capture git diff.
- Run relevant tests if practical.
- When done, provide a short final summary.

Issue:
{instance['problem_statement']}
"""
    prompt_path.write_text(prompt)

    print(f"Prepared workspace: {repo_dir}")
    print(f"Prepared prompt:    {prompt_path}")
    print()
    print("Run:")
    print("  make build build-bench")
    print("  ./bin/bench run --model qwen3627b-mtp-code --suite swe-one --caretz-bin ./bin/caretz")


if __name__ == "__main__":
    main()
