# Releasing tclientlib and tclient

The Go module and `cmd/tclient` use the same `vMAJOR.MINOR.PATCH` tag. A
prerelease adds a suffix such as `v0.1.0-rc.1`. Separate CLI tags would break the
normal Go module version selection used by `go install .../cmd/tclient@latest`.

Release commits must already be merged into `master`. PR checks include native
Linux and Windows tests on Go 1.20.x and stable, the Linux PTY/TCP suite, and a
GoReleaser snapshot build of all six archives. Snapshots are review artifacts,
not GitHub Releases; they have read-only repository permissions and expire
after seven days.

## Start a release

Update [CHANGELOG.md](../CHANGELOG.md) with the new version's user-visible
changes and migration steps before merging the release commit. Generated
commit lists supplement these notes; they do not replace API upgrade guidance.

In GitHub, open **Actions → Release → Run workflow**, select `master`, and
enter the version tag. For example, use `v0.1.0` for a stable release or
`v0.1.0-rc.1` for a release candidate.

The workflow tests the exact commit selected when it starts. After the tests
pass, it creates an annotated tag if that tag does not already exist. An
existing tag must point to the same commit. It then builds and publishes from
that commit, even if `master` advances while the workflow is running. A tag
created with the workflow's `GITHUB_TOKEN` does not start another workflow;
the same run continues the release.

Alternatively, push an annotated tag from a reviewed checkout:

```bash
git switch master
git pull --ff-only
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Choose one entry point for a version. Both run the same tests and release job.
The tag-push path uses the existing tag and never moves it.

## What gets published

GoReleaser OSS is pinned to `v2.19.0`. Actions are pinned to full commit SHAs.
Only the release job receives repository write and OIDC/attestation permissions;
it uses GitHub's short-lived `GITHUB_TOKEN`, without a personal token or signing
key. Builds disable CGO, use `-trimpath`, and embed the version, full commit,
build time, Go compiler version, and target platform.

| Platform | Architectures | Archive |
| --- | --- | --- |
| Linux | amd64, arm64 | `tclient_VERSION_linux_ARCH.tar.gz` |
| macOS | amd64, arm64 | `tclient_VERSION_darwin_ARCH.tar.gz` |
| Windows | amd64, arm64 | `tclient_VERSION_windows_ARCH.zip` |

Each archive contains the binary, README, changelog, and documentation guides. The release also
contains `checksums.txt` with SHA-256 hashes. Windows binaries support
redirected sessions; interactive terminal support currently targets Unix.
Cross-compilation does not replace platform runtime testing: native CI runs
on Linux and Windows amd64, and the PTY suite runs on Linux.

Publication is ordered:

1. Run the test matrix for the release commit.
2. Build all archives and upload them to a draft release with generated notes.
3. Create GitHub build attestations for the archive checksums.
4. Publish the completed draft. Prerelease tags remain prereleases and are not
   marked as the latest stable release.

This order also works when repository release immutability is enabled. It
avoids publishing a partial set of assets before all uploads and attestations
have succeeded. Do not publish the intermediate draft manually.

## Verify or retry

Follow the [CLI installation guide](cli.md#install-a-release) to verify
checksums and attestations. `tclient --version` reports the compiled metadata.
The release uses the current stable Go compiler and records it in the binary;
it does not claim byte-identical results across different toolchain versions or
build times.

If building, uploading, or attesting fails, fix the failure and rerun the
original workflow run. Any existing draft for that tag can be replaced, but
the tag must still identify the tested commit. A successful published release
is never overwritten by this workflow. Publish a new version for changes to
already released code or assets.

For local packaging checks, install GoReleaser `v2.19.0` and run:

```bash
goreleaser check
goreleaser release --snapshot --clean
```

Snapshot mode builds and packages without publishing. Output is written to the
ignored `dist/` directory. The repository's `.goreleaser.yaml` remains the
single definition of the release targets and archive layout.
