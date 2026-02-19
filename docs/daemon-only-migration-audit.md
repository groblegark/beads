# Daemon-Only Migration Audit

**Epic:** hq-18vg2m — Remove local database code path from bd CLI
**Date:** 2026-02-19
**Author:** obsidian-3 (polecat)

## Overview

The bd CLI has two data access modes: **direct storage** (local Dolt/SQLite) and
**daemon RPC**. All K8s agents use daemon-only mode. The local path causes bugs
(hq-88n9th, hq-s2wwl3) and adds maintenance burden. This audit inventories all
local-db code paths that must change.

## Phase 1: Enforce Daemon-Required (High Priority)

### 1.1 PersistentPreRun Fallback — `cmd/bd/main.go`

**Lines 756–881** — When daemon connection fails AND `BD_DAEMON_HOST` is not set,
the CLI falls back to opening a local Dolt database directly.

Key decision point at **line 763**:
```go
if daemonClient == nil && !forceDirectMode && !isDaemonCommand && !isCrossRig {
```

**Change:** Make this a hard error. Remove lines 777–881 (direct storage init).

**Globals to remove from main.go (lines 31–69):**
- `store storage.Storage` (line 34) — Replace all usages with daemon RPC
- `autoFlushEnabled` (line 46)
- `flushMutex`, `storeMutex`, `storeActive` (lines 47–49)
- `flushFailureCount`, `lastFlushError` (lines 50–51)
- `flushManager *FlushManager` (line 54)
- `skipFinalFlush` (line 60)
- `autoImportEnabled` (line 63)

**Imports to remove from main.go:**
- `"github.com/steveyegge/beads/internal/storage"`
- `"github.com/steveyegge/beads/internal/storage/factory"`

### 1.2 Force-Direct-Mode Commands — `cmd/bd/main.go`

These commands set `forceDirectMode = true` and bypass daemon:

| Command | Line | Reason | Daemon-Only Plan |
|---------|------|--------|-----------------|
| `doctor` | 583 | Diagnostic tool | Keep direct mode (needs raw db access) |
| `restore` | 589 | Git history access | Keep direct mode |
| `--profile` | 472 | CPU profiling | Can use daemon — remove exemption |
| `edit` | 491 | No remote daemon | Remove — edit should use daemon |

### 1.3 `dbPath` Variable — 34 references in main.go

**Line 32:** `dbPath string` — Used for local database discovery.

**Remaining uses after Phase 1:**
- Hook runner initialization (needs CWD fallback, already fixed in hq-s2wwl3)
- Doctor/restore commands (keep for direct-mode exemptions)
- Daemon startup (`bd daemon start` reads dbPath for its own storage)

**Can be scoped down** to only daemon-internal and doctor/restore paths.

---

## Phase 2: Remove JSONL Sync System (High Priority)

### 2.1 Auto-Flush — `cmd/bd/autoflush.go`

Entire file can be deleted. Exports issues to `.beads/*.jsonl` after writes.

**Related:**
- `FlushManager` struct and goroutines
- `autoflush_test.go`
- `flushMutex`, `storeMutex`, `storeActive` globals in main.go
- `skipFinalFlush` flag

### 2.2 Auto-Import — `cmd/bd/autoimport.go`

Entire file can be deleted. Imports from `.beads/*.jsonl` on startup (after git pull).

**Related:**
- `autoimport_test.go`
- `autoImportEnabled` global
- Import trigger in PersistentPreRun (main.go:869–880)

### 2.3 Import/Export Commands

These are user-facing commands that may still be useful for backup/migration:
- `cmd/bd/import.go` — Keep (operates via daemon RPC)
- `cmd/bd/export.go` — Keep (operates via daemon RPC)

The auto-* variants are the ones to remove.

---

## Phase 3: Remove Direct Store Usage from Commands (Medium Priority)

### 3.1 Commands with `store != nil` / `store == nil` Guards

**31 files, 54 occurrences** check `store` directly. These represent dual-path
code where the command works differently with local vs daemon storage.

