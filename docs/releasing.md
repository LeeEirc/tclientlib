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

### Recommended: merge a version PR

1. Set `VERSION` to the intended tag on a single line, for example
   `v0.1.0` or `v0.1.0-rc.1`, and update the changelog.
2. Open a PR describing the version, release highlights, and any migration
   steps. Review its checks and snapshot archives.
3. Merge the reviewed PR into `master`. The push that changes `VERSION` starts
   the `Release` workflow automatically.

**The initial `VERSION` value is `v0.1.0`. Merging the PR that introduces this
file starts the `v0.1.0` release after the release checks pass.**

Opening or updating a PR only runs checks and creates snapshot artifacts.
Ordinary pushes to `master` that leave `VERSION` unchanged do not start a
release. A direct push to `master` that changes `VERSION` also starts one, so
treat a version-file change as an explicit request to publish.

`VERSION` records the requested release version, not whether publication has
completed. It does not change the version reported by ordinary developer
builds. Release binaries receive metadata from GoReleaser; ordinary builds and
`go install` continue to use their embedded module/VCS metadata where available.

### Other entry points

For a manual run, open **Actions → Release → Run workflow**, select `master`,
and enter the version tag. The entered tag is used instead of `VERSION`.

Alternatively, push an annotated tag from a reviewed checkout already merged
into `master`:

```bash
git switch master
git pull --ff-only
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The pushed tag is used instead of `VERSION`; this path never creates or moves
a tag. Choose one entry point for each version.

### Commit and tag guarantees

All three entry points resolve and validate the version, then run the same
tests and release job. The workflow uses the exact commit selected when it
starts: a version-file release reads `VERSION` from that commit, not from a
later `master`. Testing, tagging, building, and publication remain on that
commit even if `master` advances during the run.

After the tests pass, version-file and manual releases create an annotated
tag if it does not already exist. An existing tag must point to the same
commit. A tag created with the workflow's `GITHUB_TOKEN` does not start another
workflow; the same run continues the release.

Publication is serialized by the resolved version across all three entry
points. A published version cannot be overwritten, even if another entry point
is used to request it again.

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

Each archive contains the binary, README, changelog, and documentation guides.
The release also contains `checksums.txt` with SHA-256 hashes. Windows binaries
support redirected sessions; interactive terminal support currently targets Unix.
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
