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
CI workflows are **manual-only** (CLI trigger). Release/image/helm workflows auto-trigger on `v*` tags.
```yaml
# Manual-only workflow (ci.yml pattern):
on:
  cli:
    init:
      commit-sha: ${{ event.git.sha }}

# Auto-trigger on tags only (helm.yml pattern):
on:
  github:
    push:
      if: ${{ starts-with(event.git.tag, 'v') }}
      init:
        commit-sha: ${{ event.git.sha }}
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
# Run a workflow (opens browser)
rwx run .rwx/ci.yml --init commit-sha=$(git rev-parse HEAD) --open

# Run and wait for result (blocks until done, returns exit status)
rwx run .rwx/ci.yml --init commit-sha=$(git rev-parse HEAD) --wait

# Run specific task(s) only
rwx run .rwx/ci.yml --init commit-sha=$(git rev-parse HEAD) --target test

# Get JSON output (includes RunID for chaining with results/logs)
rwx run .rwx/ci.yml --init commit-sha=$(git rev-parse HEAD) --wait --output json
# → {"RunID":"<hex>","RunURL":"https://cloud.rwx.com/mint/...","ResultStatus":"succeeded"}
```

#### Getting logs and errors from a run:
```bash
# Step 1: Get the run ID (from `rwx run --output json` or from the web UI URL)
RUN_ID="44c4349c741349888a6ab3fc541bd18d"

# Step 2: Get results — shows pass/fail and lists failed task IDs
rwx results $RUN_ID
# Output on failure:
#   Run result status: failed
#   # Failed task:
#   You can pull the logs for this task using `rwx logs <task-id>`
#   - code.git-clone (task-id: fcc468e0b6c26e6c3a22b3d949d78c22)

# Step 3: Download logs for a specific failed task
rwx logs <task-id>                              # saves to .rwx/downloads/
rwx logs <task-id> --output-file /tmp/fail.log  # save to specific file
rwx logs <task-id> --open                       # download and open immediately
rwx logs <task-id> --auto-extract               # extract zip (for group tasks)

# Full pipeline: run, check, get error logs
rwx run .rwx/ci.yml --init commit-sha=$(git rev-parse HEAD) --wait --output json \
  | jq -r .RunID | xargs rwx results
```

**Where to find task IDs:**
- From `rwx results <run-id>` output (shown for failed tasks)
- From the RWX web UI URL: when viewing a task, the ID is in the URL
- Group/parent task IDs download a .zip with all child logs; leaf task IDs download a single .log

**Web UI:** `https://cloud.rwx.com/mint/alfred-jean-labs/runs/<run-id>`

#### Cross-workflow dispatch:
```bash
curl -X POST https://api.rwx.com/v1/dispatches \
  -H "Authorization: Bearer $RWX_ACCESS_TOKEN" \
  -d '{"repository":"other-repo","workflow":"rebuild","parameters":{...}}'
```

### Release Strategy (IMPORTANT)
All releasing goes through gastown's `platform-versions.env` — do NOT cut separate beads or coop releases for deployment.
- Bump versions in `~/gastown3/platform-versions.env` (BEADS_VERSION, COOP_VERSION, PLATFORM_VERSION)
- Push to gastown main → docker.yml auto-builds CalVer-tagged images
- Update helm chart values to reference the CalVer tag (e.g., `2026.0218.0`)
- Deploy via `helm upgrade`
- beads tags use CalVer (e.g., `2026.0218.0`) — binary version now matches deployed image tags

### Our Pipelines (.rwx/)

| File | Purpose | Triggers | Key tasks |
|------|---------|----------|-----------|
| `ci.yml` | Main CI | **CLI only** (manual) | build, lint, test, version-check, beads-guard |
| `image.yml` | OCI image builds | push(main/tags), PR(build only) | build-bd (Go), build-oj (Rust), image-bd, image-toolchain |
| `release.yml` | GoReleaser releases | push(CalVer tags `20*`), CLI | release, dispatch-gastown |
| `helm.yml` | Helm chart lint only | **CalVer tags `20*` + CLI** (manual) | helm-lint |

**Versioning: CalVer (all-in)**
- All deployed images and charts use CalVer: `YYYY.MMDD.N` (e.g., `2026.0218.0`) — 3-part for semver/GoReleaser compatibility
- CalVer is driven by gastown's `platform-versions.env` — single source of truth
- Gastown `docker.yml` publishes CalVer images (push-bd, push-agent, push-controller) and CalVer charts (helm-publish-charts)
- Beads `release.yml` now creates GitHub releases with CalVer tags (`2026.0218.0`) — binary versions match image tags
- Beads repo does NOT publish images or charts to GHCR — gastown handles all of that
- Chart version in beads `helm/bd-daemon/Chart.yaml` is the source template; gastown overrides version/appVersion to CalVer at publish time

### Running CI Manually

CI does **not** auto-run on push or PR. Run it yourself before releasing:

```bash
# Full CI (build + lint + test)
rwx run .rwx/ci.yml --init commit-sha=$(git rev-parse HEAD) --wait

# Single task (e.g., just tests)
rwx run .rwx/ci.yml --init commit-sha=$(git rev-parse HEAD) --target test --wait

# Helm lint only
rwx run .rwx/helm.yml --init commit-sha=$(git rev-parse HEAD) --wait
```

#### Lock files for cache control:
- `build-deps.lock` — system packages (touch weekly)
- `rust-version.lock` — Rust toolchain (touch monthly)
- `oj-version.lock` — OJ binary rebuild
- `helm-version.lock` — Helm CLI version
- `goreleaser-version.lock` — GoReleaser version
- `release-deps.lock` — release dependencies (touch monthly)
- `toolchain-version.lock` — toolchain image deps
- `system-deps.lock` — CI system packages

#### RWX images (local testing only — NOT pushed to GHCR):
- **image-bd** — Minimal daemon: `bd` + `oj` binaries, runs as `beads` user, entrypoint `bd daemon start`
- **image-toolchain** — Full dev environment: Go 1.25, Rust 1.93, Node.js 22, plus `bd` + `oj`, runs as `agent` user

GHCR image publishing is handled by gastown `docker.yml` with CalVer tags.

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

**Nice-to-have:**
2. Document in README-LOCKS.md: when updating Go version, must update ci.yml, image.yml, release.yml, AND .go-version together.
3. Consider webhook triggers for gastown dispatch (instead of curl to RWX API).

**Previously fixed (kept for reference):**
- ~~`build`/`lint` implicit dep on `code`~~ — now explicit in `use` arrays
- ~~`helm.yml` PR status-checks~~ — now `tasks: [helm-lint]`
- ~~`beads-changes-check` unconditional~~ — now `if: ${{ init.trigger == 'pr' }}`
- ~~coverage.out artifact~~ — added to ci.yml test task
- ~~image.yml build-bd filter mismatch~~ — aligned with ci.yml filter
