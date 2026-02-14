# RWX Cache Control Lock Files

These lock files control when cached dependencies are rebuilt:

- **build-deps.lock** - System packages (touch weekly)
- **rust-version.lock** - Rust toolchain (touch monthly)
- **oj-version.lock** - OJ binary from oddjobs (touch when updating)
- **helm-version.lock** - Helm CLI (touch to update version)
- **goreleaser-version.lock** - GoReleaser (touch to update)
- **release-deps.lock** - Release dependencies (touch monthly)

To force a cache rebuild, simply touch the corresponding lock file:
```bash
touch .rwx/rust-version.lock
git add .rwx/rust-version.lock
git commit -m "chore: rebuild Rust toolchain cache"
```
