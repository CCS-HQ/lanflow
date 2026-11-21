#!/usr/bin/env bash
# Fails when the TUN helper's code changed since the previous release tag
# but cmd/lanflow-helper/VERSION did not: an installed helper is replaced
# only when its version differs, so an unbumped version would leave the old
# helper running with the new app.
# Usage: scripts/check-helper-version.sh [ref]   (default: HEAD; needs tags)
set -euo pipefail
cd "$(dirname "$0")/.."
REF="${1:-HEAD}"
PREV="$(git describe --tags --abbrev=0 --match 'v*' "${REF}^" 2>/dev/null || true)"
if [ -z "$PREV" ]; then
  echo "no earlier release tag; nothing to compare"
  exit 0
fi
# The helper's own packages, from the build graph (cmd/lanflow-helper and
# what it imports from this module).
paths=()
while read -r dir; do
  paths+=("${dir#"$PWD"/}")
done < <(go list -deps -f '{{if not .Standard}}{{if eq .Module.Path "lanflow"}}{{.Dir}}{{end}}{{end}}' ./cmd/lanflow-helper | sed '/^$/d')
changed="$(git diff --name-only "$PREV" "$REF" -- "${paths[@]}" | grep -v -e '^cmd/lanflow-helper/VERSION$' -e '_test\.go$' || true)"

# Its dependencies: the modules the helper is built from on each system, at
# the versions go.mod selects, and the Go version. Only those count: go.mod
# also changes for modules the helper never links (the UI framework), and
# such a change must not force every user to reinstall the helper.
helper_deps() { # <checkout>
  (
    cd "$1"
    sed -n 's/^go \([0-9][0-9.]*\).*/go \1/p; s/^toolchain \(.*\)/toolchain \1/p' go.mod
    for goos in darwin windows linux; do
      GOOS="$goos" GOFLAGS=-mod=mod go list -deps \
        -f '{{if not .Standard}}{{with .Module}}{{if ne .Path "lanflow"}}{{.Path}} {{.Version}}{{end}}{{end}}{{end}}' \
        ./cmd/lanflow-helper
    done | sed '/^$/d' | sort -u
  )
}
tmp="$(mktemp -d)"
cleanup() {
  git worktree remove --force "$tmp/prev" >/dev/null 2>&1 || true
  git worktree remove --force "$tmp/ref" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT
git worktree add --quiet --detach "$tmp/prev" "$PREV"
git worktree add --quiet --detach "$tmp/ref" "$REF"
deps_prev="$(helper_deps "$tmp/prev")"
deps_ref="$(helper_deps "$tmp/ref")"
if [ "$deps_prev" != "$deps_ref" ]; then
  deps_diff="$(diff <(printf '%s\n' "$deps_prev") <(printf '%s\n' "$deps_ref") | grep '^[<>]' || true)"
  changed="${changed:+$changed
}helper dependencies ($PREV < > $REF):
$deps_diff"
fi
if [ -z "$changed" ]; then
  echo "helper unchanged since $PREV"
  exit 0
fi
old="$(git show "$PREV:cmd/lanflow-helper/VERSION" 2>/dev/null | tr -d ' \n' || true)"
new="$(git show "$REF:cmd/lanflow-helper/VERSION" | tr -d ' \n')"
if [ "$old" = "$new" ]; then
  echo "error: the helper changed since $PREV but cmd/lanflow-helper/VERSION is still $new:" >&2
  echo "$changed" >&2
  exit 1
fi
echo "helper $old -> $new since $PREV"
