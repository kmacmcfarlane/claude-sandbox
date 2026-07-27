#!/usr/bin/env bash
#
# check-spec-coverage.sh — every Gherkin scenario ID (CS-XXX-NNN) declared in
# spec/*.feature must be referenced somewhere in a *_test.go file (as an It()
# description, or in a comment explaining why it is skipped/manual).
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"

mapfile -t ids < <(grep -rhoE 'CS-[A-Z]+-[0-9]{3}' "$REPO_ROOT/spec"/*.feature | sort -u)

missing=0
for id in "${ids[@]}"; do
    if ! grep -rq "$id" --include='*_test.go' "$REPO_ROOT/internal" "$REPO_ROOT/cmd" 2>/dev/null; then
        echo "MISSING: $id has no reference in any *_test.go file" >&2
        missing=$((missing + 1))
    fi
done

echo "Spec coverage: $(( ${#ids[@]} - missing ))/${#ids[@]} scenario IDs referenced in tests."
if [ "$missing" -gt 0 ]; then
    exit 1
fi
