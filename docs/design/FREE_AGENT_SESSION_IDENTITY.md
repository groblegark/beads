# Free-Agent Session Identity

> **Issue**: bd-zp6v9 | **Status**: Design | **Date**: 2026-02-15
> **Prerequisite for**: bd-xtahx (Inbox routing), bd-c9oxj.4 (free-agent bootstrap)

## Problem Statement

When multiple Claude Code sessions run on the same machine, they all resolve to
the same actor identity. This makes it impossible to:

- Route inbox messages to a specific session (`bd inbox push --to=???`)
- Distinguish "my work" from "your work" in `bd news` (all sessions filter
  identically)
- Attribute issues to specific sessions (`created_by` is the same for all)
- Address agents in `bd send` (all agents are "matthewbaker")

### How gastown solves this

Gastown assigns identity at spawn time via environment variables:

```
gt spawn polecat --name=toast --rig=gastown
  -> sets GT_ROLE=gastown/polecats/toast
  -> sets BD_ACTOR=gastown/polecats/toast
  -> starts tmux session with these env vars baked in
```

The orchestrator (gt) is the naming authority. Each agent gets a unique,
human-meaningful name before it starts. The agent never needs to name itself.

### What a free agent has

A free agent is a Claude Code session started by a human (or another tool) with
no gastown infrastructure. Here's what's available:

| Signal | Unique per session? | Human-meaningful? | Stable across restart? | Available everywhere? |
|--------|:---:|:---:|:---:|:---:|
| `$USER` | No | Yes | Yes | Yes |
| `git user.name` | No | Yes | Yes | Yes |
| `BD_ACTOR` env | No (unless set) | Yes (if set) | No (env-scoped) | No |
| `TERM_SESSION_ID` | Yes (per tab) | No (UUID) | No (new tab = new) | macOS only |
| Claude project session UUID | Yes | No (UUID) | Yes (within project) | Yes |
| PID of Claude process | Yes | No | No (changes per command) | Yes |
| Working directory (`$PWD`) | Sometimes | Yes | Yes | Yes |
| `.beads/` database path | Sometimes | Partially | Yes | Yes |

**Nothing available is both unique per session AND human-meaningful.**

## Approaches

### Approach A: Self-Naming via SessionStart Hook

A `SessionStart` hook detects "no identity" and tells the agent to name itself.

**Flow:**
1. Hook fires on session start
2. Hook checks: is `BD_ACTOR` set? Is there a cached session name?
3. If no: outputs instruction telling Claude to run `bd register --name=<pick>`
4. Claude picks a name and runs the command
5. `bd register` writes name to `.runtime/actors/{session-key}`
6. Subsequent `bd` commands read the cached name

**Session key**: `TERM_SESSION_ID` on macOS, or hash of `(PWD, PID-of-parent-shell)`.

**Pros:**
- Human-meaningful names (Claude picks good ones)
- Works without daemon
- No env var injection needed

**Cons:**
- Requires Claude cooperation (must follow the hook instruction)
- First command in session has wrong identity (before registration)
- Race condition: multiple commands before registration completes
- Session key is fragile (TERM_SESSION_ID is macOS-only, PID changes)

### Approach B: Daemon-Assigned Identity

The daemon is the naming authority. First contact from a new session triggers
name assignment.

**Flow:**
1. Agent runs any `bd` command (e.g., `bd ready`)
2. CLI detects no cached identity for this session
3. CLI calls daemon RPC: `RegisterSession(session_key, preferred_name)`
4. Daemon checks registry: is this session_key already known?
   - Yes: return existing name
   - No: assign new name (auto-generate or use preferred_name)
5. CLI caches the assigned name in `.runtime/actors/{session-key}`
6. All subsequent commands use cached name

