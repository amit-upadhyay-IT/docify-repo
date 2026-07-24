#!/bin/sh
# image-checks.sh verifies the release invariants of a built docify-repo container image.
#
# This is CI/release tooling, not part of the tool's execution path, so using a POSIX shell
# here does not weaken the "no shell script in the execution path" guarantee of the image
# itself (its ENTRYPOINT is the compiled binary). The script is never copied into the image.
#
# Usage:
#   scripts/image-checks.sh <image-ref> [platform]
#
#   <image-ref>  Image to check, e.g. ghcr.io/example/docify-repo@sha256:...
#   [platform]   Optional docker platform, e.g. linux/amd64 or linux/arm64. When set, every
#                container is run with --platform so both release architectures can be checked
#                on one host via QEMU.
#
# Exit status is non-zero if any check fails. Each check prints a PASS/FAIL line.
set -eu

IMAGE="${1:-${DOCIFY_IMAGE:-}}"
PLATFORM="${2:-${DOCIFY_PLATFORM:-}}"

if [ -z "$IMAGE" ]; then
    echo "usage: $0 <image-ref> [platform]" >&2
    exit 2
fi

PLATFORM_ARGS=""
if [ -n "$PLATFORM" ]; then
    PLATFORM_ARGS="--platform $PLATFORM"
fi

failures=0

pass() { printf 'PASS  %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# run_tool executes the image through its real ENTRYPOINT (the binary).
run_tool() {
    # shellcheck disable=SC2086
    docker run --rm $PLATFORM_ARGS \
        --cap-drop ALL --security-opt no-new-privileges \
        "$IMAGE" "$@"
}

# run_shell executes a POSIX-shell probe inside the image for inspection only. It overrides the
# entrypoint; it does not reflect how the tool runs in production.
run_shell() {
    # shellcheck disable=SC2086
    docker run --rm $PLATFORM_ARGS \
        --cap-drop ALL --security-opt no-new-privileges \
        --entrypoint /bin/sh "$IMAGE" -c "$1"
}

echo "== docify-repo image checks =="
echo "image:    $IMAGE"
echo "platform: ${PLATFORM:-<host default>}"
echo

# 1. The default runtime user is non-root.
uid="$(run_shell 'id -u' | tr -d '[:space:]')"
if [ "$uid" != "0" ] && [ -n "$uid" ]; then
    pass "runs as non-root user (uid=$uid)"
else
    fail "expected a non-root default user, got uid='$uid'"
fi

# 2. No language runtime, package manager, cloud CLI, or GitHub CLI is present. These would
#    enlarge the attack surface and are explicitly excluded by the image design. The whole scan
#    runs in one container to keep the check fast.
forbidden="go gofmt node nodejs npm npx yarn pnpm python python3 pip pip3 ruby java javac php \
perl gcc cc make cargo rustc dotnet aws gcloud az kubectl helm gh hub apt apt-get yum dnf"
present="$(run_shell 'present=""; for b in '"$forbidden"'; do
    if command -v "$b" >/dev/null 2>&1; then present="$present $b"; fi
done; printf "%s" "$present"')"
if [ -z "$present" ]; then
    pass "no language runtime, cloud CLI, or GitHub CLI on PATH"
else
    fail "unexpected tooling present:$present"
fi

# 3. Git is present for diff, rename detection, and object publishing.
if run_tool_version="$(run_shell 'git --version')"; then
    pass "git available ($run_tool_version)"
else
    fail "git is not available"
fi

# 4. A writable temporary directory exists and 5. Git object-publishing plumbing (commit +
#    fast-forward push to a local remote) works as the non-root user. This mirrors what the
#    publisher does over the network, minus the remote transport, so it proves the runtime user
#    can create objects, write refs, and push without hooks or global configuration.
publish_probe='
set -e
export HOME=/tmp GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0
work="$(mktemp -d)"
[ -w "$work" ] || { echo "temp dir not writable" >&2; exit 1; }
git init -q "$work/repo"
git init -q --bare "$work/remote.git"
cd "$work/repo"
printf "hello\n" > file.txt
git -c user.name=docify -c user.email=docify@example.invalid -c commit.gpgSign=false \
    -c core.hooksPath=/dev/null add file.txt
git -c user.name=docify -c user.email=docify@example.invalid -c commit.gpgSign=false \
    -c core.hooksPath=/dev/null commit -q -m init
git remote add origin "$work/remote.git"
git push -q origin HEAD:refs/heads/main
git ls-remote "$work/remote.git" refs/heads/main | grep -q refs/heads/main
'
if run_shell "$publish_probe"; then
    pass "writable temp dir and Git object-publishing plumbing work as non-root"
else
    fail "Git object-publishing plumbing failed as the non-root user"
fi

# 6. The entrypoint is the binary and runs.
if run_tool --help >/dev/null 2>&1; then
    pass "entrypoint binary runs (--help)"
else
    fail "entrypoint binary did not run --help successfully"
fi

# 7. The configured entrypoint is the binary in exec form (no shell wrapper).
entrypoint="$(docker inspect --format '{{json .Config.Entrypoint}}' "$IMAGE" 2>/dev/null || echo '')"
case "$entrypoint" in
    *'/usr/local/bin/docify-repo'*)
        case "$entrypoint" in
            *'/bin/sh'*|*'/bin/bash'*) fail "entrypoint wraps a shell: $entrypoint" ;;
            *) pass "entrypoint is the binary in exec form" ;;
        esac
        ;;
    '') printf 'SKIP  entrypoint inspection (image not present locally)\n' ;;
    *) fail "unexpected entrypoint: $entrypoint" ;;
esac

echo
if [ "$failures" -ne 0 ]; then
    echo "$failures check(s) failed"
    exit 1
fi
echo "all checks passed"
