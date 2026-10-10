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
NATIVE_LIBRARIES=(libyang libpcre2 libxxhash)
DARWIN_DEPENDENCIES=(
    /usr/lib/libresolv.9.dylib
    /usr/lib/libSystem.B.dylib
    /System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation
    /System/Library/Frameworks/Security.framework/Versions/A/Security
)
LINUX_VIRTUAL_DEPENDENCIES=(linux-vdso.so.1 linux-gate.so.1)
LINUX_LIBRARIES=(libc.so.6 libm.so.6)
DEPENDENCY_LISTING=""
VALID_DOCUMENT_NAME="network-routes.json"
MANAGEMENT_ENTRY='{ "name": "enmgmt0", "type": "iana-if-type:other" }'
UNKNOWN_MEMBER_ENTRY='{ "name": "enmgmt0", "type": "iana-if-type:other", "unknown-member": true }'
REJECTED_ENTRY_ERROR="Rejected provider entry"
CONFIG_ADDRESS="mwan_network_config.gateway"
DATA_ADDRESS="data.mwan_network.gateway"
FIXTURE_FILES=(main.tf deferred.tf network-routes.json changed.json added.json removed.json reformatted.json invalid.json)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURE_DIR="${SCRIPT_DIR}/../internal/provider/testdata/networkplan"

WORK_DIR=""
MODULE_DIR=""
EVIDENCE_BASE_APPLY="not run"
EVIDENCE_CASES=()
PLATFORM_OS=""
PLATFORM_ARCH=""
CHILD_PIDS=()
INTERRUPTED=0

EVIDENCE_ARCHIVE="not recorded"
EVIDENCE_SHA256="not recorded"
EVIDENCE_CHECKSUMS="not recorded"
EVIDENCE_VERSION="not recorded"
EVIDENCE_COMMIT="not recorded"
EVIDENCE_EXPECTED_COMMIT="not recorded"
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
    local dependency case_line
    printf '%s\n' "===== release package evidence ====="
    printf 'archive: %s\n' "${EVIDENCE_ARCHIVE}"
    printf 'archive sha256: %s\n' "${EVIDENCE_SHA256}"
    printf 'checksums file: %s\n' "${EVIDENCE_CHECKSUMS}"
    printf 'provider address: %s\n' "${PROVIDER_ADDRESS}"
    printf 'provider mirror version: %s\n' "${EVIDENCE_VERSION}"
    printf 'provider stamped commit: %s\n' "${EVIDENCE_COMMIT}"
    printf 'expected commit: %s\n' "${EVIDENCE_EXPECTED_COMMIT}"
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
    printf 'base apply: %s\n' "${EVIDENCE_BASE_APPLY}"
    printf '%s\n' "cases:"
    for case_line in "${EVIDENCE_CASES[@]+"${EVIDENCE_CASES[@]}"}"; do
        printf '  %s\n' "${case_line}"
    done
    printf 'exit status: %s\n' "${status}"
    printf '%s\n' "===== end of evidence ====="
    printf '%s\n' "===== plan excerpts ====="
    if [[ -n "${WORK_DIR}" && -f "${WORK_DIR}/excerpts.txt" ]]; then
        sed -n 'p' "${WORK_DIR}/excerpts.txt"
    fi
    printf '%s\n' "===== end of plan excerpts ====="
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
        if [[ "${ALLOW_MISSING_CHECKSUMS:-}" != "1" ]]; then
            EVIDENCE_CHECKSUMS="absent beside the archive"
            fail "The script requires ${CHECKSUMS_NAME} beside the archive unless ALLOW_MISSING_CHECKSUMS=1."
        fi
        EVIDENCE_CHECKSUMS="absent beside the archive; the caller allowed the absence with ALLOW_MISSING_CHECKSUMS=1"
        log "The script skips comparison with missing ${CHECKSUMS_NAME} under ALLOW_MISSING_CHECKSUMS=1."
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

