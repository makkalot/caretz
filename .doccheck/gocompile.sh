#!/bin/bash
# Compile every Go doc example. Full programs are compiled as-is; fragments are
# wrapped in the smallest legal host so field/method references get type-checked.
set -u
ROOT=$(pwd)
BASE=$ROOT/.doccheck/gomod
BLOCKS=$ROOT/.doccheck/blocks
PASS=0; FAIL=0
run() { # run <name> <dirsrc>
  local name=$1
  rm -rf "$BASE/$name"; mkdir -p "$BASE/$name"
  cp "$BASE/go.mod" "$BASE/go.sum" "$BASE/$name/" 2>/dev/null
  cp "$BASE/$src"/*.go "$BASE/$name/" 2>/dev/null
  (cd "$BASE/$name" && GOFLAGS=-mod=mod go build ./... > /tmp/$name.log 2>&1)
  if [ $? -eq 0 ]; then echo "  PASS  $name"; PASS=$((PASS+1));
  else echo "  FAIL  $name"; sed 's/^/        /' /tmp/$name.log; FAIL=$((FAIL+1)); fi
}

wrap_lit() { # fragment as struct-literal keys
  local name=$1 src=$2
  mkdir -p "$BASE/$name"
  { echo 'package main'; echo ''; echo 'import "github.com/makkalot/caretz"'; echo '';
    echo 'func main() {'; echo '	var _ = caretz.ClientOptions{'; sed 's/^/	/' "$src"; echo '	}'; echo '}'; } > "$BASE/$name/main.go"
}
wrap_body() { # fragment as statement block needing client/session in scope
  local name=$1 src=$2
  mkdir -p "$BASE/$name"
  { echo 'package main'; echo ''; echo 'import ('; echo '	"context"';
    echo '	"github.com/makkalot/caretz"'; echo '	"github.com/makkalot/caretz/lib/eventing"'; echo '	"github.com/makkalot/caretz/lib/llm"'; echo '	"github.com/makkalot/caretz/lib/types"'; echo ')';
    echo ''; echo 'var _ = context.Background'; echo 'var _ eventing.HandleFunc'; echo 'var _ *types.Event'; echo 'var _ []llm.Message'; echo '';
    echo 'func run(ctx context.Context, client *caretz.Client, session *caretz.Session, event *types.Event) error {';
    echo '	var err error'; echo '	var messages []llm.Message';
    echo '	_ = err; _ = messages; _ = client; _ = session; _ = event';
    sed 's/^/	/' "$src"; echo '	return nil'; echo '}'; } > "$BASE/$name/main.go"
}

for f in "$BLOCKS"/*.go; do
  name=$(basename "$f" .go | tr '-' '_')
  src=$(mktemp -d)
  if head -1 "$f" | grep -q '^package '; then
    mkdir -p "$BASE/$name"; cp "$f" "$BASE/$name/main.go"; cp "$BASE/go.mod" "$BASE/go.sum" "$BASE/$name/" 2>/dev/null
  else
    case "$name" in
      *b10*|*b11*) wrap_body "$name" "$f" ;;
      *b12*) wrap_lit "$name" "$f" ;;
      *) wrap_lit "$name" "$f" ;;
    esac
  fi
  (cd "$BASE/$name" && GOFLAGS=-mod=mod go build ./... > "$BASE/$name.log" 2>&1)
  if [ $? -eq 0 ]; then echo "  PASS  $name"; PASS=$((PASS+1));
  else echo "  FAIL  $name  <-- $(grep -n "" "$f" >/dev/null; grep -rl "" /dev/null 2>/dev/null)"; sed 's/^/        /' "$BASE/$name.log"; FAIL=$((FAIL+1)); fi
done
echo "go examples: $PASS pass, $FAIL fail"
