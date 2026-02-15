---
sidebar_position: 5
title: Sync Failures
description: Recover from sync failures
---

# Sync Failures Recovery

:::info
Dolt handles synchronization automatically. Most sync failures described on this page should be rare. If you experience data transfer issues, use `bd export` and `bd import`.
:::

This runbook helps you recover from sync failures.

## Symptoms

- Sync hangs or times out
- Network-related error messages
- "failed to push" or "failed to pull" errors
- Daemon not responding

## Diagnosis

```bash
# Check daemon status
bd daemon status

# Check sync state
bd status

# View daemon logs
cat .beads/daemon.log | tail -50
```

## Solution

**Step 1:** Stop the daemon
```bash
bd daemon stop
```

**Step 2:** Check for lock files
```bash
ls -la .beads/*.lock
# Remove stale locks if daemon is definitely stopped
rm -f .beads/*.lock
```

**Step 3:** Force a fresh sync
```bash
bd doctor --fix
```

**Step 4:** Restart daemon
```bash
bd daemon start
```

**Step 5:** Verify data transfer works
```bash
# Use export/import for manual data transfer
bd export
bd info
```

## Common Causes

| Cause | Solution |
|-------|----------|
| Network timeout | Retry with better connection |
| Stale lock file | Remove lock after stopping daemon |
| Corrupted state | Use `bd doctor --fix` |
| Git conflicts | See [Merge Conflicts](/recovery/merge-conflicts) |

## Prevention

- Ensure stable network before sync
- Let sync complete before closing terminal
- Use `bd daemon stop` before system shutdown
