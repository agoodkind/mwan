#!/usr/bin/env bash
set -euo pipefail

: "${WANCONFIG_DOCKER_SOURCE:?Set WANCONFIG_DOCKER_SOURCE to the repository root}"
: "${WANCONFIG_DOCKER_ARCH:?Set WANCONFIG_DOCKER_ARCH}"
: "${WANCONFIG_BUILDER_IMAGE:?Set WANCONFIG_BUILDER_IMAGE}"

if [ "$#" -eq 0 ]; then
    printf '%s\n' 'Supply the command to run in the builder container.' >&2
    exit 64
fi

if [ -n "${MWAN_NATIVE_DPDK_PREFIX:-}" ]; then
    if [[ "${MWAN_NATIVE_DPDK_PREFIX}" != /* ]]; then
        printf '%s\n' 'MWAN_NATIVE_DPDK_PREFIX must be an absolute host directory.' >&2
        exit 64
    fi
    export MWAN_NATIVE_DPDK_CONTAINER_PREFIX="${MWAN_NATIVE_DPDK_CONTAINER_PREFIX:-${MWAN_NATIVE_DPDK_PREFIX}}"
    if [[ "${MWAN_NATIVE_DPDK_CONTAINER_PREFIX}" != /* ]]; then
        printf '%s\n' 'MWAN_NATIVE_DPDK_CONTAINER_PREFIX must be an absolute container directory.' >&2
        exit 64
    fi
else
    if [ -n "${MWAN_NATIVE_DPDK_CONTAINER_PREFIX:-}" ]; then
        printf '%s\n' 'Set MWAN_NATIVE_DPDK_PREFIX with MWAN_NATIVE_DPDK_CONTAINER_PREFIX.' >&2
        exit 64
    fi
fi

RUN_DIRECTORY=$(mktemp -d "${TMPDIR:-/tmp}/mwan-compose-XXXXXXXXXX")
PROJECT_NAME=$(printf '%s' "${RUN_DIRECTORY##*/}" | LC_ALL=C tr '[:upper:]' '[:lower:]')
COMPOSE_DIRECTORY="${BASH_SOURCE[0]%/*}"
COMPOSE_COMMAND=(docker compose --project-name "${PROJECT_NAME}" --file "${COMPOSE_DIRECTORY}/compose.yaml")
if [ -n "${MWAN_NATIVE_DPDK_PREFIX:-}" ]; then
    COMPOSE_COMMAND+=(--file "${COMPOSE_DIRECTORY}/compose.sdk.yaml")
fi
RUNNER_PID=

cleanup() {
    local run_status="$1"
    local cleanup_status=0
    trap - EXIT
    trap '' HUP INT TERM

    if [ -n "${RUNNER_PID}" ]; then
        # Interruption can stop the wrapper while Compose still creates resources.
        if kill -TERM "${RUNNER_PID}"; then
            if wait "${RUNNER_PID}"; then
                :
            else
                printf '%s\n' 'The interrupted Compose command stopped.' >&2
            fi
        else
            printf '%s\n' 'Could not signal the Compose command during cleanup.' >&2
        fi
    fi

    if "${COMPOSE_COMMAND[@]}" down --volumes --remove-orphans; then
        :
    else
        cleanup_status=$?
        printf 'Compose cleanup failed for project %s.\n' "${PROJECT_NAME}" >&2
    fi
    if rmdir "${RUN_DIRECTORY}"; then
        :
    else
        if [ "${cleanup_status}" -eq 0 ]; then
            cleanup_status=1
        fi
        printf 'Could not remove the temporary project directory %s.\n' "${RUN_DIRECTORY}" >&2
    fi

    if [ "${run_status}" -eq 0 ]; then
        run_status=${cleanup_status}
    fi
    exit "${run_status}"
}

trap 'cleanup "$?"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

printf 'Compose project: %s\n' "${PROJECT_NAME}"
"${COMPOSE_COMMAND[@]}" run --rm --no-TTY --no-deps builder "$@" &
RUNNER_PID=$!
RUN_STATUS=0
if wait "${RUNNER_PID}"; then
    :
else
    RUN_STATUS=$?
fi
RUNNER_PID=
cleanup "${RUN_STATUS}"
