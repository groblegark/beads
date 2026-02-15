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
- **goreleaser-version.lock** - GoReleaser (touch to update)
- **release-deps.lock** - Release dependencies (touch monthly)
- **toolchain-version.lock** - Toolchain image deps (touch to update)

To force a cache rebuild, simply touch the corresponding lock file:
```bash
touch .rwx/rust-version.lock
git add .rwx/rust-version.lock
git commit -m "chore: rebuild Rust toolchain cache"
```
