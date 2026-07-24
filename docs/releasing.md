# Releasing

The container image is built and published by
[`.github/workflows/release.yml`](../.github/workflows/release.yml), triggered by pushing an
annotated semantic-version tag.

```bash
git tag -a v0.1.0 -m "docify-repo v0.1.0"
git push origin v0.1.0
```

## What the pipeline does

1. **Multi-architecture build.** Buildx cross-compiles the static binary for `linux/amd64` and
   `linux/arm64` from a single builder (CGO is disabled, so no emulation of the compiler is
   needed) and pushes one multi-arch manifest to `ghcr.io/<owner>/<repo>`.
2. **Immutable tags.** The image is tagged with the full version, the `major.minor` series, and
   the long commit SHA. `latest` is intentionally not published; consumers pin by digest.
3. **SBOM and provenance.** A per-architecture SPDX SBOM and max-mode SLSA provenance are
   attached to the manifest, and the SBOM is also uploaded as a downloadable artifact.
4. **Vulnerability scan.** The pushed digest is scanned; the release fails on fixable high or
   critical vulnerabilities.
5. **Signing.** The image digest is signed with keyless cosign using the workflow's OIDC
   identity, and a build-provenance attestation is pushed to the registry.
6. **Image-size gate.** The compressed size of each architecture is recorded in the job summary
   and compared against `IMAGE_SIZE_LIMIT_BYTES`; exceeding it fails the release.
7. **Image checks.** [`scripts/image-checks.sh`](../scripts/image-checks.sh) runs against the
   pushed digest on both `linux/amd64` and `linux/arm64` (arm64 via QEMU), verifying the
   non-root user, the absence of language runtimes / cloud CLIs / the GitHub CLI, a writable
   temporary directory, and Git object-publishing plumbing.

## Digest pinning is a release gate

The `Dockerfile` and the workflows reference base images and actions by human-readable tag so
they stay readable. **Before production use, repin them to immutable `@sha256` digests:**

- The `FROM golang:1.26.3-alpine3.22` and `FROM alpine:3.22` lines in the `Dockerfile`. Pinning
  the runtime base by digest also freezes the Alpine package index, which makes the installed
  `git` and `ca-certificates` versions reproducible.
- Every `uses:` action in `release.yml` and `docify-repo.yml`.
- The image reference in the reference workflow (`ghcr.io/example/docify-repo:0.1.0` →
  `...@sha256:<digest>`).

Resolve a pushed manifest's digest with:

```bash
docker buildx imagetools inspect ghcr.io/<owner>/<repo>:0.1.0 --format '{{json .Manifest.Digest}}'
```

## Verifying a release

```bash
# Verify the signature and its OIDC identity.
cosign verify \
  --certificate-identity-regexp "https://github.com/<owner>/<repo>/.github/workflows/release.yml@.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/<owner>/<repo>@sha256:<digest>

# Inspect the attached SBOM and provenance attestations.
cosign download sbom ghcr.io/<owner>/<repo>@sha256:<digest>
cosign verify-attestation --type slsaprovenance \
  --certificate-identity-regexp "https://github.com/<owner>/<repo>/.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/<owner>/<repo>@sha256:<digest>

# Run the runtime invariant checks locally against the digest.
scripts/image-checks.sh ghcr.io/<owner>/<repo>@sha256:<digest>
```

## Local build

```bash
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=dev --build-arg REVISION="$(git rev-parse HEAD)" \
  -t docify-repo:dev .
```

Building for a single local architecture and running the checks:

```bash
docker build -t docify-repo:dev .
scripts/image-checks.sh docify-repo:dev
```
