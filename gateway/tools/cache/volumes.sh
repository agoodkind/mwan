#!/usr/bin/env bash
set -euo pipefail

usage() {
    printf 'Run this script with cache-usage, cache-trim, or cache-clean and a checkout ID, architecture, and three limits in MB.\n' >&2
    exit 2
}

if [[ $# -ne 6 ]]; then
    usage
fi

mode=$1
checkout_id=$2
architecture=$3
gomod_limit=$4
build_limit=$5
gomk_limit=$6

case "$mode" in
    cache-usage | cache-trim | cache-clean) ;;
    *) usage ;;
esac
case "$architecture" in
    amd64 | arm64) ;;
    *) usage ;;
esac
if [[ ! "$checkout_id" =~ ^[0-9]+$ ]]; then
    usage
fi
for limit in "$gomod_limit" "$build_limit" "$gomk_limit"; do
    if [[ ! "$limit" =~ ^[0-9]+$ ]]; then
        usage
    fi
done

suffix="$checkout_id-$architecture"
script_dir=$(dirname "$0")
disk_usage=$(docker system df --verbose --format '{{range .Volumes}}{{println .Name .Links .Size}}{{end}}')
volumes=$(awk -v mode="$mode" -v suffix="$suffix" -v gomod_limit="$gomod_limit" \
    -v build_limit="$build_limit" -v gomk_limit="$gomk_limit" \
    -f "$script_dir/volumes.awk" <<< "$disk_usage")

printf '%-40s %10s %8s %10s %s\n' 'VOLUME' 'SIZE' 'USERS' 'LIMIT (MB)' 'ACTION'
while read -r volume size users limit action; do
    printf '%-40s %10s %8s %10s %s\n' "$volume" "$size" "$users" "$limit" "$action"
    if [[ "$action" == delete ]]; then
        docker volume rm "$volume"
    fi
done <<< "$volumes"
