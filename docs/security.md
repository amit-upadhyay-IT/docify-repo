# Security and Privacy

`docify-repo` reads a repository's Git-tracked source, sends selected source to a language
model to draft documentation, and opens a pull request with the result. This document describes
the data it handles, the trust boundaries it enforces, and how the released image is hardened.

## Data leaving the repository

**Selected source code is transmitted to the configured LLM provider.** To generate
documentation the tool sends the content of the source files it selects — production code,
contracts, configuration, and the diff range for changed components — to the LLM endpoint
configured through `DOCIFY_LLM_BASE_URL` and `DOCIFY_LLM_MODEL`. That endpoint may be a hosted
service, a gateway, or a self-hosted model. Operators are responsible for choosing a provider
whose data-handling terms are acceptable for the repository's contents; a private or
self-hosted endpoint keeps source within the operator's boundary.

Requests are sent **only** to the configured LLM endpoint and to the GitHub API. No other
network destination is contacted.

What is **not** sent:

- Files excluded by path filters, size limits, binary detection, or `.docify.yml`.
- Files matching the non-overridable secret-path denial list.
- Source-control credentials — the GitHub token is never visible to the LLM request builder.

The structured run report contains change ranges, affected components, and counts. It does not
contain source text, prompts, responses, or credentials by default.

## Credential isolation

Two independent credentials are used and kept separate:

| Variable | Purpose | Path to subprocess |
| --- | --- | --- |
| `DOCIFY_LLM_API_KEY` | Authenticates LLM requests | Attached only to the outbound HTTPS request by the LLM adapter |
| `DOCIFY_GITHUB_TOKEN` | `contents: write`, `pull-requests: write` on the target repo (`github-pr` mode only) | Supplied to Git only through a scoped child-process `GIT_ASKPASS` callback |

- The GitHub token reaches Git **never as a command-line argument and never inside a remote
  URL**. Git invokes this same binary as its askpass program, which reads the token from the
  environment and writes it only to the pipe Git reads. The token is never logged.
- Pull-request operations use Go's HTTP client. The image contains **no GitHub CLI**.
- The LLM request builder has no reference to the GitHub token, and the model is given no tools
  and no access to process environment variables.

Configuration records only whether each secret variable is *present*; token values are not
copied into application command models.

## Executing repository content

The tool never executes repository code or lifecycle hooks.

- Every Git subprocess runs non-interactively with hooks (`core.hooksPath=/dev/null`), commit
  signing (`commit.gpgSign=false`), external diff (`diff.external=`), the filesystem monitor,
  optional locks, replacement objects, and the pager disabled, and with system and global Git
  configuration neutralized (`GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL=/dev/null`).
- Only Git-tracked, filtered text files are read.
- Source comments and strings are treated as untrusted prompt content. Because the model has no
  tools, prompt injection cannot make it run commands; schema validation and local rendering
  prevent a model response from selecting filesystem paths or altering component identity,
  scope, ordering, state, or publishing.
- Generated writes are limited to tool-owned paths (`docs/generated/**` and
  `.docify/state.json`).

## Runtime container

- **Non-root.** The image declares a fixed non-root user (UID/GID `10001`). The reference
  workflow additionally runs the container as the checkout's own UID/GID, drops all Linux
  capabilities (`--cap-drop ALL`), and forbids privilege escalation
  (`--security-opt no-new-privileges`).
- **Minimal.** The runtime image contains only the compiled binary, `git`, and
  `ca-certificates`. There is no language runtime, no package manager for target repositories,
  no cloud CLI, no GitHub CLI, and no shell script in the execution path — the entrypoint is the
  binary itself.
- **Mount only the workspace.** Mount the repository workspace, not the Docker socket or host
  credentials.
- **Writable temporary directory.** Set `HOME` to a writable location (the reference workflow
  uses `/tmp`); Git object-publishing operations run there as the non-root user.

These runtime invariants are enforced by [`scripts/image-checks.sh`](../scripts/image-checks.sh)
on both target architectures during release, and statically by the `internal/release` test
suite.

## Supply chain

Released images are published with:

- A per-architecture **SBOM** (SPDX) attached to the manifest and available as a build artifact.
- A **vulnerability scan** that fails the release on fixable high or critical findings.
- A keyless **cosign signature** over the image digest and **SLSA build provenance**.
- **Immutable tags** (semantic version and revision) with no floating `latest`, addressed by
  digest.

In production, pin both the GitHub Actions and the image by immutable `@sha256` digest. See
[releasing.md](releasing.md) for the release and verification procedure.

## Operator checklist

- Choose an LLM endpoint whose data handling is acceptable for this repository's source.
- Store `DOCIFY_LLM_API_KEY` and `DOCIFY_GITHUB_TOKEN` as CI secrets, never in `.docify.yml`.
- Grant the GitHub token only `contents: write` and `pull-requests: write` on the target
  repository, or supply a GitHub App installation token where policy forbids `GITHUB_TOKEN`
  from opening pull requests.
- Pin the image and actions by digest before production use.
- Keep the vulnerability-scan and image-size gates enabled in the release pipeline.