# Run listing functions outside command substitution so fail exits the script.
list_darwin_dependencies() {
    local binary="$1"
    local probe output
    if probe="$(xcode-select -p 2>&1)"; then
        if ! output="$(otool -L "${binary}" 2>&1)"; then
            fail "otool -L failed to inspect ${binary} with output ${output}."
        fi
        DEPENDENCY_LISTING="$(printf '%s\n' "${output}" | sed -n '2,$p' | sed -e 's/^[[:space:]]*//' -e 's/ (compatibility.*$//')"
        return 0
    fi
    log "The script uses dyld_info because xcode-select failed with output ${probe}."
    if ! output="$(dyld_info -dependents "${binary}" 2>&1)"; then
        fail "dyld_info failed to inspect ${binary} with output ${output}."
    fi
    DEPENDENCY_LISTING="$(printf '%s\n' "${output}" | awk 'NR > 1 && $NF ~ /^[\/@]/ { print $NF }')"
}

list_linux_dependencies() {
    local binary="$1"
    local output
    if output="$(ldd "${binary}" 2>&1)"; then
        DEPENDENCY_LISTING="$(printf '%s\n' "${output}" | awk '
            $2 == "=>" && $3 == "not" { print "missing:" $1; next }
            $2 == "=>" { print $3; next }
            $1 ~ /^\// { print $1; next }
            { print "virtual:" $1 }')"
        return 0
    fi
    case "${output}" in
        *"not a dynamic executable"* | *"statically linked"*)
            DEPENDENCY_LISTING="static:"
            ;;
        *)
            fail "ldd failed to inspect ${binary} with output ${output}."
            ;;
    esac
}

dependency_in_package() {
    local dependency="$1"
    local package_dir="$2"
    case "${dependency}" in
        "${package_dir}"/*) return 0 ;;
        @loader_path/*)
            if [[ -f "${package_dir}/${dependency#@loader_path/}" ]]; then
                return 0
            fi
            ;;
    esac
    return 1
}

# The provider must link libyang, libpcre2, and libxxhash statically or load them from the package.
dependency_is_native_library() {
    local dependency="$1"
    local library
    for library in "${NATIVE_LIBRARIES[@]}"; do
        if [[ "${dependency##*/}" == *"${library}"* ]]; then
            return 0
        fi
    done
    return 1
}

dependency_allowed() {
    local dependency="$1"
    local package_dir="$2"
    local allowed directory triplet loader
    if dependency_in_package "${dependency}" "${package_dir}"; then
        return 0
    fi
    if [[ "${PLATFORM_OS}" == "darwin" ]]; then
        for allowed in "${DARWIN_DEPENDENCIES[@]}"; do
            if [[ "${dependency}" == "${allowed}" ]]; then
                return 0
            fi
        done
        return 1
    fi
    for allowed in "${LINUX_VIRTUAL_DEPENDENCIES[@]}"; do
        if [[ "${dependency}" == "virtual:${allowed}" ]]; then
            return 0
        fi
    done
    if [[ "${PLATFORM_ARCH}" == "arm64" ]]; then
        triplet="aarch64-linux-gnu"
        loader="ld-linux-aarch64.so.1"
    else
        triplet="x86_64-linux-gnu"
        loader="ld-linux-x86-64.so.2"
    fi
    # ldd can report /lib or /usr/lib paths for the same file on merged-usr distributions.
    for directory in "/lib/${triplet}" "/usr/lib/${triplet}" /lib64 /usr/lib64 /lib /usr/lib; do
        for allowed in "${LINUX_LIBRARIES[@]}" "${loader}"; do
            if [[ "${dependency}" == "${directory}/${allowed}" ]]; then
                return 0
            fi
        done
    done
    return 1
}

