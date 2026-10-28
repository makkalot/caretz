#!/bin/bash

retry() {
  local attempts="$1"
  shift
  local delay=2
  local n=1
  until "$@"; do
    if [ "$n" -ge "$attempts" ]; then
      return 1
    fi
    rm -rf /var/lib/apt/lists/*
    sleep "$delay"
    n=$((n + 1))
    delay=$((delay * 2))
  done
}

apt_install() {
  local attempts=3
  local delay=2
  local n=1
  until [ "$n" -gt "$attempts" ]; do
    apt-get clean
    rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/partial/*
    if apt-get update &&
      DEBIAN_FRONTEND=noninteractive apt-get install -y --fix-missing "$@"; then
      return 0
    fi
    DEBIAN_FRONTEND=noninteractive apt-get -f install -y --fix-missing || true
    sleep "$delay"
    n=$((n + 1))
    delay=$((delay * 2))
  done
  return 1
}

# Install curl. Debian mirrors can briefly serve mismatched package indexes and
# archives, so retry after clearing apt's indexes/cache and repairing partial
# dpkg state.
if ! curl --version >/dev/null 2>&1; then
  apt_install curl
fi

# Install uv
if ! uvx --version >/dev/null 2>&1; then
  retry 3 sh -c 'curl -LsSf https://astral.sh/uv/0.9.5/install.sh | sh'
fi

source $HOME/.local/bin/env

# Check if we're in a valid working directory
if [ "$PWD" = "/" ]; then
    echo "Error: No working directory set. Please set a WORKDIR in your Dockerfile before running this script."
    exit 1
fi

uvx \
  -p 3.13 \
  -w pytest==8.4.1 \
  -w pytest-json-ctrf==0.3.5 \
  pytest --ctrf /logs/verifier/ctrf.json /tests/test_outputs.py -rA


if [ $? -eq 0 ]; then
  echo 1 > /logs/verifier/reward.txt
else
  echo 0 > /logs/verifier/reward.txt
fi
