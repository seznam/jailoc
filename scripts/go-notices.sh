#!/usr/bin/env bash
# Print the license files of the Go standard library and of every module that
# the given packages link in on the given platforms, for third-party notices.
#
# Usage: go-notices.sh MODULE_DIR "GOOS/GOARCH ..." PACKAGE...
#
# CGO_ENABLED is taken from the environment and must match the build.
set -euo pipefail

if [ $# -lt 3 ]; then
	echo "usage: $0 MODULE_DIR \"GOOS/GOARCH ...\" PACKAGE..." >&2
	exit 2
fi
dir=$1 platforms=$2
shift 2
cd "$dir"

format='{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{with .Replace}} => {{.Path}} {{.Version}}{{end}}	{{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}'
modules=$(for platform in $platforms; do
	GOOS=${platform%/*} GOARCH=${platform#*/} go list -deps -f "$format" "$@"
done | sed '/^$/d' | sort -u)

notice() {
	local name=$1 moddir=$2 found=0 file
	for file in "$moddir"/*; do
		case $(basename "$file" | tr '[:upper:]' '[:lower:]') in
		licen[cs]e* | copying* | notice* | patents*) ;;
		*) continue ;;
		esac
		[ -f "$file" ] || continue
		found=1
		printf '%s\n%s (%s)\n%s\n\n' "================================================================================" \
			"$name" "$(basename "$file")" "--------------------------------------------------------------------------------"
		cat "$file"
		printf '\n'
	done
	if [ "$found" = 0 ]; then
		echo "no license file found for $name in $moddir" >&2
		return 1
	fi
}

notice "Go standard library $(go env GOVERSION)" "$(go env GOROOT)"
while IFS=$'\t' read -r name moddir; do
	notice "$name" "$moddir"
done <<<"$modules"