check_dependencies() {
    local binary="$1"
    local package_dir="$2"
    local dependency
    local rejected=()
    local native=()
    DEPENDENCY_LISTING=""
    if [[ "${PLATFORM_OS}" == "darwin" ]]; then
        list_darwin_dependencies "${binary}"
    else
        list_linux_dependencies "${binary}"
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
        if dependency_is_native_library "${dependency}" && ! dependency_in_package "${dependency}" "${package_dir}"; then
            native+=("${dependency}")
        elif ! dependency_allowed "${dependency}" "${package_dir}"; then
            rejected+=("${dependency}")
        fi
    done <<<"${DEPENDENCY_LISTING}"
    if [[ "${#native[@]}" -gt 0 ]]; then
        fail "The provider depends on native libraries outside the package at ${native[*]}."
    fi
    if [[ "${#rejected[@]}" -gt 0 ]]; then
        fail "The provider has unsupported loader dependencies ${rejected[*]}."
    fi
    if [[ "${EVIDENCE_LINKAGE}" == "dynamic" && "${#EVIDENCE_DEPENDENCIES[@]}" -eq 0 ]]; then
        fail "The loader inspection found no dependencies for the dynamic binary ${binary}."
    fi
}

record_stamped_commit() {
    local binary="$1"
    local expected="$2"
    local output commit
    # Commit extraction accepts output from a provider invocation that exits with an error.
    if output="$(env -i HOME="${WORK_DIR}/home" PATH="${RUNTIME_PATH}" TMPDIR="${WORK_DIR}/tmp" "${binary}" 2>&1)"; then
        log "The provider exited with status 0 outside a plugin host."
    fi
    commit="$(printf '%s\n' "${output}" | sed -n 's/.*provider server starting.* commit=\([^ ]*\).*/\1/p')"
    if [[ -z "${commit}" ]]; then
        fail "The script found no stamped commit in the direct provider output ${output}."
    fi
    EVIDENCE_COMMIT="${commit}"
    if [[ -z "${expected}" ]]; then
        EVIDENCE_EXPECTED_COMMIT="not set"
        return 0
    fi
    # EXPECTED_COMMIT matches when either commit value is a prefix of the other.
    if [[ "${commit}" != "${expected}"* && "${expected}" != "${commit}"* ]]; then
        EVIDENCE_EXPECTED_COMMIT="${expected}, which differs from the stamped commit ${commit}"
        fail "The stamped commit ${commit} differs from EXPECTED_COMMIT ${expected}."
    fi
    EVIDENCE_EXPECTED_COMMIT="${expected}, which matches the stamped commit ${commit}"
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
    local status=0
    (
        cd "${MODULE_DIR}"
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
    wait "${pid}" || status=$?
    CHILD_PIDS=()
    return "${status}"
}

show_log() {
    local log_file="$1"
    sed -e 's/^/    /' "${log_file}" >&2
}

record_case() {
    EVIDENCE_CASES+=("$1")
}

fail_case() {
    local name="$1"
    local reason="$2"
    local log_file="$3"
    record_case "${name}: FAILED; ${reason}"
    show_log "${log_file}"
    fail "The case ${name} failed because ${reason}."
}

# Each replacement requires exactly one match in the source file.
replace_once() {
    local source_file="$1"
    local target_file="$2"
    local old_text="$3"
    local new_text="$4"
    local status
    if OLD_TEXT="${old_text}" NEW_TEXT="${new_text}" awk '
        BEGIN { old = ENVIRON["OLD_TEXT"]; replacement = ENVIRON["NEW_TEXT"] }
        { content = content $0 "\n" }
        END {
            position = index(content, old)
            if (position == 0) { exit 3 }
            rest = substr(content, position + length(old))
            if (index(rest, old) != 0) { exit 4 }
            printf "%s%s%s", substr(content, 1, position - 1), replacement, rest
        }' "${source_file}" >"${target_file}"; then
        return 0
    else
        status=$?
    fi
    if [[ "${status}" -eq 3 ]]; then
        fail "${source_file} does not contain the text ${old_text}."
    fi
    if [[ "${status}" -eq 4 ]]; then
        fail "${source_file} contains the text ${old_text} more than once."
    fi
    fail "awk exited with status ${status} while editing ${source_file}."
}

make_variant() {
    local name="$1"
    shift
    local target="${WORK_DIR}/documents/${name}.json"
    cp "${FIXTURE_DIR}/${VALID_DOCUMENT_NAME}" "${target}"
    while [[ "$#" -ge 2 ]]; do
        replace_once "${target}" "${target}.next" "$1" "$2"
        mv "${target}.next" "${target}"
        shift 2
    done
}

make_documents() {
    local line_break=$'\n'
    local mapping_indent="              "
    mkdir -p "${WORK_DIR}/documents"
    make_variant unknown-member "${MANAGEMENT_ENTRY}" "${UNKNOWN_MEMBER_ENTRY}"
    make_variant invalid-enum '"hash-mode": "source"' '"hash-mode": "bogus"'
    make_variant out-of-range \
        "\"ping-count\": 3,${line_break}            \"success-threshold\": 2," \
        "\"ping-count\": 256,${line_break}            \"success-threshold\": 2,"
    make_variant missing-mandatory "${MANAGEMENT_ENTRY}" '{ "name": "enmgmt0" }'
    make_variant explicit-null '"forced-dscp": 8' '"forced-dscp": null'
    make_variant policy-changed '"fw-mark-prio": 100,' '"fw-mark-prio": 110,'
    make_variant policy-added \
        "\"ietf-ip:ipv6\": {${line_break}          \"goodkind-mwan-steering:translation\": { \"mode\": \"native\" }" \
        '"ietf-ip:ipv6": { "goodkind-mwan-steering:translation": { "mode": "ietf-nat:nptv6", "nptv6": { "internal-prefix": "2001:db8:b01::/60", "external-source": "configured", "external-prefix": "2001:db8:beef:100::/60" } }'
    make_variant policy-removed \
        '"mode": "ietf-nat:nptv6",' '"mode": "native"' \
        "\"nptv6\": {${line_break}${mapping_indent}\"internal-prefix\": \"2001:db8:b01::/60\",${line_break}${mapping_indent}\"external-source\": \"configured\",${line_break}${mapping_indent}\"external-prefix\": \"2001:db8:beef:200::/60\"${line_break}            }" \
        ''
    make_variant rule-removed '{ "protocol": "tcp", "port": 22 },' ''
    make_variant rule-changed '"fw-mark": 3,' '"fw-mark": 5,'
    make_variant rule-added \
        '{ "protocol": "tcp", "port": 22 },' \
        '{ "protocol": "tcp", "port": 22 }, { "protocol": "udp", "port": 161 },'
    make_variant rule-reordered \
        "{ \"external\": \"203.0.113.2\", \"internal\": \"192.0.2.2\" },${line_break}${mapping_indent}{ \"external\": \"203.0.113.3\", \"internal\": \"192.0.2.3\" }" \
        "{ \"external\": \"203.0.113.3\", \"internal\": \"192.0.2.3\" },${line_break}${mapping_indent}{ \"external\": \"203.0.113.2\", \"internal\": \"192.0.2.2\" }"
    make_variant set-changed \
        '"pinned-v4": ["198.51.100.0/24"],' \
        '"pinned-v4": ["198.51.100.0/24", "203.0.113.128/25"],'
}

# Whitespace normalization lets diagnostic comparisons match wrapped text.
normalized_log() {
    local log_file="$1"
    tr -s '[:space:]' ' ' <"${log_file}"
}

# The first blank line ends the rendered resource change.
resource_block() {
    local log_file="$1"
    local address="$2"
    awk -v marker="# ${address} " '
        index($0, marker) != 0 { printing = 1 }
        printing && $0 ~ /^[[:space:]]*$/ { exit }
        printing { print }' "${log_file}"
}

add_excerpt() {
    local name="$1"
    local text="$2"
    printf -- '--- %s ---\n%s\n' "${name}" "${text}" >>"${WORK_DIR}/excerpts.txt"
}

expect_rejection() {
    local name="$1"
    local command="$2"
    local document="$3"
    local summary="$4"
    local token="$5"
    local log_file="${WORK_DIR}/logs/${name}.log"
    local text
    local status=0
    if [[ "${command}" == "apply" ]]; then
        run_tofu "${log_file}" apply -auto-approve -input=false -no-color -var "network_file=${document}" || status=$?
    else
        run_tofu "${log_file}" plan -input=false -no-color -var "network_file=${document}" || status=$?
    fi
    if [[ "${status}" -eq 0 ]]; then
        fail_case "${name}" "tofu ${command} accepted the document" "${log_file}"
    fi
    text="$(normalized_log "${log_file}")"
    if [[ "${text}" != *"Error: ${summary}"* ]]; then
        fail_case "${name}" "tofu ${command} failed without the diagnostic ${summary}" "${log_file}"
    fi
    if [[ "${text}" != *"${token}"* ]]; then
        fail_case "${name}" "the diagnostic lacks the token ${token}" "${log_file}"
    fi
    record_case "${name}: tofu ${command} failed with status ${status}; Error: ${summary}; token ${token}"
}

expect_update() {
    local name="$1"
    local document="$2"
    local absent="$3"
    shift 3
    local log_file="${WORK_DIR}/logs/${name}.log"
    local block token summary
    local status=0
    run_tofu "${log_file}" plan -input=false -no-color -var "network_file=${document}" || status=$?
    if [[ "${status}" -ne 0 ]]; then
        fail_case "${name}" "tofu plan exited with status ${status}" "${log_file}"
    fi
    if grep -q -e "must be replaced" -e "will be destroyed" "${log_file}"; then
        fail_case "${name}" "the plan replaces or destroys a resource" "${log_file}"
    fi
    if ! summary="$(grep -F "Plan: 0 to add, " "${log_file}")" || [[ "${summary}" != *" 0 to destroy."* ]]; then
        fail_case "${name}" "the plan summary adds or destroys a resource" "${log_file}"
    fi
    if ! grep -q -F "# ${CONFIG_ADDRESS} will be updated in-place" "${log_file}"; then
        fail_case "${name}" "the plan does not update ${CONFIG_ADDRESS} in place" "${log_file}"
    fi
    block="$(resource_block "${log_file}" "${CONFIG_ADDRESS}")"
    for token in "$@"; do
        if [[ "${block}" != *"${token}"* ]]; then
            fail_case "${name}" "the rendered change lacks ${token}" "${log_file}"
        fi
    done
    if [[ -n "${absent}" && "${block}" == *"${absent}"* ]]; then
        fail_case "${name}" "the rendered change contains ${absent}" "${log_file}"
    fi
    add_excerpt "${name}" "${block}"
    record_case "${name}: update in place; ${summary}; tokens $*"
}

expect_no_changes() {
    local name="$1"
    local document="$2"
    local log_file="${WORK_DIR}/logs/${name}.log"
    local line
    local status=0
    run_tofu "${log_file}" plan -input=false -no-color -var "network_file=${document}" || status=$?
    if [[ "${status}" -ne 0 ]]; then
        fail_case "${name}" "tofu plan exited with status ${status}" "${log_file}"
    fi
    if ! line="$(grep -F "No changes." "${log_file}")"; then
        fail_case "${name}" "the plan reports changes" "${log_file}"
    fi
    add_excerpt "${name}" "${line}"
    record_case "${name}: ${line}"
}

run_rejection_cases() {
    local documents="${WORK_DIR}/documents"
    local case_line
    expect_rejection unknown-member plan "${documents}/unknown-member.json" "${SCHEMA_ERROR}" "unknown-member"
    # Reuse the unknown-member rejection text for the "schema rejection:" evidence line.
    case_line="${EVIDENCE_CASES[${#EVIDENCE_CASES[@]} - 1]#unknown-member: }"
    EVIDENCE_REJECTION="${case_line%; token *}"
    expect_rejection invalid-enum plan "${documents}/invalid-enum.json" "${SCHEMA_ERROR}" "bogus"
    expect_rejection out-of-range plan "${documents}/out-of-range.json" "${SCHEMA_ERROR}" "256"
    expect_rejection missing-mandatory plan "${documents}/missing-mandatory.json" "${SCHEMA_ERROR}" 'Mandatory node "type"'
    expect_rejection explicit-null plan "${documents}/explicit-null.json" "${SCHEMA_ERROR}" "uint8 value"
    expect_rejection semantic-host-bits plan "${FIXTURE_DIR}/invalid.json" "${REJECTED_ENTRY_ERROR}" \
        "must be a canonical same-family network prefix"
}

run_update_cases() {
    local documents="${WORK_DIR}/documents"
    local log_file="${WORK_DIR}/logs/base-apply.log"
    local status=0
    run_tofu "${log_file}" apply -auto-approve -input=false -no-color \
        -var "network_file=${FIXTURE_DIR}/${VALID_DOCUMENT_NAME}" || status=$?
    if [[ "${status}" -ne 0 ]]; then
        EVIDENCE_BASE_APPLY="failed with status ${status}"
        show_log "${log_file}"
        fail "tofu apply exited with status ${status} for the base document."
    fi
    EVIDENCE_BASE_APPLY="succeeded; $(grep -F "Apply complete!" "${log_file}")"

    expect_update route-changed "${FIXTURE_DIR}/changed.json" "" \
        '~ "enwebpass0|ipv4|198.18.0.0/24"' '"203.0.113.1" -> "203.0.113.9"'
    expect_update route-added "${FIXTURE_DIR}/added.json" "" '+ "enwebpass0|ipv4|198.18.3.0/24"'
    expect_update policy-changed "${documents}/policy-changed.json" "" '~ "att|ipv4|fwmark"' '100 -> 110'
    expect_update policy-added "${documents}/policy-added.json" "" \
        '+ "att|ipv6|source"' '"2001:db8:beef:100::/60"'
    expect_update rule-changed "${documents}/rule-changed.json" "" \
        '~ "inet|mangle|prerouting|provider-mark|enmbrains0"' '3 -> 5'
    expect_update rule-added "${documents}/rule-added.json" "" \
        '+ "inet|filter|input|management-service|udp,161,ipv4"' '~ rule_order'
    expect_update rule-reordered "${documents}/rule-reordered.json" "firewall_rules" \
        '~ "ip|nat|prerouting"' '~ rule_order' \
        '- "ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.2"'
    expect_update route-removed "${FIXTURE_DIR}/removed.json" "" \
        '- "enwebpass0|ipv4|198.18.2.0/24"' '} -> null'
    expect_update policy-removed "${documents}/policy-removed.json" "" \
        '- "webpass|ipv6|source"' '} -> null'
    expect_update rule-removed "${documents}/rule-removed.json" "" \
        '- "inet|filter|input|management-service|tcp,22,ipv4"' \
        '- "inet|filter|input|management-service|tcp,22,ipv6"' '~ rule_order' '} -> null'
    expect_update set-changed "${documents}/set-changed.json" "" \
        '~ "inet|mangle|att_pinned_v4"' '+ "203.0.113.128/25"'
    expect_no_changes formatting-only "${FIXTURE_DIR}/reformatted.json"
}

# Validation waits until apply when document content is unknown during plan.
run_deferral_case() {
    local name="deferred-content"
    local document="${WORK_DIR}/documents/unknown-member.json"
    local log_file="${WORK_DIR}/logs/${name}-plan.log"
    local read_line="# ${DATA_ADDRESS} will be read during apply"
    local status=0
    MODULE_DIR="${WORK_DIR}/deferred"
    mkdir -p "${MODULE_DIR}"
    cp "${FIXTURE_DIR}/deferred.tf" "${MODULE_DIR}/main.tf"
    run_tofu "${WORK_DIR}/logs/${name}-init.log" init -input=false -no-color || status=$?
    if [[ "${status}" -ne 0 ]]; then
        fail_case "${name}" "tofu init exited with status ${status}" "${WORK_DIR}/logs/${name}-init.log"
    fi
    run_tofu "${log_file}" plan -input=false -no-color -var "network_file=${document}" || status=$?
    if [[ "${status}" -ne 0 ]]; then
        fail_case "${name}" "tofu plan exited with status ${status} for unknown content" "${log_file}"
    fi
    if ! grep -q -F "${read_line}" "${log_file}"; then
        fail_case "${name}" "the plan does not defer ${DATA_ADDRESS}" "${log_file}"
    fi
    if grep -q -F "${SCHEMA_ERROR}" "${log_file}"; then
        fail_case "${name}" "the plan reports ${SCHEMA_ERROR} for unknown content" "${log_file}"
    fi
    add_excerpt "${name}" "$(resource_block "${log_file}" "${DATA_ADDRESS}")"
    record_case "${name}: tofu plan succeeded; ${read_line}"
    expect_rejection "${name}-apply" apply "${document}" "${SCHEMA_ERROR}" "unknown-member"
}

main() {
    local archive="${PROVIDER_PACKAGE:-}"
    local version="${PROVIDER_VERSION:-}"
    local package_dir binary valid_document tofu_version fixture
    local status=0

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
    for fixture in "${FIXTURE_FILES[@]}"; do
        if [[ ! -f "${FIXTURE_DIR}/${fixture}" ]]; then
            fail "The fixture directory ${FIXTURE_DIR} does not contain ${fixture}."
        fi
    done

    WORK_DIR="$(mktemp -d)"
    MODULE_DIR="${WORK_DIR}/module"
    mkdir -p "${WORK_DIR}/home" "${WORK_DIR}/tmp" "${WORK_DIR}/logs" "${MODULE_DIR}"
    package_dir="${WORK_DIR}/mirror/${PROVIDER_ADDRESS}/${version}/${PLATFORM_OS}_${PLATFORM_ARCH}"
    extract_archive "${archive}" "${package_dir}" "${version}"
    binary="${package_dir}/${PROVIDER_BINARY}_v${version}"

    check_dependencies "${binary}" "${package_dir}"
    record_stamped_commit "${binary}" "${EXPECTED_COMMIT:-}"
    record_build_tools

    printf 'provider_installation {\n  filesystem_mirror {\n    path    = "%s"\n    include = ["%s"]\n  }\n}\n' \
        "${WORK_DIR}/mirror" "${PROVIDER_ADDRESS}" >"${WORK_DIR}/tofurc"
    cp "${FIXTURE_DIR}/main.tf" "${WORK_DIR}/module/main.tf"

    make_documents

    run_tofu "${WORK_DIR}/version.log" version || status=$?
    if [[ "${status}" -ne 0 ]]; then
        show_log "${WORK_DIR}/version.log"
        fail "tofu version exited with status ${status}."
    fi
    tofu_version="$(sed -n '1p' "${WORK_DIR}/version.log")"
    EVIDENCE_TOFU="${TOFU} (${tofu_version})"

    run_tofu "${WORK_DIR}/init.log" init -input=false -no-color || status=$?
    if [[ "${status}" -ne 0 ]]; then
        EVIDENCE_INIT="failed with status ${status}"
        show_log "${WORK_DIR}/init.log"
        fail "tofu init exited with status ${status}."
    fi
    if ! grep -q -F "${PROVIDER_ADDRESS} v${version}" "${WORK_DIR}/init.log"; then
        EVIDENCE_INIT="succeeded without installing ${PROVIDER_ADDRESS} v${version}"
        show_log "${WORK_DIR}/init.log"
        fail "tofu init did not report installing ${PROVIDER_ADDRESS} v${version}."
    fi
    EVIDENCE_INIT="succeeded; installed ${PROVIDER_ADDRESS} v${version} from the filesystem mirror"

    run_tofu "${WORK_DIR}/valid.log" plan -input=false -no-color -var "network_file=${valid_document}" || status=$?
    if [[ "${status}" -ne 0 ]]; then
        EVIDENCE_VALID_PLAN="failed with status ${status}"
        show_log "${WORK_DIR}/valid.log"
        fail "tofu plan exited with status ${status} for the valid document."
    fi
    if ! grep -q -F "mwan_network_config.gateway will be created" "${WORK_DIR}/valid.log"; then
        EVIDENCE_VALID_PLAN="succeeded without planning mwan_network_config.gateway"
        show_log "${WORK_DIR}/valid.log"
        fail "tofu plan did not report creating mwan_network_config.gateway for the valid document."
    fi
    EVIDENCE_VALID_PLAN="succeeded; $(grep -F "Plan:" "${WORK_DIR}/valid.log")"

    run_rejection_cases
    run_update_cases
    run_deferral_case
}

main "$@"
