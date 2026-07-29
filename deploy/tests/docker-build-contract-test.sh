#!/usr/bin/env bash

set -euo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${TEST_DIR}/../.." && pwd)"

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

assert_frontend_workspace_config_copied() {
    local dockerfile="$1"
    local expected='COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./'

    grep -Fqx "${expected}" "${dockerfile}" ||
        fail "${dockerfile} must copy pnpm-workspace.yaml before frozen install"
}

assert_frontend_workspace_config_copied "${REPO_ROOT}/Dockerfile"
assert_frontend_workspace_config_copied "${REPO_ROOT}/deploy/Dockerfile"

printf 'Docker build contract tests passed.\n'
