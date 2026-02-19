# RWX Directory Guide

## WARNING: RWX reads ALL .yml files in this directory

RWX automatically picks up every `.yml` file in `.rwx/` as a workflow
definition. **Do NOT store backup, draft, or old workflow files here** —
they will trigger duplicate runs on every push.

If you need to keep old versions for reference, move them outside `.rwx/`
(e.g. `docs/rwx-archive/`) or use git history instead.

## Cache Control Lock Files

These lock files control when cached dependencies are rebuilt:

- **build-deps.lock** - System packages (touch weekly)
- **rust-version.lock** - Rust toolchain (touch monthly)
- **oj-version.lock** - OJ binary from oddjobs (touch when updating)
- **helm-version.lock** - Helm CLI (touch to update version)
- **release-deps.lock** - Release dependencies + gh CLI (touch monthly)
- **toolchain-version.lock** - Toolchain image deps (touch to update)

To force a cache rebuild, simply touch the corresponding lock file:
```bash
touch .rwx/rust-version.lock
git add .rwx/rust-version.lock
git commit -m "chore: rebuild Rust toolchain cache"
```

## Version Sync Requirements

Some versions appear in multiple pipeline files and must be updated together:

### Go version
Update `.go-version` — the `golang/install` package in ci.yml, image.yml, and release.yml
all read `go-version: "1.25"` and should match. The image-toolchain task resolves the
exact patch version dynamically from `go env GOVERSION`.

Files to update: `.go-version`, `ci.yml`, `image.yml`, `release.yml`

### Rust version
Hardcoded in `image.yml` (rust task) and `image-toolchain` task.

Files to update: `image.yml` (two locations), touch `rust-version.lock`

### golangci-lint version
Hardcoded in `ci.yml` golangci-lint task (`v2.9.0`).

Files to update: `ci.yml`
