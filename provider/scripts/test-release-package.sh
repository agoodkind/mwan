#!/usr/bin/env bash
# The script compiles no code.

set -euo pipefail

PROVIDER_ADDRESS="tofu.home.arpa/agoodkind/mwan"
PROVIDER_BINARY="terraform-provider-mwan"
PLACEHOLDER_VERSION="0.0.1"
SCHEMA_ERROR="Invalid network schema"
CHECKSUMS_NAME="checksums.txt"
RUNTIME_PATH="/usr/bin:/bin:/usr/sbin:/sbin"
BUILD_TOOLS=(cc gcc clang go cmake pkg-config)
VALID_DOCUMENT_NAME="network-routes.json"
MANAGEMENT_ENTRY='{ "name": "enmgmt0", "type": "iana-if-type:other" }'
UNKNOWN_MEMBER_ENTRY='{ "name": "enmgmt0", "type": "iana-if-type:other", "unknown-member": true }'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURE_DIR="${SCRIPT_DIR}/../internal/provider/testdata/networkplan"

WORK_DIR=""
PLATFORM_OS=""
PLATFORM_ARCH=""
CHILD_PIDS=()
INTERRUPTED=0
RUN_STATUS=0

EVIDENCE_ARCHIVE="not recorded"
EVIDENCE_SHA256="not recorded"
EVIDENCE_CHECKSUMS="not recorded"
EVIDENCE_VERSION="not recorded"
EVIDENCE_COMMIT="not recorded"
EVIDENCE_PLATFORM="not recorded"
EVIDENCE_TOFU="not recorded"
EVIDENCE_LINKAGE="not recorded"
EVIDENCE_DEPENDENCIES=()
EVIDENCE_BUILD_TOOLS="not recorded"
EVIDENCE_INIT="not run"
EVIDENCE_VALID_PLAN="not run"
EVIDENCE_REJECTION="not run"

log() {
    printf 'test-release-package: %s\n' "$*" >&2
}

fail() {
    log "error: $*"
    exit 1
}

kill_children() {
    local pid
    for pid in "${CHILD_PIDS[@]+"${CHILD_PIDS[@]}"}"; do
        # Cleanup ignores kill failures because a child may have already exited.
        kill "${pid}" 2>/dev/null || true
    done
}

remove_work_dir() {
    if [[ -n "${WORK_DIR}" && -d "${WORK_DIR}" ]]; then
        rm -rf "${WORK_DIR}"
    fi
}

print_evidence() {
    local status="$1"
    local dependency
    printf '%s\n' "===== release package evidence ====="
    printf 'archive: %s\n' "${EVIDENCE_ARCHIVE}"
    printf 'archive sha256: %s\n' "${EVIDENCE_SHA256}"
    printf 'checksums file: %s\n' "${EVIDENCE_CHECKSUMS}"
    printf 'provider address: %s\n' "${PROVIDER_ADDRESS}"
    printf 'provider mirror version: %s\n' "${EVIDENCE_VERSION}"
    printf 'provider stamped commit: %s\n' "${EVIDENCE_COMMIT}"
    printf 'platform: %s\n' "${EVIDENCE_PLATFORM}"
    printf 'tofu: %s\n' "${EVIDENCE_TOFU}"
    printf 'linkage: %s\n' "${EVIDENCE_LINKAGE}"
    printf '%s\n' "loader dependencies:"
    for dependency in "${EVIDENCE_DEPENDENCIES[@]+"${EVIDENCE_DEPENDENCIES[@]}"}"; do
        printf '  %s\n' "${dependency}"
    done
    printf 'runtime PATH: %s\n' "${RUNTIME_PATH}"
    printf 'build tools on runtime PATH: %s\n' "${EVIDENCE_BUILD_TOOLS}"
    printf 'tofu init: %s\n' "${EVIDENCE_INIT}"
    printf 'valid plan: %s\n' "${EVIDENCE_VALID_PLAN}"
    printf 'schema rejection: %s\n' "${EVIDENCE_REJECTION}"
    printf 'exit status: %s\n' "${status}"
    printf '%s\n' "===== end of evidence ====="
}

