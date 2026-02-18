# Plan: Robust Agent Roster & Bead-Assignment Enforcement

**Author:** quick-wren
**Date:** 2026-02-18
**Status:** Draft — awaiting approval
**Scope:** `internal/eventbus/`, `cmd/bd/prime.go`, `cmd/bd/agent.go`

---

## Problem Statement

Agents can work indefinitely without claiming or creating an in_progress bead.
The current nudge system is advisory-only (warnings every 3 minutes that agents
routinely ignore). The roster display doesn't surface how long an agent has been
working without a task ("freelancing"), making it invisible to captains and humans.
There are also correctness bugs in the subprocess fallback that produce false
negatives in multi-agent environments.

## Goals

1. Make it progressively harder for agents to ignore bead-assignment requirements
2. Surface "time without task" in roster display so captains can intervene
3. Fix correctness bugs in actor-task matching
4. Keep the system opt-in for hard blocking (backward-compatible)

---

## Phase 1: Fix Correctness Bugs (no behavior change)

### 1A. Fix `checkSubprocess` actor filtering

**File:** `internal/eventbus/handlers_nudge.go:213-221`

**Bug:** The subprocess fallback runs `bd list --status=in_progress --json` and
returns true if *any* in_progress beads exist — regardless of who owns them.
In multi-agent setups, agent B avoids nudges because agent A has in_progress work.

**Fix:** Filter the JSON result by actor name. Parse the output and check whether
any returned issue has `assignee == actor` or `created_by == actor`.

```go
func (h *BeadNudgeHandler) checkSubprocess(ctx context.Context, event *Event) (bool, error) {
    stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event),
        "list", "--status=in_progress", "--json")
    if err != nil {
        return false, err
    }
    if stdout == "" || stdout == "[]" || stdout == "null" {
        return false, nil
    }
    // Parse and filter by actor
    var issues []struct {
        Assignee  string `json:"assignee"`
        CreatedBy string `json:"created_by"`
    }
    if err := json.Unmarshal([]byte(stdout), &issues); err != nil {
        return false, nil
    }
    for _, issue := range issues {
        if issue.Assignee == event.Actor || issue.CreatedBy == event.Actor {
            return true, nil
        }
    }
    return false, nil
}
```

**Tests:** Add unit test with multi-agent scenario — two actors, one with a bead,
one without. Verify the without-bead actor still gets nudged.

### 1B. Fix mutation event actor attribution

**File:** `internal/eventbus/presence.go:365-402`

**Bug:** `handleMutationEvent` resolves actor as `payload.Actor` then falls back to
`payload.Assignee`. When a captain assigns work to an agent (captain is the actor,
agent is the assignee), the task gets tracked under the captain, not the agent.

**Fix:** When `payload.NewStatus == "in_progress"`, also track under `payload.Assignee`
if it differs from the resolved actor. Both the person who made the change AND the
assignee should have the task in their `taskIDs`.

```go
if payload.NewStatus == "in_progress" {
    state.taskIDs[payload.IssueID] = true
    // Also track for assignee if different from actor
    if payload.Assignee != "" && payload.Assignee != actor {
        assigneeState, ok := pt.actors[payload.Assignee]
        if !ok {
            assigneeState = &actorState{taskIDs: make(map[string]bool)}
            pt.actors[payload.Assignee] = assigneeState
        }
        if assigneeState.taskIDs == nil {
            assigneeState.taskIDs = make(map[string]bool)
        }
        assigneeState.taskIDs[payload.IssueID] = true
    }
}
```

**Tests:** Test that `bd update <id> --status=in_progress --assignee=agent-x`
run by captain results in `HasTask("agent-x") == true`.

### 1C. Ensure `enrichRosterWithTasks` uses stable sort order

**File:** `internal/rpc/server_agent_pod.go:375-380`

**Bug:** The code assumes issues come sorted by `updated_at desc` but doesn't
enforce it. If the storage backend returns unsorted results, the "most recent"
heuristic breaks silently.

**Fix:** After fetching in_progress issues, sort by `UpdatedAt` descending before
building the `actorTask` map. Or change the logic to always prefer the most recently
updated issue regardless of iteration order.

---

## Phase 2: Track `freelancing_since` in PresenceTracker

### 2A. Add `freelancingSince` field to `actorState`

**File:** `internal/eventbus/presence.go`

Add a new field to `actorState`:

```go
type actorState struct {
    // ... existing fields ...
    freelancingSince time.Time // when this actor last had zero taskIDs
}
```

