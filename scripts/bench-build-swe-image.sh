#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PYTHON="${SWEBENCH_PYTHON:-${ROOT_DIR}/.bench/swebench-venv/bin/python}"
INSTANCE_ID="${1:-sympy__sympy-20590}"
DATASET_NAME="${SWEBENCH_DATASET_NAME:-SWE-bench/SWE-bench_Lite}"
SPLIT="${SWEBENCH_SPLIT:-test}"
DOCKERFILE_REPO="${SWEBENCH_DOCKERFILE_REPO:-${ROOT_DIR}/.bench/swe-bench-dockerfiles}"

if [ ! -d "${DOCKERFILE_REPO}/src/dockerfiles" ]; then
  cat >&2 <<EOF
SWE-bench image prebuild needs a dockerfile repo containing:
  src/dockerfiles/*.Dockerfile

The installed SWE-bench version can usually skip this step and pull the
published Docker image during evaluation. Run:

  ./bin/bench run --model qwen3627b-mtp-code --suite swe-one --caretz-bin ./bin/caretz

If you specifically need local image builds, set SWEBENCH_DOCKERFILE_REPO to a
checkout that contains src/dockerfiles.

Current SWEBENCH_DOCKERFILE_REPO:
  ${DOCKERFILE_REPO}
EOF
  exit 2
fi

"${PYTHON}" -m swebench.image_builder.prepare_images \
  --dataset_name "${DATASET_NAME}" \
  --split "${SPLIT}" \
  --instance_ids "${INSTANCE_ID}" \
  --max_workers 1 \
  --dockerfile_repo "${DOCKERFILE_REPO}"
