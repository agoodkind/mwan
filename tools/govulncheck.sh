#!/usr/bin/env bash
set -euo pipefail

KNOWN_GOBGP_VULN_ID="GO-2026-4736"
KNOWN_GOBGP_FIXED_VERSION="v4.3.0"
GOBGP_MODULE="github.com/osrg/gobgp/v4"
UPSTREAM_GOVULNCHECK_VERSION="v1.3.0"

function semver_gte() {
    local left="${1#v}"
    local right="${2#v}"
    local left_major left_minor left_patch
    local right_major right_minor right_patch

    IFS='.' read -r left_major left_minor left_patch <<< "$left"
    IFS='.' read -r right_major right_minor right_patch <<< "$right"

    left_major="${left_major:-0}"
    left_minor="${left_minor:-0}"
    left_patch="${left_patch:-0}"
    right_major="${right_major:-0}"
    right_minor="${right_minor:-0}"
    right_patch="${right_patch:-0}"

    if (( left_major > right_major )); then
        return 0
    fi
    if (( left_major < right_major )); then
        return 1
    fi
    if (( left_minor > right_minor )); then
        return 0
    fi
    if (( left_minor < right_minor )); then
        return 1
    fi
    if (( left_patch >= right_patch )); then
        return 0
    fi
    return 1
}

function module_version() {
    go list -m -f '{{.Version}}' "$GOBGP_MODULE"
}

function is_known_false_positive() {
    local output_file="$1"
    local version
    local vuln_count

    version="$(module_version)"
    if [[ -z "$version" ]]; then
        return 1
    fi
    if ! semver_gte "$version" "$KNOWN_GOBGP_FIXED_VERSION"; then
        return 1
    fi

    vuln_count="$(grep -c '^Vulnerability #' "$output_file" || true)"
    if [[ "$vuln_count" != "1" ]]; then
        return 1
    fi

    if ! grep -q "$KNOWN_GOBGP_VULN_ID" "$output_file"; then
        return 1
    fi
    if ! grep -q "Module: $GOBGP_MODULE" "$output_file"; then
        return 1
    fi
    return 0
}

function main() {
    local output_file
    output_file="$(mktemp)"
    trap 'rm -f "${output_file:-}"' EXIT

    go install "golang.org/x/vuln/cmd/govulncheck@${UPSTREAM_GOVULNCHECK_VERSION}"

    if govulncheck ./... >"$output_file" 2>&1; then
        cat "$output_file"
        return 0
    fi

    cat "$output_file"

    if is_known_false_positive "$output_file"; then
        printf '\n'
        printf '%s\n' \
            "govulncheck: suppressing $KNOWN_GOBGP_VULN_ID for $GOBGP_MODULE $(module_version)"
        printf '%s\n' \
            "govulncheck: upstream fix is present in v4.3.0+; current vuln metadata appears stale"
        return 0
    fi

    return 1
}

main "$@"