**Behavior:**
- Set to `firstSeen` when actor first appears (they start with no task)
- Set to `time.Now()` when `len(taskIDs)` transitions from >0 to 0
- Reset to zero value when `len(taskIDs)` transitions from 0 to >0
- Preserved across resurrections (don't reset on un-reap)

### 2B. Surface in `PresenceEntry` and roster API

Add to `PresenceEntry`:

```go
FreelancingSecs float64 `json:"freelancing_secs,omitempty"`
```

Add to `AgentRosterEntry` in `internal/rpc/protocol.go`:

```go
FreelancingSecs float64 `json:"freelancing_secs,omitempty"`
```

Compute in `Roster()` method from `freelancingSince`.

### 2C. Surface in `bd agent roster` display

**File:** `cmd/bd/agent.go:873-896`

When an actor has no task and `FreelancingSecs > 0`, show:

```
  bright-hog  idle=3s  dur=12m5s  rate=4.2/m  events=51  last=PostToolUse  tool=Bash
                NO TASK (freelancing 8m12s)
```

### 2D. Surface in `outputRosterSection` (prime output)

**File:** `cmd/bd/prime.go:710-728`

Change the "no claimed task" line to include freelancing duration:

```
- **bright-hog** — active, freelancing, no claimed task (idle 3s, freelancing 8m12s)
```

This gives the agent itself (and captains reading the roster) visibility into
how long the situation has persisted.

---

## Phase 3: Escalating Nudge with Optional Blocking

### 3A. Escalation tiers in `BeadNudgeHandler`

**File:** `internal/eventbus/handlers_nudge.go`

Replace the single-tier nudge with a three-tier escalation based on
`freelancingSince` from the PresenceTracker:

| Tier | Threshold | Action |
|------|-----------|--------|
| 1 (Remind) | 0-5 min | Warning with ready task suggestions (current behavior) |
| 2 (Urge) | 5-10 min | Stronger warning: "You MUST claim a bead. Continued work without assignment wastes team visibility." |
| 3 (Block) | 10+ min | If `enforce-bead-assignment` config is true: set `result.Block = true` on PreToolUse. Otherwise tier-2 warning continues. |

**Implementation notes:**
- The handler already has access to `PresenceTracker` via `SetPresenceTracker`.
  Read `freelancingSince` from presence to determine tier.
- If presence tracker is unavailable (nil), fall back to current behavior
  (single-tier warning, no escalation).
- Cooldown adjusts per tier: tier 1 = 3min, tier 2 = 2min, tier 3 = every call
  (blocking should be immediate once threshold is hit).

### 3B. Add `enforce-bead-assignment` config option

**Config key:** `enforce-bead-assignment`
**Default:** `false`
**Set via:** `bd config set enforce-bead-assignment true`

When true, tier-3 escalation blocks the agent. When false (default), the agent
gets tier-2 warnings indefinitely — same as today but louder.

### 3C. Expand `BeadNudgeHandler` to handle `PreToolUse`

Currently the handler only fires on `PostToolUse`. To support blocking, it must
also handle `PreToolUse` (blocking on PostToolUse is too late — the tool already ran).

```go
func (h *BeadNudgeHandler) Handles() []EventType {
    return []EventType{EventPostToolUse, EventPreToolUse}
}
```

On `PreToolUse`: check tier. If tier 3 and enforcement enabled, block.
On `PostToolUse`: nudge/warn as today (tiers 1-2).

### 3D. Allow captain override via inbox

Add a mechanism for captains to send an `unblock` inbox message that temporarily
exempts an agent from tier-3 blocking for N minutes. This handles legitimate
cases where an agent is doing exploratory work that doesn't fit a bead.

**Implementation:** Check inbox for recent `type=unblock` items from captain
before applying tier-3 block. If found within the last N minutes (configurable,
default 30), skip the block.

---

## Phase 4: Roster Display Improvements

### 4A. Add "freelancing" count to roster summary

**File:** `cmd/bd/agent.go:849-854`

Add a "Freelancing" counter alongside Working/Idle/Dead:

```
Live Roster (8 active, 12 total tracked, uptime: 2h15m)
  Working: 5  Idle: 1  Freelancing: 2  Dead: 4
```

"Freelancing" = live actors with no task who have been active for >60 seconds
(to avoid counting agents that just started).

### 4B. Color/emoji indicators for freelancing duration

In `printRosterEntry`, add visual urgency based on freelancing duration:

- 0-5 min: no indicator (normal startup)
- 5-10 min: `[!]` prefix
- 10+ min: `[!!]` prefix (or `[BLOCKED]` if enforcement is active)

### 4C. Add `--freelancing` filter to `bd agent roster`

New flag: `bd agent roster --freelancing` — show only agents without tasks.
Useful for captain triage.

---

## Phase 5: Global Advice for Anti-Freelancing Prompting

### 5A. Create global advice bead for bead-assignment expectations

Agents receive advice via `bd prime` on SessionStart/PreCompact. This is the
right place to set behavioral expectations that persist across compaction.

Create a global advice bead that reinforces bead-assignment discipline:

```bash
bd create --title="Always claim or create a bead before starting work" \
  --type=advice --priority=2 \
  --description="$(cat <<'EOF'
## Bead Assignment Discipline

You MUST always have an in_progress bead assigned to you while working.
This is not optional — it is how the team tracks what you are doing.

**Before writing any code or making tool calls:**
1. Run `bd ready` to see available work
2. Claim an existing bead: `bd update <id> --status=in_progress`
3. Or create a new one: `bd create --title="..." --type=task --priority=2`
4. Then `bd update <id> --status=in_progress` to claim it

**Why this matters:**
- Captains and humans use the roster to understand team state
- Agents without beads are "freelancing" — their work is invisible
- Duplicate work happens when others can't see what you're doing
- The system will warn you, then escalate to blocking if you freelance too long

**When you finish a task:**
- `bd close <id>` to mark it complete
- Immediately claim or create your next bead — don't freelance between tasks
EOF
)"
bd label add <id> global
```

### 5B. Update existing nudge message to reference advice

The nudge message in `handlers_nudge.go` should reference the advice bead so
agents understand this is a team-wide policy, not a one-off suggestion:

```go
"You don't have a bead assigned (freelancing). " +
"Agents MUST always have an in_progress bead — see global advice. " +
"Run `bd ready` to find work, or `bd create --title=\"...\" --type=task` to track what you're doing."
```

### 5C. Add anti-freelancing note to prime output preamble

In `outputCLIContext` (`cmd/bd/prime.go`), add to the Core Rules section:

```
- **Anti-freelancing**: Always have an in_progress bead. Run `bd ready` or `bd create` before starting work.
```

This ensures even without the advice system, the prime output itself reminds
agents of the expectation.

---

## Rollout Strategy

| Phase | Risk | Can Ship Independently | Estimated Complexity |
|-------|------|----------------------|---------------------|
| 1 (Correctness) | Low — fixes bugs, no behavior change | Yes | Small (3 functions) |
| 2 (Tracking) | Low — adds data, no enforcement | Yes, after Phase 1 | Medium (presence + roster + display) |
| 3 (Escalation) | Medium — can block agents if misconfigured | Yes, after Phase 2. Default off. | Medium-Large (handler refactor + config) |
| 4 (Display) | Low — cosmetic improvements | Yes, after Phase 2 | Small |
| 5 (Advice) | Low — adds prompting, no code enforcement | Yes, ships independently | Small (1 advice bead + 2 string changes) |

Recommended order: **5 → 1 → 2 → 4 → 3** (ship advice prompting first since it's
zero-risk and immediate, then fix bugs, add tracking/display, then enforcement).

---

## Files Changed (Estimated)

| File | Phases | Changes |
|------|--------|---------|
| `internal/eventbus/handlers_nudge.go` | 1A, 3A, 3C | Fix subprocess, add escalation tiers, handle PreToolUse |
| `internal/eventbus/presence.go` | 1B, 2A, 2B | Fix mutation attribution, add freelancingSince tracking |
| `internal/rpc/server_agent_pod.go` | 1C, 2B | Fix sort order, surface freelancing_secs |
| `internal/rpc/protocol.go` | 2B | Add FreelancingSecs to AgentRosterEntry |
| `cmd/bd/prime.go` | 2D, 5C | Surface freelancing duration in roster section, anti-freelancing rule |
| `cmd/bd/agent.go` | 2C, 4A, 4B, 4C | Roster display improvements |
| `internal/eventbus/handlers_nudge_test.go` | 1A, 3A | Tests for subprocess fix & escalation |
| `internal/eventbus/presence_test.go` | 1B, 2A | Tests for mutation attribution & freelancing tracking |
| (advice bead via `bd create`) | 5A | Global advice bead for bead-assignment expectations |

---

## Open Questions

1. **Blocking granularity:** Should tier-3 blocking apply per-tool-call or should
   it allow a grace period after each block (e.g., block once, let 2 tool calls
   through, then block again)? Pure per-call blocking might prevent the agent from
   running `bd create` or `bd update` to fix the situation.

2. **Captain override UX:** Is inbox-based unblocking sufficient, or should there
   be a `bd agent exempt <actor> --duration=30m` command?

3. **Subagent handling:** Should subagents (e.g., `bright-hog/researcher`) be
   exempt from nudging? They're typically short-lived and don't independently
   own beads. Current behavior: they get nudged because they have their own
   BD_ACTOR identity.

4. **Multi-bead actors:** If an actor has 3 in_progress beads, should the roster
   show all of them or just the most recent? Currently shows one. Showing all
   might be useful for detecting agents that claim work but don't close it.
