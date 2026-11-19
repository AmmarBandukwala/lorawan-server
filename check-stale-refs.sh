#!/bin/sh
# Report references to repo-internal source paths that no longer resolve.
#
# Why this exists: the compiler validates references in code, so a rename that
# moves a file is caught immediately in the files that import it. References in
# comments, markdown and SQL comments are invisible to every compiler, and they
# are exactly where a rename goes to rot. This repo found six such references
# after a directory move that had otherwise passed a full build, vet and test
# cycle — including one comment that pointed at a test which, on inspection,
# never existed.
#
# The check is deliberately narrow. A guard with false positives is a guard that
# gets ignored, so anything that cannot be resolved with certainty is skipped:
# HTTP route strings, Go import paths (the compiler owns those), upstream
# citations into ChirpStack's Rust tree, and generated files.
#
# Exits non-zero when a reference is stale, so it can gate a commit or a build.

set -eu

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

status=0

# Everything in the tree that a commit would carry: tracked files plus new ones
# not yet staged. Using tracked files only would skip the scan *and* the
# resolution for a brand-new file, which is exactly the file most likely to be
# wrong and is added in the same commit that would be checked.
all=$(git ls-files --others --exclude-standard; git ls-files)

# Files worth scanning: text that a human writes. Generated and vendored files
# are excluded because their "paths" are package tarballs and source maps. This
# script is excluded too, because its own pattern source contains the path
# fragments it looks for and would report itself forever.
files=$(printf '%s\n' "$all" | grep -v -E \
  '^(package-lock\.json|dist/|node_modules/|check-stale-refs\.sh$)|\.(min\.(js|css)|map|png|ico|woff2?)$')

# What a reference may point at: the same set.
resolvable="$all"

# 1. Directory-anchored paths: these can only mean a file in this tree.
for f in $files; do
  grep -noE '(ns|api)/(internal|cmd)/[A-Za-z0-9_./-]+|(src/features|db|migrations)/[A-Za-z0-9_./-]+|storage/[a-z_]+|ns/(test|internal|cmd)' \
    "$f" 2>/dev/null |
  while IFS=: read -r ln tok; do
    # Trailing sentence punctuation is not part of a path.
    tok=$(printf '%s' "$tok" | sed 's/[.,;:)]*$//')
    case "$tok" in
      */v1*|api/v*) continue ;;   # HTTP routes, not files
      ns/test|ns/internal|ns/cmd) [ -e "$tok" ] || {
        printf '%s:%s\tstale directory reference: %s\n' "$f" "$ln" "$tok"; }
        continue ;;
    esac
    # A final segment with a dot that is not a known file extension is a
    # qualified identifier, not a path: `engine.UseRegionDefault` and
    # `httpapi.regionDefault` are how Go names a constant, and prose about a
    # constant in the other module is not a broken link.
    last=${tok##*/}
    case "$last" in
      *.go|*.ts|*.tsx|*.js|*.jsx|*.mjs|*.sql|*.md|*.sh|*.toml|*.yaml|*.yml|*.json|*.rs|*.html|*.css) ;;
      *.*) continue ;;
    esac
    # Try the path as written, then relative to ns/, then as a TypeScript
    # module specifier, which is written without an extension.
    found=0
    for cand in "$tok" "ns/$tok" "${tok#ns/}"; do
      if [ -e "$cand" ] || [ -e "$cand.go" ] || [ -e "$cand.ts" ] ||
         [ -e "$cand.tsx" ] || [ -e "$cand/index.ts" ] || [ -e "$cand/index.tsx" ]; then
        found=1; break
      fi
    done
    [ "$found" -eq 1 ] || printf '%s:%s\tstale path reference: %s\n' "$f" "$ln" "$tok"
  done
done | sort -u > /tmp/stale-refs.$$

# 2. Filenames cited in markdown. A bare `fcnt_test.go` in prose is a reference
#    like any other, and a directory-anchored scan cannot see it.
for f in $(printf '%s\n' "$files" | grep -E '\.md$'); do
  grep -noE '`[A-Za-z0-9_.-]+\.(go|ts|tsx|sql|toml|sh)`' "$f" 2>/dev/null |
  sed 's/`//g' |
  while IFS=: read -r ln name; do
    case "$name" in */*) continue ;; esac   # anchored forms are covered above
    base=$(basename "$name")
    # Match the basename at the start of a path too: a file at the repository
    # root has no directory component to match a slash against.
    if ! printf '%s\n' "$resolvable" | grep -qE "(^|/)${base}\$"; then
      printf '%s:%s\tstale file reference: %s\n' "$f" "$ln" "$name"
    fi
  done
done | sort -u > /tmp/stale-files.$$

for out in /tmp/stale-refs.$$ /tmp/stale-files.$$; do
  if [ -s "$out" ]; then
    cat "$out"
    status=1
  fi
done
rm -f /tmp/stale-refs.$$ /tmp/stale-files.$$

if [ "$status" -eq 0 ]; then
  echo "stale references: none"
fi
exit "$status"