**High-traffic files (4+ checks):**
| File | Count | Purpose |
|------|-------|---------|
| `autoflush.go` | 4 | Removed in Phase 2 |
| `swarm.go` | 4 | Swarm management |
| `dep.go` | 4 | Dependency tracking |
| `formula.go` | 3 | Formula/molecule management |
| `linear.go` | 3 | Linear sync |
| `list.go` | 3 | List/query issues |
| `daemon.go` | 3 | Daemon management |

**Per-file changes needed:**
- Files that check `store == nil` as error: Remove guard, rely on `requireDaemon()`
- Files that check `store != nil` for local-only logic: Remove the local branch
- Test files: Remove local-store test helpers

### 3.2 `requireDaemon()` Calls — 123 invocations

These are already correct — they enforce daemon mode. After Phase 1, the daemon
will always be available (or the CLI exits), so these checks become no-ops.

**Can be left in place** as defensive guards, or removed in a cleanup pass.

---

## Phase 4: Remove Storage Backend Imports from CLI (Medium Priority)

### 4.1 Storage Factory — `internal/storage/factory/`

- `factory.go` — Used by daemon internally, but also by CLI PersistentPreRun
- `factory_dolt.go` — Dolt backend registration
- After Phase 1, these are only used by `bd daemon start`

### 4.2 Storage Interface

`internal/storage/storage.go` — The `storage.Storage` interface is used by:
- Daemon internals (keep)
- CLI commands accessing `store` directly (remove in Phase 3)

### 4.3 Dolt Backend — `internal/storage/dolt/`

20+ files. Remains as daemon-internal only. No CLI changes needed.

---

## Phase 5: Cleanup (Low Priority)

### 5.1 Local Database Discovery — `internal/beads/beads.go`

Functions that search for local `.beads/*.db` files:
- `FindDatabasePath()` — Walks directory tree
- `FindBeadsDir()` — Searches for `.beads/` directory
- `findDatabaseInBeadsDir()` — Checks metadata.json
- `FollowRedirect()` / `GetRedirectInfo()` — Symlink handling

**Keep for:** Daemon startup, doctor command
**Remove from:** CLI startup path (PersistentPreRun)

### 5.2 Test Infrastructure

Many tests create local databases. These need to be migrated to either:
- Use in-memory daemon for testing
- Use mock RPC client
- Keep local-only for unit tests of storage internals

**Test files with direct store usage:**
- `cli_fast_test.go`
- `list_watch_test.go`
- `cli_coverage_show_test.go`
- `init_test.go`
- `import_*_test.go` (6 files)
- `deletion_tracking_test.go`
- `daemon_rpc_roundtrip_test.go`
- Various doctor/ tests

### 5.3 Flags to Remove/Deprecate

| Flag | Location | Action |
|------|----------|--------|
| `--db` | main.go | Remove or repurpose for daemon config |
| `--no-auto-flush` | main.go | Remove |
| `--no-auto-import` | main.go | Remove |
| `--force-direct` | main.go | Keep for doctor/restore only |

---

## Migration Strategy

**Recommended order:**

1. **Phase 1** — Enforce daemon-required in PersistentPreRun
   - Single file change (main.go)
   - Immediately surfaces any commands that incorrectly rely on direct store
   - Keep forceDirectMode for doctor/restore

2. **Phase 2** — Delete auto-flush and auto-import
   - Clean deletion of 2 files + test files
   - Removes background goroutines and race-condition-prone code

3. **Phase 3** — Remove `store` from commands one-by-one
   - Can be done incrementally, command by command
   - Each command that checked `store != nil` just uses `requireDaemon()` + RPC

4. **Phase 4** — Remove storage imports from CLI binary
   - Reduces binary size (Dolt is large)
   - Move to daemon-only compilation unit

5. **Phase 5** — Clean up tests and discovery functions
   - Lowest risk, can be done anytime

## Impact Summary

| Metric | Current | After Migration |
|--------|---------|-----------------|
| Files with `store` checks | 31 | 0 (CLI), daemon-internal only |
| Auto-flush/import code | ~600 lines | 0 |
| CLI binary deps | Dolt, SQLite | RPC client only |
| Fallback paths | 3 modes | 1 mode (daemon) |
| `dbPath` references | 34 in main.go | ~5 (daemon/doctor only) |
