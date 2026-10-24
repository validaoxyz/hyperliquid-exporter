#!/usr/bin/env bash
set -euo pipefail

source_status=$(git status --porcelain=v1 --untracked-files=all --ignore-submodules=none)
if [[ -n "$source_status" ]]; then
  printf 'Release source tree is not clean:\n%s\n' "$source_status" >&2
  exit 1
fi
