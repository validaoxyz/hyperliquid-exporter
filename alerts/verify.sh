#!/bin/sh

set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repository_root"

go test ./alerts

if ! command -v promtool >/dev/null 2>&1; then
	echo "promtool is not installed; static contract checks passed, but PromQL parsing and alert fixture evaluation still require promtool 3.8.1" >&2
	exit 2
fi

promtool_version=$(promtool --version 2>&1 | sed -n '1p')
case "$promtool_version" in
	*"version 3.8.1"*) ;;
	*)
		echo "promtool 3.8.1 is required; found: $promtool_version" >&2
		exit 2
		;;
esac

promtool check rules alerts/*.rules.yml
promtool test rules alerts/tests/*.test.yml