on_exit() {
    local status=$?
    trap - EXIT
    kill_children
    if [[ "${INTERRUPTED}" -eq 0 ]]; then
        print_evidence "${status}"
    fi
    remove_work_dir
    exit "${status}"
}

on_interrupt() {
    INTERRUPTED=1
    log "The script received an interrupt signal."
    exit 130
}

on_terminate() {
    INTERRUPTED=1
    log "The script received a termination signal."
    exit 143
}

require_absolute_file() {
    local name="$1"
    local value="$2"
    if [[ -z "${value}" ]]; then
        fail "${name} is empty."
    fi
    if [[ "${value}" != /* ]]; then
        fail "${name} uses the relative path ${value}."
    fi
    if [[ ! -f "${value}" ]]; then
        fail "${name} does not identify a file at ${value}."
    fi
}

sha256_of() {
    local file="$1"
    local output
    if command -v sha256sum >/dev/null 2>&1; then
        output="$(sha256sum "${file}")"
    elif command -v shasum >/dev/null 2>&1; then
        output="$(shasum -a 256 "${file}")"
    else
        fail "The host has neither sha256sum nor shasum."
    fi
    printf '%s\n' "${output%% *}"
}

detect_platform() {
    local kernel machine
    kernel="$(uname -s)"
    machine="$(uname -m)"
    case "${kernel}" in
        Darwin) PLATFORM_OS="darwin" ;;
        Linux) PLATFORM_OS="linux" ;;
        *) fail "The script does not support the operating system ${kernel}." ;;
    esac
    case "${machine}" in
        arm64 | aarch64) PLATFORM_ARCH="arm64" ;;
        x86_64 | amd64) PLATFORM_ARCH="amd64" ;;
        *) fail "The script does not support the architecture ${machine}." ;;
    esac
    EVIDENCE_PLATFORM="${PLATFORM_OS}/${PLATFORM_ARCH}"
}

check_archive_platform() {
    local archive_name="$1"
    local expected_suffix="_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"
    if [[ "${archive_name}" == "${PROVIDER_BINARY}"_*_*.tar.gz ]]; then
        if [[ "${archive_name}" != *"${expected_suffix}" ]]; then
            fail "The archive ${archive_name} does not match the host platform ${EVIDENCE_PLATFORM}."
        fi
    else
        log "The script assumes ${EVIDENCE_PLATFORM} because the archive name ${archive_name} does not specify a platform."
    fi
}

verify_checksums() {
    local archive="$1"
    local digest="$2"
    local checksums_file expected archive_name
    checksums_file="$(dirname "${archive}")/${CHECKSUMS_NAME}"
    archive_name="$(basename "${archive}")"
    if [[ ! -f "${checksums_file}" ]]; then
        EVIDENCE_CHECKSUMS="absent beside the archive"
        log "The script skips checksum comparison because the archive directory lacks ${CHECKSUMS_NAME}."
        return 0
    fi
    expected="$(awk -v name="${archive_name}" '{ file = $NF; sub(/^\*/, "", file); if (file == name) { print $1 } }' "${checksums_file}")"
    if [[ -z "${expected}" ]]; then
        EVIDENCE_CHECKSUMS="${checksums_file} has no entry for ${archive_name}"
        fail "${checksums_file} has no entry for ${archive_name}."
    fi
    if [[ "${expected}" != "${digest}" ]]; then
        EVIDENCE_CHECKSUMS="${checksums_file} records ${expected}, which differs from the archive"
        fail "The archive SHA-256 ${digest} differs from ${expected} in ${checksums_file}."
    fi
    EVIDENCE_CHECKSUMS="${checksums_file} matches the archive"
}

