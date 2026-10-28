#!/usr/bin/env bash

set -euo pipefail

usage() {
	cat >&2 <<'EOF'
Usage: scripts/worktree.sh <new|from-branch|from-remote>

Environment variables:
  NAME           Short name for a new feat/<name> branch
  BRANCH         Remote branch name, without the origin/ prefix
  BASE           Starting ref for a new branch (default: main)
  WORKTREE_ROOT  Destination root (default: sibling caretz-worktrees directory)
  YES=1          Skip the final confirmation prompt
EOF
}

die() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

prompt_value() {
	local variable_name=$1
	local prompt=$2
	local value

	if [[ ! -t 0 ]]; then
		die "$variable_name is required when input is not interactive"
	fi
	printf '%s: ' "$prompt" >&2
	IFS= read -r value
	[[ -n "$value" ]] || die "$variable_name cannot be empty"
	printf '%s' "$value"
}

confirm() {
	local answer

	if [[ "${YES:-}" == "1" ]]; then
		return
	fi
	if [[ ! -t 0 ]]; then
		die "confirmation requires an interactive terminal; set YES=1 to confirm"
	fi
	printf 'Create this worktree? [y/N] ' >&2
	IFS= read -r answer
	case "$answer" in
		y|Y|yes|YES) ;;
		*) printf 'Cancelled.\n' >&2; exit 0 ;;
	esac
}

validate_branch_name() {
	git check-ref-format --branch "$1" >/dev/null 2>&1 || die "invalid branch name: $1"
}

validate_short_name() {
	local name=$1
	[[ "$name" != */* ]] || die "NAME must be a single path component"
	[[ "$name" != "." && "$name" != ".." ]] || die "invalid NAME: $name"
	validate_branch_name "feat/$name"
}

branch_exists() {
	git show-ref --verify --quiet "refs/heads/$1"
}

ensure_destination_available() {
	local destination=$1
	local registered

	[[ ! -e "$destination" ]] || die "destination already exists: $destination"
	registered=$(git worktree list --porcelain | sed -n 's/^worktree //p')
	if printf '%s\n' "$registered" | grep -F -x -q -- "$destination"; then
		die "destination is already registered as a worktree: $destination"
	fi
}

branch_worktree_path() {
	local wanted="refs/heads/$1"
	awk -v wanted="$wanted" '
		$1 == "worktree" { path = substr($0, 10) }
		$1 == "branch" && $2 == wanted { print path; exit }
	' < <(git worktree list --porcelain)
}

print_plan() {
	printf '\nWorktree plan\n'
	printf '  Source:      %s\n' "$1"
	printf '  Branch:      %s\n' "$2"
	printf '  Destination: %s\n\n' "$3"
}

[[ $# -eq 1 ]] || { usage; exit 2; }
mode=$1

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "run this command inside a Git worktree"
primary_worktree=$(git worktree list --porcelain | sed -n '1s/^worktree //p')
[[ -n "$primary_worktree" ]] || die "could not determine the primary worktree"

worktree_root=${WORKTREE_ROOT:-"$(dirname "$primary_worktree")/caretz-worktrees"}
if [[ "$worktree_root" != /* ]]; then
	worktree_root="$(git rev-parse --show-toplevel)/$worktree_root"
fi

case "$mode" in
	new)
		name=${NAME:-}
		[[ -n "$name" ]] || name=$(prompt_value NAME "Short worktree name")
		validate_short_name "$name"
		branch="feat/$name"
		base=${BASE:-main}
		git rev-parse --verify --quiet "${base}^{commit}" >/dev/null || die "starting ref does not exist: $base"
		branch_exists "$branch" && die "local branch already exists: $branch"
		destination="$worktree_root/$name"
		ensure_destination_available "$destination"
		print_plan "$base" "$branch" "$destination"
		confirm
		mkdir -p "$worktree_root"
		git worktree add -b "$branch" "$destination" "$base"
		;;
	from-branch)
		branch=$(git symbolic-ref --quiet --short HEAD) || die "cannot move a detached HEAD into a worktree"
		[[ "$branch" != "main" ]] || die "main cannot be moved into a feature worktree"
		[[ -z "$(git status --porcelain)" ]] || die "the current worktree must be completely clean"
		git show-ref --verify --quiet refs/heads/main || die "local main branch does not exist"
		main_worktree=$(branch_worktree_path main)
		current_worktree=$(git rev-parse --show-toplevel)
		if [[ -n "$main_worktree" && "$main_worktree" != "$current_worktree" ]]; then
			die "main is already checked out at $main_worktree"
		fi
		name=${branch##*/}
		destination="$worktree_root/$name"
		ensure_destination_available "$destination"
		print_plan "current branch (switch this checkout to main)" "$branch" "$destination"
		confirm
		mkdir -p "$worktree_root"
		git switch main
		git worktree add "$destination" "$branch"
		;;
	from-remote)
		branch=${BRANCH:-}
		[[ -n "$branch" ]] || branch=$(prompt_value BRANCH "Remote branch (without origin/)")
		[[ "$branch" != origin/* ]] || die "BRANCH must not include the origin/ prefix"
		validate_branch_name "$branch"
		branch_exists "$branch" && die "local branch already exists: $branch"
		name=${branch##*/}
		destination="$worktree_root/$name"
		ensure_destination_available "$destination"
		print_plan "origin/$branch" "$branch" "$destination"
		confirm
		mkdir -p "$worktree_root"
		git fetch origin "+refs/heads/$branch:refs/remotes/origin/$branch"
		git worktree add --track -b "$branch" "$destination" "origin/$branch"
		;;
	*)
		usage
		exit 2
		;;
esac

printf '\nWorktree ready: %s\n' "$destination"
