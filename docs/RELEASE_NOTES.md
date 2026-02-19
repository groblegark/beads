# Platform Release Notes

Release notes for the beads platform. Each release uses CalVer (`YYYY.MDD.N`).

---

## 2026.218.9 (unreleased)

### Agent Watch TUI Overhaul

The `bd agent watch` TUI has been completely redesigned with a rich, interactive experience.

**Split-pane layout** (bd-4be5t, bd-i19bt)
- Horizontal split at terminals >= 100 columns: compact agent list on left, detail panel on right
- Collapsed single-pane mode for narrow terminals with inline expansion (enter to toggle)
- Responsive breakpoints: 35-col list at normal width, 40-col at >= 150 columns
- Compact 2-line list items with colored state dots: Working (green), Active (blue), Idle (yellow), Stale (red), Crashed (red X)

**Rich detail panel** (bd-czaca, bd-s6fiy)
- 6 sections: Identity, Status, Current Work, Git Context, Activity, Unclaimed Tasks
- Shows task ID/title, parent epic, git branch/repo, session duration, event rate
- Unclaimed tasks section with priority color coding

**Keyboard shortcuts** (bd-edx55)
- `n`/`N`: Jump between working agents
- `s`: Cycle sort modes (default, name, idle time, event rate)
- `/`: Filter agents by name (live search)
- `o`: Show full task detail in scrollable overlay
- `h`/`l` or arrows: Switch pane focus

**Recent activity timeline** (bd-9y9ba, bd-1xgft)
- New `agent_recent_events` RPC endpoint with per-actor ring buffer (50 events)
- Timeline in detail panel shows last 15 events with relative timestamps
- Human-readable summaries extracted from tool input (bash commands, file paths, grep patterns)
- Color-coded by event type: tool events in teal, errors in red, lifecycle in muted
- New CLI: `bd agent recent-events <actor> [--limit=N]`

### Agent Lifecycle

**Live terminal peek** (beads-tsk-port_agent_terminal_peek_activity_go)
- Slack bot can now show live terminal output from agent Coop sidecars
- Enables real-time visibility into what agents are doing

**bd yield** (hq-h80x9n)
- `bd done` renamed to `bd yield` to better reflect its purpose (yield session, wait for events)
- `bd done` is now the canonical teardown command (close beads, commit, push)
- Clean separation: `yield` = pause and listen, `done` = finish session

**Decision auto-assignment** (bd-isufm)
- When a human selects a decision option that references a bead ID, the bead is automatically assigned to the requesting agent
- Reduces friction in the checkpoint workflow

### Bug Fixes

- Fix BD_DAEMON_HOST interference in 9 tests that assume local filesystem mode (bd-iyscn)

---

## 2026.218.8

- feat: `bd agent orphans` command for Witness patrol auto-nuke (bd-7njn3)
- fix: auto-supersede stale pending decisions from same agent (bd-ni0br)
- feat: cleanup_status on agent bead for proactive polecat GC (bd-6eflz)

## 2026.218.7

- fix: 3 cmd/bd tests fail when BD_DAEMON_HOST is set (bd-16000)

## 2026.218.6

- fix: sync export tests fail due to Dolt error message wrapping (bd-c2zh0)

## 2026.218.5

- feat: label-based agent resolution as fallback in mail router (bd-p1mt)
- feat: bus watch TUI phases B-G (ring buffer, filtering, JSON highlighting, coloring, search, stats dashboard)
- feat: configurable Slack notification verbosity
- feat: local dispatch fallback when daemon unavailable
- feat: decision thread persistence and iteration chains
- fix: respect explicit --assignee on status transitions
- fix: BEADS_DIR env var routing confusion
- feat: quench.toml quality gate config
- chore: remove deprecated CLI aliases and dead config fields
- feat: helm chart template validation test