extract_archive() {
    local archive="$1"
    local package_dir="$2"
    local version="$3"
    local members member
    members="$(tar -tzf "${archive}")"
    while IFS= read -r member; do
        if [[ "${member}" == /* || "${member}" == *..* ]]; then
            fail "The archive member ${member} uses an absolute path or contains two consecutive periods."
        fi
    done <<<"${members}"
    mkdir -p "${package_dir}"
    tar -xzf "${archive}" -C "${package_dir}"
    if [[ ! -f "${package_dir}/${PROVIDER_BINARY}" ]]; then
        fail "The archive members ${members} do not include ${PROVIDER_BINARY}."
    fi
    mv "${package_dir}/${PROVIDER_BINARY}" "${package_dir}/${PROVIDER_BINARY}_v${version}"
    if [[ ! -x "${package_dir}/${PROVIDER_BINARY}_v${version}" ]]; then
        fail "The archive member ${PROVIDER_BINARY} is not executable."
    fi
}

list_darwin_dependencies() {
    local binary="$1"
    local probe output
    if probe="$(xcode-select -p 2>&1)"; then
        output="$(otool -L "${binary}")"
        printf '%s\n' "${output}" | sed -n '2,$p' | sed -e 's/^[[:space:]]*//' -e 's/ (compatibility.*$//'
        return 0
    fi
    log "The script uses dyld_info because xcode-select failed with output ${probe}."
    output="$(dyld_info -dependents "${binary}")"
    printf '%s\n' "${output}" | awk 'NR > 1 && $NF ~ /^[\/@]/ { print $NF }'
}

list_linux_dependencies() {
    local binary="$1"
    local output
    if output="$(ldd "${binary}" 2>&1)"; then
        printf '%s\n' "${output}" | awk '
            $2 == "=>" && $3 == "not" { print "missing:" $1; next }
            $2 == "=>" { print $3; next }
            $1 ~ /^\// { print $1; next }
            { print "virtual:" $1 }'
        return 0
    fi
    case "${output}" in
        *"not a dynamic executable"* | *"statically linked"*)
            printf '%s\n' "static:"
            ;;
        *)
            fail "ldd failed to inspect ${binary} with output ${output}."
            ;;
    esac
}

dependency_allowed() {
    local dependency="$1"
    local package_dir="$2"
    case "${dependency}" in
        static: | virtual:linux-vdso.so.1 | virtual:linux-gate.so.1) return 0 ;;
        missing:* | virtual:*) return 1 ;;
        "${package_dir}"/*) return 0 ;;
        @loader_path/*)
            if [[ -f "${package_dir}/${dependency#@loader_path/}" ]]; then
                return 0
            fi
            return 1
            ;;
    esac
    if [[ "${PLATFORM_OS}" == "darwin" ]]; then
        case "${dependency}" in
            /usr/lib/* | /System/Library/*) return 0 ;;
        esac
        return 1
    fi
    case "${dependency}" in
        /lib/* | /lib64/* | /usr/lib/* | /usr/lib64/*) return 0 ;;
    esac
    return 1
}

check_dependencies() {
    local binary="$1"
    local package_dir="$2"
    local listing dependency
    local rejected=()
    if [[ "${PLATFORM_OS}" == "darwin" ]]; then
        listing="$(list_darwin_dependencies "${binary}")"
    else
        listing="$(list_linux_dependencies "${binary}")"
    fi
    EVIDENCE_LINKAGE="dynamic"
    while IFS= read -r dependency; do
        if [[ -z "${dependency}" ]]; then
            continue
        fi
        if [[ "${dependency}" == "static:" ]]; then
            EVIDENCE_LINKAGE="static"
            continue
        fi
        EVIDENCE_DEPENDENCIES+=("${dependency}")
        if ! dependency_allowed "${dependency}" "${package_dir}"; then
            rejected+=("${dependency}")
        fi
    done <<<"${listing}"
    if [[ "${#rejected[@]}" -gt 0 ]]; then
        fail "The provider has unsupported loader dependencies ${rejected[*]}."
    fi
}

record_stamped_commit() {
    local binary="$1"
    local output commit
    # Commit extraction accepts output from a provider invocation that exits with an error.
    if output="$("${binary}" 2>&1)"; then
        log "The provider exited with status 0 outside a plugin host."
    fi
    commit="$(printf '%s\n' "${output}" | sed -n 's/.*provider server starting.* commit=\([^ ]*\).*/\1/p')"
    if [[ -z "${commit}" ]]; then
        fail "The script found no stamped commit in the direct provider output ${output}."
    fi
    EVIDENCE_COMMIT="${commit}"
}