**Session key options:**
- `TERM_SESSION_ID` (macOS terminal tabs)
- `CLAUDE_SESSION_ID` (if Claude Code exposes one — it doesn't today)
- Hash of `(PPID, TTY)` — parent process ID + terminal device
- Fallback: generate a random session key on first `bd` command, cache it in a
  session-local temp file

**Name generation:**
- Default: `{user}-{n}` where n increments (matthewbaker-1, matthewbaker-2)
- Or: `{user}-{adjective}` from a word list (matthewbaker-falcon, matthewbaker-cedar)
- Or: user provides via `BD_ACTOR` env (override)

**Pros:**
- Daemon is natural naming authority (already tracks activity)
- Works for any number of sessions
- Names are stable within a session
- No hook cooperation required — happens on first command
- Daemon can track all active sessions (for `bd inbox push --to=all`)

**Cons:**
- Requires a running daemon (standalone mode can't do this)
- Session key problem remains (what uniquely identifies a session?)
- First command has latency for registration RPC
- Daemon restart could lose session registry (needs persistence)

### Approach C: Per-Working-Directory Identity

Each git worktree or project directory gets its own identity via local config.

**Flow:**
1. User creates worktree: `git worktree add ../beads-alice`
2. In that worktree: `bd config set actor alice`
3. All sessions in `../beads-alice/` are "alice"
4. All sessions in `../beads-bob/` are "bob"

**Pros:**
- Simple, works today (just needs `bd config set actor` to work)
- No daemon required
- Stable identity tied to filesystem location
- Natural for "one agent per worktree" pattern

**Cons:**
- Doesn't work for multiple sessions in the same directory
- Requires manual setup per worktree
- Not suitable for "3 agents on same repo" scenario
- Worktree creation is heavyweight

### Approach D: Explicit BD_ACTOR in Shell Environment

User manually sets `BD_ACTOR` per terminal before starting Claude.

**Flow:**
1. Terminal 1: `export BD_ACTOR=alice && claude`
2. Terminal 2: `export BD_ACTOR=bob && claude`
3. Terminal 3: `export BD_ACTOR=carol && claude`

**Pros:**
- Works today, zero code changes
- User controls naming
- Stable within terminal session

**Cons:**
- Manual setup every time
- Easy to forget
- Defeats "zero-config" goal
- Not feasible for automated/scripted agent launches

### Approach E: Claude Code Session ID as Identity Seed

Request that Claude Code expose a `CLAUDE_SESSION_ID` environment variable.

**Flow:**
1. Claude Code sets `CLAUDE_SESSION_ID=bb667f01-5ac5-49e6-98fe-b767006d4c2b`
2. `bd` hashes this to a short human-friendly name: `session-falcon-7b3c`
3. Or: daemon maps session IDs to names in a registry

**Pros:**
- Truly unique per session
- Stable across compactions within same session
- Available to all tools (env var)

**Cons:**
- Requires upstream Claude Code change (dependency on Anthropic)
- UUIDs aren't human-friendly (need mapping layer)
- Not available today

### Approach F: Hybrid — Layered Identity Resolution (Recommended)

Combine multiple approaches into a fallback chain, similar to how `getActor()`
already works but with session-awareness.

**Resolution chain:**
```
1. --actor flag            (explicit override, highest priority)
2. BD_ACTOR env var        (set by gastown, scripts, or user)
3. GT_ROLE env var         (gastown-managed agents)
4. Daemon session registry (daemon-assigned, auto-generated)
5. bd config actor         (per-directory persistent config)
6. git user.name           (developer default)
7. $USER                   (system fallback)
```

**The key addition is step 4: daemon session registry.**

When the daemon is running:
- First `bd` command registers the session with a session key
- Daemon assigns a unique name: `{base}-{n}` (e.g., `matthewbaker-2`)
- Name is cached locally and reused for all commands in that session
- Daemon tracks all active sessions for routing

When the daemon is NOT running (standalone mode):
- Falls through to steps 5-7
- Multiple sessions in same directory share identity (acceptable for standalone)
- User can disambiguate with `BD_ACTOR` env var

**Session key derivation (the hard part):**

The session key must be:
- Unique per Claude Code session
- Stable across commands within the same session
- Derivable without special env vars

**Proposed session key**: Hash of `(PPID, TTY, PWD)` — parent PID, terminal
device, and working directory. This is:
- Unique: different terminals have different TTYs, different PPIDs
- Stable: within a terminal session, all three are constant
- Portable: works on macOS and Linux
- No special env vars needed

```bash
# Example session key derivation
tty=$(tty 2>/dev/null || echo "notty")
ppid=$PPID
pwd=$PWD
session_key=$(echo "${ppid}:${tty}:${pwd}" | sha256sum | head -c 16)
```

**Fallback for subagents**: Claude Code subagents (Task tool) inherit the parent's
env but may have different PPIDs. They should inherit the parent's BD_ACTOR
(already works via env inheritance) or the parent should set BD_ACTOR before
spawning.

**Edge cases:**
- `cd` changes PWD → session key changes. Mitigate: use the project root
  (`.beads/` parent) instead of `$PWD`
- Terminal multiplexer (tmux inside terminal) → PPID changes. Mitigate: use
  `TERM_SESSION_ID` when available (macOS), fall back to `(PPID, TTY)`
- SSH sessions → no TTY sometimes. Mitigate: use `SSH_CONNECTION` + PID as key
- Docker/container → consistent within container. No issue.

## Recommendation

**Approach F (Hybrid)** with daemon session registry as the primary mechanism.

### Why

1. **Backwards compatible**: Existing `BD_ACTOR` and `GT_ROLE` env vars still
   work. Gastown agents are unaffected.

2. **Zero-config for common case**: Single developer, single session — falls
   through to `git user.name`, works perfectly (same as today).

3. **Auto-scales for multi-session**: Daemon assigns unique names automatically
   when it detects multiple sessions. No manual setup.

4. **Progressive enhancement**: Works without daemon (falls to config/git),
   better with daemon (unique names, routing).

5. **Inbox-ready**: Daemon knows all active sessions and their names, enabling
   `bd inbox push --to=matthewbaker-2` and `bd inbox push --to=all`.

### Implementation sketch

**Phase 1: Session registry in daemon** (~100 lines)
- New RPC: `OpSessionRegister` — input: session_key, base_name. Output:
  assigned_name.
- Daemon maintains in-memory map: `session_key → assigned_name`
- Persist to `registry.json` (already exists, currently empty `[]`)
- Name generation: `{base_name}-{n}` where n is lowest unused number
- Stale session cleanup: prune entries not seen in 30 minutes

**Phase 2: CLI integration** (~30 lines)
- On first command: derive session key, call `OpSessionRegister`
- Cache result in env-like mechanism or temp file
- `getActor()` chain updated to check daemon registry between env vars and
  config

**Phase 3: bd whoami** (~30 lines)
- Shows resolved identity, source (which step in the chain), session key
- Shows daemon registration status

### What this unblocks

- `bd inbox push --to=matthewbaker-2` — route to specific session
- `bd news` — correctly filters per-session activity
- `bd send alice "hey"` — disambiguate multiple sessions
- `bd list --assignee=me` — returns work assigned to THIS session
- `InboxItem.AgentName` — has a meaningful, unique value to route to

## Open Questions

1. **Name persistence across compaction/restart**: If Claude session compacts or
   restarts (new conversation, same project), should it get the same name? The
   session key would change (new PID). Options: (a) new name each time, (b) let
   user claim a name via `bd config set actor`, (c) daemon remembers by project
   path.

2. **Name collision with gastown agents**: If a free agent auto-names
   "matthewbaker-1" and a gastown agent is also "matthewbaker-1", there's a
   collision. Options: (a) prefix free agents (e.g., "free/matthewbaker-1"),
   (b) daemon checks gastown registry, (c) accept the risk (unlikely in
   practice).

3. **Subagent identity**: When Claude spawns subagents via the Task tool, should
   they share the parent's identity or get their own? Subagents inherit env, so
   they'd share BD_ACTOR. But for team agents, each should have a unique name.
   The Task tool already passes `name` parameter — could map to BD_ACTOR.

4. **Session key on Linux**: No `TERM_SESSION_ID` equivalent. `(PPID, TTY, PWD)`
   works but is less stable. Should we generate a random session key on first
   command and cache it in a temp file (`/tmp/bd-session-{PPID}`)? PPID-keyed
   temp files work if the parent shell is stable.

5. **Human-readability of auto-names**: `matthewbaker-1` is fine for 2-3
   sessions. For many agents, would a word-list approach be better?
   `matthewbaker-falcon`, `matthewbaker-cedar`? More memorable but harder to
   predict ordering.
