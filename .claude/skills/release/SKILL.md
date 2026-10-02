---
name: release
description: Use when cutting an agtlog release from a vX.Y.Z tag.
---

# Cutting an agtlog release

A pushed `v*` tag runs `.github/workflows/release.yaml`. GoReleaser then publishes `tar.gz`
archives for Linux and macOS on amd64 and arm64. The tag is the version. No source file holds a
version number.

GoReleaser is not in the dev shell. Run it as `nix run nixpkgs#goreleaser -- <args>`.

## Runbook

1. Check that `main` is clean, pushed, and green in CI.
2. Run `just pre-commit`, `just test-race`, and `just nix-build`.
3. Run `nix run nixpkgs#goreleaser -- check`.
4. Create the local tag with `git tag vX.Y.Z`. Then run
   `nix run nixpkgs#goreleaser -- release --snapshot --clean`. `--snapshot` skips publishing.
5. Delete the local tag with `git tag -d vX.Y.Z`.
6. Stop and ask the user to authorize publishing. Do not continue without that approval.
7. Create a lightweight tag on the pushed `main` commit, and push it:
   `git tag vX.Y.Z main && git push origin vX.Y.Z`.
8. Find the Release run with `gh run list --workflow release.yaml --limit 1`. Then run
   `gh run watch <run-id>` until it succeeds.
9. Run `gh release view vX.Y.Z --json assets --jq '.assets[].name'`. Check that it lists
   `checksums.txt` and four archives named `agtlog-X.Y.Z-<os>-<arch>.tar.gz`, one for each of
   `linux` and `darwin` with `amd64` and `arm64`.

If a published release is wrong, cut the next patch release. Never move a published tag.