record_build_tools() {
    local tool location
    local found=()
    for tool in "${BUILD_TOOLS[@]}"; do
        if location="$(PATH="${RUNTIME_PATH}" command -v "${tool}")"; then
            found+=("${location}")
        fi
    done
    if [[ "${#found[@]}" -eq 0 ]]; then
        EVIDENCE_BUILD_TOOLS="none"
    else
        EVIDENCE_BUILD_TOOLS="${found[*]}"
    fi
}

run_tofu() {
    local log_file="$1"
    shift
    local pid
    (
        cd "${WORK_DIR}/module"
        exec env -i \
            HOME="${WORK_DIR}/home" \
            PATH="${RUNTIME_PATH}" \
            TMPDIR="${WORK_DIR}/tmp" \
            TF_CLI_CONFIG_FILE="${WORK_DIR}/tofurc" \
            TF_IN_AUTOMATION=1 \
            "${TOFU}" "$@"
    ) >"${log_file}" 2>&1 &
    pid=$!
    CHILD_PIDS+=("${pid}")
    if wait "${pid}"; then
        RUN_STATUS=0
    else
        RUN_STATUS=$?
    fi
    CHILD_PIDS=()
}

show_log() {
    local log_file="$1"
    sed -e 's/^/    /' "${log_file}" >&2
}

