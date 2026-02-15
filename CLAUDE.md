# Beads Project Memory

## RWX CI/CD Platform

### What is RWX?
RWX (rwx.com) is a CI/CD platform for high-velocity teams. Their CI product was formerly called **Mint** but is now just "RWX CI". Key differentiators:
- **DAG-based execution** with true parallelization — tasks form a directed acyclic graph
- **Content-based caching** — cache keys are computed from actual task inputs (files, commands), not timestamps
- **Right-sized compute** — each task gets its own isolated environment
- Supports GitHub and GitLab triggers

### Configuration Format
Workflows live in `.rwx/` directory. **Every `.yml` file in `.rwx/` is auto-discovered as a workflow** — never store drafts/backups there.

#### Top-level structure:
```yaml
on:          # Event triggers (github push/PR, cli, dispatch)
base:        # Base container image + RWX config
tasks:       # Array of task definitions (the DAG)
```

#### Task definition fields:
- **key** — unique task identifier
- **run** — shell script to execute
- **call** — invoke a reusable RWX package (e.g., `git/clone 2.0.3`, `golang/install 1.2.0`)
- **with** — parameters passed to `call` packages
- **use** — declare task dependencies (array or single key). Task won't run until all `use` tasks complete. Filesystem from `use` tasks is available.
- **filter** — file glob patterns for cache invalidation. If none of the listed files changed, the task is a cache hit and skips execution. Supports `!` for exclusions.
- **if** — conditional execution using expressions like `${{ init.ref-name != 'pr' }}`
- **env** — environment variables for the task
- **outputs.artifacts** — declare output files that downstream tasks can reference via `${{ tasks.<key>.artifacts.<artifact-key> }}`
- **cache: false** — opt out of caching
- **cache-ttl** — e.g., `7 days`, `1 hour`

#### Triggers (`on` block):
```yaml
on:
  github:
    push:
      if: ${{ event.git.branch == 'main' }}
      init:
        commit-sha: ${{ event.git.sha }}
      status-checks:
        name: CI
    pull_request:
      init:
        commit-sha: ${{ event.git.sha }}
      status-checks:
        - tasks: [build, lint, test]
          name: CI
  cli:
    init:
      commit-sha: ${{ event.git.sha }}
```

#### Caching model:
- Automatic content-based caching — same inputs = cache hit even if upstream tasks re-ran
- **filter** globs control what files are considered inputs for caching
- Non-deterministic commands (e.g., `date`) still get cached — user must manage
- Lock files (`.rwx/*.lock`) are the pattern for controlling external dependency caching — touch the file to force rebuild

#### CLI usage:
```bash
rwx run .rwx/ci.yml --init commit-sha=$(git rev-parse HEAD) --open
```

#### Cross-workflow dispatch:
```bash
curl -X POST https://api.rwx.com/v1/dispatches \
  -H "Authorization: Bearer $RWX_ACCESS_TOKEN" \
  -d '{"repository":"other-repo","workflow":"rebuild","parameters":{...}}'
```

### Our Pipelines (.rwx/)

| File | Purpose | Triggers | Key tasks |
|------|---------|----------|-----------|
| `ci.yml` | Main CI | push(main), PR | build, lint, test, version-check, beads-guard |
| `image.yml` | OCI image builds | push(main/tags), PR(build only) | build-bd (Go), build-oj (Rust), image-bd, image-toolchain |
| `release.yml` | GoReleaser releases | push(v* tags), CLI | release, dispatch-gastown |
| `helm.yml` | Helm chart lint+publish | push(main/tags), PR | helm-lint, helm-publish to ghcr.io |

#### Lock files for cache control:
- `build-deps.lock` — system packages (touch weekly)
- `rust-version.lock` — Rust toolchain (touch monthly)
- `oj-version.lock` — OJ binary rebuild
- `helm-version.lock` — Helm CLI version
- `goreleaser-version.lock` — GoReleaser version
- `release-deps.lock` — release dependencies (touch monthly)
- `toolchain-version.lock` — toolchain image deps
- `system-deps.lock` — CI system packages

#### Images built:
- **image-bd** — Minimal daemon: `bd` + `oj` binaries, runs as `beads` user, entrypoint `bd daemon start`
- **image-toolchain** — Full dev environment: Go 1.25, Rust 1.93, Node.js 22, plus `bd` + `oj`, runs as `agent` user

#### Fork context:
- RWX pipelines clone from `groblegark/beads` (fork)
- Go module path is `github.com/steveyegge/beads` (upstream)
- `.golangci.yml` exclusion paths use upstream module path — this is correct

### RWX Platform Updates (as of Feb 2026)
- **Jan 2026**: New base config — `base: { image: ubuntu:24.04, config: rwx/base 1.0.0 }` (we already use this)
- **Jan 2026**: RWX CLI v3.0.0 — new tools for coding agents to iterate on CI changes
- **Jan 2026**: `rwx logs` command for pulling task logs from CLI
- **Feb 2026**: Webhook triggers — can trigger runs from third-party webhooks
- **Nov 2025**: `rwx run` v2 — run builds from terminal with local code changes
- **Dec 2025**: Zero-config OpenTelemetry observability

### Known Pipeline Issues (reviewed Feb 2026)

**Should fix:**
1. Go version inconsistency — `golang/install` gets `1.25` (latest patch) but `image-toolchain` hardcodes `go1.25.7`. These can diverge.
2. `build` and `lint` tasks in ci.yml have implicit dependency on `code` via transitive chain through `git-config` → `go-deps` → `code`. Making `use: [code, ...]` explicit would be clearer and safer.
3. `helm.yml` PR status-checks don't explicitly list tasks — should be `tasks: [helm-lint]` to exclude `helm-publish` from PR runs.
4. `beads-changes-check` runs unconditionally but is only meaningful for PRs.

**Nice-to-have:**
5. Add `outputs.artifacts` for coverage.out in ci.yml test task (for trend tracking).
6. Align `image.yml` build-bd filter with ci.yml's more granular build filter (includes `!cmd/bd/version.go` exclusion).
7. Document in README-LOCKS.md: when updating Go version, must update ci.yml, image.yml, release.yml, AND .go-version together.
8. Consider webhook triggers for gastown dispatch (instead of curl to RWX API).
