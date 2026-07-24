// Package release holds release-engineering invariant checks for the container image and CI
// workflows. It contains no runtime code: the tests in this package statically assert the
// Phase 7 hardening properties (multi-stage minimal image, non-root user, no language runtime
// or GitHub CLI, multi-architecture build, SBOM/scan/signing pipeline, and the documented
// transmission of selected source to the configured LLM provider) so they can be verified by
// `go test ./...` without a Docker daemon.
package release
