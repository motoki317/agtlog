---
name: release
description: Use when cutting an agtlog release from a vX.Y.Z tag.
---

# Cutting an agtlog release

A `vX.Y.Z` tag runs `.github/workflows/release.yaml`, which uses GoReleaser to
publish Linux and macOS archives for amd64 and arm64. The tag is the version;
there is no source version file to edit.

## Runbook

1. Confirm `main` is clean, pushed, and green in CI.
2. Run `just pre-commit`, `just test-race`, and `just nix-build`.
3. Validate `.goreleaser.yaml` with `goreleaser check`.
4. Create a local `vX.Y.Z` tag and run `goreleaser release --snapshot
   --skip=publish --clean`; delete the local tag after the dry run.
5. Push `main`, then create and push the lightweight release tag only after the
   user explicitly authorizes publishing.
6. Verify the Release workflow and all four archives plus `checksums.txt`.

Never move a published tag. Cut the next patch release instead.
