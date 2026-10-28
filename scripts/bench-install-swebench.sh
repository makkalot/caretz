#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BENCH_DIR="${ROOT_DIR}/.bench"
VENV_DIR="${BENCH_DIR}/swebench-venv"
SRC_DIR="${BENCH_DIR}/SWE-bench"
DOCKERFILES_DIR="${BENCH_DIR}/swe-bench-dockerfiles"

mkdir -p "${BENCH_DIR}"

if [ ! -d "${VENV_DIR}" ]; then
  python3 -m venv "${VENV_DIR}"
fi

"${VENV_DIR}/bin/python" -m pip install --upgrade pip wheel setuptools

if [ ! -d "${SRC_DIR}/.git" ]; then
  git clone https://github.com/SWE-bench/SWE-bench.git "${SRC_DIR}"
else
  git -C "${SRC_DIR}" pull --ff-only
fi

if [ ! -d "${DOCKERFILES_DIR}/.git" ]; then
  git clone https://github.com/SWE-bench/swe-bench-dockerfiles.git "${DOCKERFILES_DIR}"
else
  git -C "${DOCKERFILES_DIR}" pull --ff-only
fi

"${VENV_DIR}/bin/python" -m pip install -e "${SRC_DIR}"
"${VENV_DIR}/bin/python" -m pip install datasets

cat <<EOF
SWE-bench tooling installed.

Use this Python for helper scripts:
  ${VENV_DIR}/bin/python

Before running official evaluation, make sure Docker is installed and running.
EOF