main() {
    local archive="${PROVIDER_PACKAGE:-}"
    local version="${PROVIDER_VERSION:-}"
    local package_dir binary valid_document invalid_document tofu_version

    trap on_exit EXIT
    trap on_interrupt INT
    trap on_terminate TERM

    TOFU="${TOFU:-}"
    require_absolute_file "PROVIDER_PACKAGE" "${archive}"
    require_absolute_file "TOFU" "${TOFU}"
    if [[ ! -x "${TOFU}" ]]; then
        fail "The TOFU file ${TOFU} is not executable."
    fi
    if [[ -z "${version}" ]]; then
        version="${PLACEHOLDER_VERSION}"
        log "The mirror uses version ${version} because PROVIDER_VERSION is empty."
    fi
    EVIDENCE_ARCHIVE="${archive}"
    EVIDENCE_VERSION="${version}"

    detect_platform
    check_archive_platform "$(basename "${archive}")"
    EVIDENCE_SHA256="$(sha256_of "${archive}")"
    verify_checksums "${archive}" "${EVIDENCE_SHA256}"

    valid_document="${FIXTURE_DIR}/${VALID_DOCUMENT_NAME}"
    if [[ ! -f "${FIXTURE_DIR}/main.tf" || ! -f "${valid_document}" ]]; then
        fail "The fixture directory ${FIXTURE_DIR} does not contain both main.tf and ${VALID_DOCUMENT_NAME}."
    fi

    WORK_DIR="$(mktemp -d)"
    mkdir -p "${WORK_DIR}/home" "${WORK_DIR}/tmp" "${WORK_DIR}/module"
    package_dir="${WORK_DIR}/mirror/${PROVIDER_ADDRESS}/${version}/${PLATFORM_OS}_${PLATFORM_ARCH}"
    extract_archive "${archive}" "${package_dir}" "${version}"
    binary="${package_dir}/${PROVIDER_BINARY}_v${version}"

    check_dependencies "${binary}" "${package_dir}"
    record_stamped_commit "${binary}"
    record_build_tools

    printf 'provider_installation {\n  filesystem_mirror {\n    path    = "%s"\n    include = ["%s"]\n  }\n}\n' \
        "${WORK_DIR}/mirror" "${PROVIDER_ADDRESS}" >"${WORK_DIR}/tofurc"
    cp "${FIXTURE_DIR}/main.tf" "${WORK_DIR}/module/main.tf"

    invalid_document="${WORK_DIR}/unknown-member.json"
    sed "s/${MANAGEMENT_ENTRY}/${UNKNOWN_MEMBER_ENTRY}/" "${valid_document}" >"${invalid_document}"
    if ! grep -q -F '"unknown-member": true' "${invalid_document}"; then
        fail "The substitution of ${MANAGEMENT_ENTRY} in ${VALID_DOCUMENT_NAME} did not produce the unknown member."
    fi

    run_tofu "${WORK_DIR}/version.log" version
    if [[ "${RUN_STATUS}" -ne 0 ]]; then
        show_log "${WORK_DIR}/version.log"
        fail "tofu version exited with status ${RUN_STATUS}."
    fi
    tofu_version="$(sed -n '1p' "${WORK_DIR}/version.log")"
    EVIDENCE_TOFU="${TOFU} (${tofu_version})"

    run_tofu "${WORK_DIR}/init.log" init -input=false -no-color
    if [[ "${RUN_STATUS}" -ne 0 ]]; then
        EVIDENCE_INIT="failed with status ${RUN_STATUS}"
        show_log "${WORK_DIR}/init.log"
        fail "tofu init exited with status ${RUN_STATUS}."
    fi
    if ! grep -q -F "${PROVIDER_ADDRESS} v${version}" "${WORK_DIR}/init.log"; then
        EVIDENCE_INIT="succeeded without installing ${PROVIDER_ADDRESS} v${version}"
        show_log "${WORK_DIR}/init.log"
        fail "tofu init did not report installing ${PROVIDER_ADDRESS} v${version}."
    fi
    EVIDENCE_INIT="succeeded; installed ${PROVIDER_ADDRESS} v${version} from the filesystem mirror"

    run_tofu "${WORK_DIR}/valid.log" plan -input=false -no-color -var "network_file=${valid_document}"
    if [[ "${RUN_STATUS}" -ne 0 ]]; then
        EVIDENCE_VALID_PLAN="failed with status ${RUN_STATUS}"
        show_log "${WORK_DIR}/valid.log"
        fail "tofu plan exited with status ${RUN_STATUS} for the valid document."
    fi
    if ! grep -q -F "mwan_network_config.gateway will be created" "${WORK_DIR}/valid.log"; then
        EVIDENCE_VALID_PLAN="succeeded without planning mwan_network_config.gateway"
        show_log "${WORK_DIR}/valid.log"
        fail "tofu plan did not report creating mwan_network_config.gateway for the valid document."
    fi
    EVIDENCE_VALID_PLAN="succeeded; $(grep -F "Plan:" "${WORK_DIR}/valid.log")"

    run_tofu "${WORK_DIR}/invalid.log" plan -input=false -no-color -var "network_file=${invalid_document}"
    if [[ "${RUN_STATUS}" -eq 0 ]]; then
        EVIDENCE_REJECTION="missing; tofu plan accepted the unknown member"
        show_log "${WORK_DIR}/invalid.log"
        fail "tofu plan accepted a document with an unknown member."
    fi
    if ! grep -q -F "${SCHEMA_ERROR}" "${WORK_DIR}/invalid.log"; then
        EVIDENCE_REJECTION="tofu plan failed with status ${RUN_STATUS} without the schema diagnostic"
        show_log "${WORK_DIR}/invalid.log"
        fail "tofu plan failed without the diagnostic ${SCHEMA_ERROR}."
    fi
    EVIDENCE_REJECTION="tofu plan failed with status ${RUN_STATUS}; $(grep -F "${SCHEMA_ERROR}" "${WORK_DIR}/invalid.log" | sed -n '1p')"
}

main "$@"
