# Jacks: Infrastructure Modification Primitives

**Issue**: beads-it75
**Status**: Draft (Review Round 1 Complete — 6 dimensions analyzed)
**Author**: Matthew Baker + Claude
**Date**: 2026-02-23

## Executive Summary

Jacks are a new bead type representing in-flight, temporary infrastructure
modifications. Like the automotive jack — you put a car "on the jacks" to do
maintenance, then take it "off the jacks" when done — a jack records the intent,
execution, and cleanup of infrastructure changes that exist outside the normal
CI/CD pipeline.

CLI: `bd jack on` / `bd jack off`

## Motivation

### The Problem

Agents currently have no sanctioned way to modify infrastructure. The system is
entirely pod-creation-centric:

| Scenario | Current Reality |
|----------|----------------|
| Add debug logging to a running pod | Must rebuild image through full CI |
| Test a quick fix before CI | No mechanism; agents wait or hack files in-place |
| Infrastructure blocking your work | Create a bead, wait for a human |
| Production outage response | No agent-driven emergency tooling |

When agents crash (29 crashed agents in a recent roster), any ad-hoc
infrastructure changes vanish. There is no record of what was modified, no way to
roll it back, and no way for the next agent to know what state infrastructure is
in.

### Why Beads, Not a Separate System

1. **Beads is already the control plane.** The gastown controller watches bead
   events and translates them into K8s operations. A separate infrastructure
   modification system would fragment the control plane.

2. **The type system was built for this.** Custom types via `types.custom`,
   type schemas with required fields, and type-specific CLI commands (gates have
   `bd gate resolve`, decisions have `bd decision respond`) are proven patterns.

3. **Dependencies and blocking already work.** A jack can block the bead that
   needs the infrastructure change. Closing the jack unblocks downstream work.

4. **The audit trail comes free.** Every bead has `created_at`, `created_by`,
   `updated_at`, `closed_at`, `close_reason`, comments, and metadata.

## Design Principles

1. **Temporary by design** — Jacks encode the expectation of removal. A car on
   jacks is not drivable. The system should nag about active jacks.
2. **Record before modify** — Create the jack before making changes, not after.
   The jack is the safety check.
3. **Revert plan required** — Every jack must document how to undo its changes.
4. **Audit everything** — The jack is the permanent record that a modification
   happened, what was found, and that it was cleaned up.
5. **TTL enforcement** — Jacks have a time-to-live. Expired jacks trigger alerts
   via `bd jack check`, not silent failures.
6. **Extend, don't reinvent** — Build on the existing bead lifecycle, dependency
   system, and gate patterns.

---

## Part 1: Data Model

### Approach: New Bead Type with Metadata Fields

A jack is a regular bead (`IssueType("jack")`) with domain-specific metadata
fields stored in the issue's `Metadata` JSON blob. This follows the pattern
established by gates (`AwaitType`, `AwaitID`, `Timeout`, `Waiters`) and
decisions (`DecisionPoint` table).

### Jack-Specific Fields (in Metadata)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `jack_target` | string | yes | What is being modified: `pod/<name>`, `deployment/<name>`, `configmap/<name>`, `namespace/<name>`, etc. |
| `jack_ttl` | duration | yes | How long the jack should live before auto-alerting (e.g., `30m`, `2h`, `24h`) |
| `jack_expires_at` | timestamp | computed | `created_at + jack_ttl`, used by `bd jack check` |
| `jack_changes` | []JackChange | no (populated during) | Structured record of what was actually modified |
| `jack_revert_plan` | string | yes | How to undo the changes (populated at creation or before `jack off`) |
| `jack_reverted` | bool | no | Whether revert was executed (set by `bd jack off`) |
| `jack_verify_cmd` | string | no | Command to verify infrastructure is clean after revert |

### JackChange Structure

Each modification made while a jack is active should be recorded:

```json
{
  "timestamp": "2026-02-23T04:30:00Z",
  "action": "edit",
  "target": "pod/bd-daemon-abc:/app/config.yaml",
  "before": "log_level: info",
  "after": "log_level: debug",
  "agent": "vast-cat"
}
```

Changes are appended to `jack_changes` via `bd jack log` or automatically by
agent tooling.

### Type Schema

```bash
bd type define jack \
  --required-field description \
  --required-label "jack:*" \
  --description "Infrastructure modification jack — requires description and jack: label"
```

Required labels enable categorization:
- `jack:debug` — debug logging, tracing
- `jack:hotfix` — testing a fix in-place
- `jack:failover` — infrastructure emergency response
- `jack:config` — configuration change (env vars, configmaps)
- `jack:experiment` — trying something to see if it works

### Status Mapping

Jacks use the existing bead status system:

| Phase | Status | Meaning |
|-------|--------|---------|
| Intent declared | `open` | Jack created, no changes made yet |
| Active | `in_progress` | Infrastructure is modified (car is "on the jacks") |
| Completed | `closed` | Changes reverted, jack removed (car is "off the jacks") |
| Orphaned/expired | `in_progress` past TTL | Needs attention — `bd jack check` flags these |

---

## Part 2: CLI Design

### The `on`/`off` Metaphor

The automotive metaphor is load-bearing:

- **Intentionality**: "Jacking up" a car is deliberate. You don't casually
  create a jack — you are about to go under the vehicle.
- **Temporariness**: Nobody leaves a car on jacks permanently. The metaphor
  encodes the expectation of removal.
- **Safety protocol**: You check stability before going under. `bd jack on` is
  the safety check — record what you are doing before you do it.
- **Urgency of removal**: A car on jacks is not drivable. The system should nag.

Compare with gate (`bd gate resolve`) and decision (`bd decision respond`). Each
type-specific verb captures domain semantics.

### Command: `bd jack on`

Raise the jack — declare intent and begin infrastructure modification.

```
Usage:
  bd jack on [flags]

Flags:
  -t, --target string      Target resource (e.g., pod/name, deployment/name) [required]
  -r, --reason string      Why this modification is needed [required]
      --ttl duration        Time-to-live before expiry alert (default 1h)
      --revert-plan string  How to undo the changes
      --blocks string       Bead ID this jack blocks (creates dependency)
      --labels strings      Additional labels (jack:* label auto-added)
  -p, --priority int        Priority 0-4 (default 2)
      --json                Output JSON
```

**Behavior:**
1. Creates a new bead with `type=jack`, `status=in_progress`
2. Sets `jack_target`, `jack_ttl`, `jack_expires_at` in metadata
3. If `--blocks` specified, creates a `blocks` dependency
4. Prints the jack ID (for use in subsequent commands)
5. Runs `hooks.EventCreate` hook

**Examples:**
```bash
# Debug logging
bd jack on --target="pod/bd-daemon-abc" \
           --reason="Adding NATS debug logging for connection timeout" \
           --ttl=1h

# Testing a fix, blocking current work
bd jack on --target="deployment/bd-daemon" \
           --reason="Testing fix for NATS reconnection (bd-abc123)" \
           --blocks=bd-abc123 \
           --ttl=30m

# Emergency failover
bd jack on --target="statefulset/dolt" \
           --reason="Primary lost, manual failover" \
           --priority=0 \
           --ttl=1h
```

### Command: `bd jack off`

Lower the jack — revert changes and close.

```
Usage:
  bd jack off <jack-id> [flags]

Flags:
  -r, --reason string    What was found / why it is being removed [required]
      --verify           Run verification command after revert
      --force            Close even if revert plan not executed
      --json             Output JSON
```

**Behavior:**
1. Validates the bead is type `jack` and status `in_progress`
2. Sets `jack_reverted=true` in metadata
3. If `--verify` and `jack_verify_cmd` is set, runs verification
4. Sets `status=closed`, `closed_at=now`, `close_reason=<reason>`
5. Unblocks any beads that were waiting on this jack
6. Prints summary of jack lifetime (duration, changes recorded)

**Examples:**
```bash
# Clean removal
bd jack off bd-jack-xyz --reason="Debug logs captured the issue, DNS resolution delay"

# With verification
bd jack off bd-jack-xyz --verify --reason="Fix confirmed working, reverting"

# Force close (pod was recycled, changes already gone)
bd jack off bd-jack-xyz --force --reason="Pod recycled, modifications lost"
```

### Command: `bd jack list`

Show active jacks.

```
Usage:
  bd jack list [flags]

Flags:
  -a, --all              Include closed jacks
      --expired          Show only expired (past TTL) jacks
      --target string    Filter by target resource
      --json             Output JSON
```

**Default output** (active jacks only):
```
ACTIVE JACKS (2):

  bd-jack-abc  [1h23m active, TTL 2h]  target=pod/bd-daemon-xyz
    Reason: Adding NATS debug logging
    Agent: vast-cat

  bd-jack-def  [4h12m active, TTL 1h]  target=statefulset/dolt  ⚠ EXPIRED
    Reason: Primary failover
    Agent: keen-newt
    Changes: 3 recorded
```

### Command: `bd jack show`

Show detailed jack information.

```
Usage:
  bd jack show <jack-id>
```

**Output:**
```
◐ bd-jack-abc [JACK] · Adding NATS debug logging   [● P2 · IN_PROGRESS]
Agent: vast-cat · Target: pod/bd-daemon-xyz
TTL: 2h · Active: 1h23m · Expires: 2026-02-23T06:30:00Z

REASON
  Adding NATS debug logging for connection timeout investigation.
  Bead bd-abc123 is blocked until root cause identified.

REVERT PLAN
  1. kubectl exec bd-daemon-xyz -- sed -i 's/log_level: debug/log_level: info/' /app/config.yaml
  2. kubectl exec bd-daemon-xyz -- kill -HUP 1

CHANGES (2):
  [04:32] edit pod/bd-daemon-xyz:/app/config.yaml  log_level: info → debug
  [04:33] exec pod/bd-daemon-xyz  kill -HUP 1 (reload config)

BLOCKS
  → ◐ bd-abc123: Investigate NATS connection timeouts
```

### Command: `bd jack check`

Find expired jacks. Designed for periodic sweeps (like `bd gate check`).

```
Usage:
  bd jack check [flags]

Flags:
      --auto-escalate    Create P0 alert beads for expired jacks
      --json             Output JSON
```

**Behavior:**
1. Finds all `type=jack`, `status=in_progress` beads
2. Checks `jack_expires_at` against current time
3. Reports expired jacks with duration past TTL
4. If `--auto-escalate`, creates P0 alert beads for each expired jack

### Command: `bd jack extend`

Request more time.

```
Usage:
  bd jack extend <jack-id> --ttl <duration> [flags]

Flags:
      --ttl duration     New TTL from now (required)
  -r, --reason string    Why more time is needed
```

**Behavior:**
1. Updates `jack_expires_at` to `now + ttl`
2. Records extension in comments (with reason)
3. Useful when investigation takes longer than expected

### Command: `bd jack log`

Record a change made while the jack is active.

```
Usage:
  bd jack log <jack-id> [flags]

Flags:
      --action string    What was done: edit, exec, patch, delete, create
      --target string    Specific resource affected (defaults to jack target)
      --before string    State before change
      --after string     State after change
      --cmd string       Command that was executed
```

**Behavior:**
1. Appends a `JackChange` entry to `jack_changes` metadata
2. Records timestamp and current agent automatically

---

## Part 3: RPC Protocol

### New Operations

| Operation | Args | Response | Description |
|-----------|------|----------|-------------|
| `OpJackOn` | `JackOnArgs` | `Issue` | Create and activate a jack |
| `OpJackOff` | `JackOffArgs` | `Issue` | Revert and close a jack |
| `OpJackCheck` | `JackCheckArgs` | `[]Issue` | Find expired jacks |
| `OpJackExtend` | `JackExtendArgs` | `Issue` | Extend jack TTL |
| `OpJackLog` | `JackLogArgs` | `Issue` | Record a change |

### Args Structs

```go
type JackOnArgs struct {
    Target     string   `json:"target"`       // Required: resource identifier
    Reason     string   `json:"reason"`       // Required: why
    TTL        string   `json:"ttl"`          // Duration string, default "1h"
    RevertPlan string   `json:"revert_plan"`  // How to undo
    Blocks     string   `json:"blocks"`       // Bead ID to block
    Labels     []string `json:"labels"`       // Additional labels
    Priority   int      `json:"priority"`     // 0-4, default 2
}

type JackOffArgs struct {
    ID     string `json:"id"`      // Jack bead ID
    Reason string `json:"reason"`  // What was found
    Verify bool   `json:"verify"`  // Run verify command
    Force  bool   `json:"force"`   // Close without revert confirmation
}

type JackCheckArgs struct {
    AutoEscalate bool `json:"auto_escalate"`
}

type JackExtendArgs struct {
    ID     string `json:"id"`
    TTL    string `json:"ttl"`     // New TTL from now
    Reason string `json:"reason"`
}

type JackLogArgs struct {
    ID     string `json:"id"`
    Action string `json:"action"`
    Target string `json:"target"`
    Before string `json:"before"`
    After  string `json:"after"`
    Cmd    string `json:"cmd"`
}
```

---

## Part 4: Daemon Implementation

### Server Handler: `server_jack.go`

The daemon handles jack operations in a new server file, following the pattern of
`server_decision_test.go` and gate handlers in `server_mol.go`.

**`handleJackOn`:**
1. Validate required fields (target, reason)
2. Parse TTL, compute `expires_at`
3. Create issue with `type=jack`, `status=in_progress`
4. Set metadata: `jack_target`, `jack_ttl`, `jack_expires_at`, `jack_revert_plan`
5. Add `jack:<category>` label from first label or default `jack:general`
6. If `blocks` specified, create `blocks` dependency
7. Emit `bus.EventJackOn` for real-time notifications

**`handleJackOff`:**
1. Fetch issue, validate type=jack, status=in_progress
2. If not `--force`, check `jack_revert_plan` exists and warn if not executed
3. Set `jack_reverted=true` in metadata
4. Close issue with reason
5. Emit `bus.EventJackOff`

**`handleJackCheck`:**
1. Query all `type=jack`, `status=in_progress` issues
2. Filter by `jack_expires_at < now`
3. If `auto_escalate`, create P0 issues for each expired jack
4. Return expired jacks list

### Event Bus Integration

New event types for real-time monitoring:

```go
const (
    EventJackOn    EventType = "jack.on"
    EventJackOff   EventType = "jack.off"
    EventJackExpired EventType = "jack.expired"
)
```

These events flow through the existing NATS → Slack notification pipeline,
so humans get Slack alerts when jacks are raised, lowered, or expire.

---

## Part 5: Integration Points

### beads3d Visualization

Jacks would render as distinctive nodes in the 3D graph — perhaps as glowing
orange warning markers that pulse more urgently as they approach TTL expiry. The
existing SSE mutation feed would carry jack events to the frontend.

### Agent Advice

An advice bead could be created to remind agents to check for active jacks
before starting infrastructure-related work:

```bash
bd advice add --trigger=session-start \
  --command='bd jack list --expired 2>/dev/null | grep -q EXPIRED && echo "⚠ Expired jacks found! Run bd jack list --expired"' \
  --labels=global
```

### Periodic Sweep

The daemon's background task runner (which already handles `bd gate check` and
`bd decision check`) would add `bd jack check --auto-escalate` on a configurable
interval (default: every 5 minutes).

### Controller Integration (Future)

The gastown controller could eventually watch for `jack.on` events and
pre-authorize certain modification patterns (e.g., allow an agent to `kubectl
exec` into its own pod for debug logging). This is a future extension, not part
of the initial implementation.

---

## Part 6: Scenarios

### Scenario 1: Debug Logging Without CI

```bash
# Agent investigating connection timeouts
bd jack on --target="pod/bd-daemon-abc" \
           --reason="Adding NATS debug logging for timeout investigation" \
           --revert-plan="Restore config.yaml log_level to info, HUP pid 1" \
           --ttl=1h \
           --labels=jack:debug

# Agent makes the change and records it
bd jack log bd-jack-xyz --action=edit \
           --target="pod/bd-daemon-abc:/app/config.yaml" \
           --before="log_level: info" --after="log_level: debug"

# ... captures logs, finds root cause ...

# Agent reverts and closes
bd jack off bd-jack-xyz --reason="Root cause: DNS resolution delay in CoreDNS ndots:5"
```

### Scenario 2: Testing a Fix In-Place

```bash
bd jack on --target="deployment/bd-daemon" \
           --reason="Testing NATS reconnect fix for bd-abc123" \
           --blocks=bd-abc123 \
           --ttl=30m \
           --labels=jack:hotfix

# Agent patches binary, verifies fix works...

bd jack off bd-jack-xyz --reason="Fix verified, proceeding to CI build"
# bd-abc123 is now unblocked
```

### Scenario 3: Emergency Failover

```bash
bd jack on --target="statefulset/dolt" \
           --reason="Dolt primary lost, manual failover" \
           --priority=0 \
           --ttl=1h \
           --labels=jack:failover \
           --revert-plan="Remove manual primary label, let cluster auto-elect"

bd jack log bd-jack-xyz --action=exec \
           --cmd="kubectl label pod dolt-1 dolthub.com/cluster_role=primary"

# System stabilizes...

bd jack off bd-jack-xyz --reason="Failover complete, new primary: dolt-1"
```

### Scenario 4: Orphaned Jack Recovery

```bash
# Agent crashes mid-jack
bd jack check
# ⚠ bd-jack-abc EXPIRED (2h past TTL) target=pod/bd-daemon-xyz
#   Agent: zesty-ape (CRASHED)
#   Reason: Adding debug logging
#   Changes: 1 recorded
#   Revert plan: Restore config.yaml log_level to info

# Human or another agent picks it up
bd jack show bd-jack-abc   # Review what was changed
bd jack off bd-jack-abc --reason="Agent crashed, reverted manually" --force
```

---

## Part 7: Implementation Plan

### Phase 1: Type and CLI (estimated ~600 lines)

1. Register `jack` as custom type in `types.custom` config
2. Create `cmd/bd/jack.go` (parent command)
3. Create `cmd/bd/jack_on.go` (`bd jack on`)
4. Create `cmd/bd/jack_off.go` (`bd jack off`)
5. Create `cmd/bd/jack_list.go` (`bd jack list`)
6. Create `cmd/bd/jack_show.go` (`bd jack show`)
7. Create `cmd/bd/jack_check.go` (`bd jack check`)
8. Create `cmd/bd/jack_extend.go` (`bd jack extend`)
9. Create `cmd/bd/jack_log.go` (`bd jack log`)

### Phase 2: RPC and Daemon (~300 lines)

1. Add RPC operation constants to `protocol.go`
2. Add args/result structs to `protocol.go`
3. Implement `handleJackOn` in `server_jack.go`
4. Implement `handleJackOff` in `server_jack.go`
5. Implement `handleJackCheck` in `server_jack.go`
6. Implement `handleJackExtend` and `handleJackLog`
7. Register operations in server dispatch

### Phase 3: Integration (~200 lines)

1. Add event bus types for jack events
2. Add Slack notification for jack events
3. Add periodic `jack check` to daemon background tasks
4. Add jack nodes to beads3d visualization (SSE events)

### Phase 4: Testing

1. Unit tests for jack metadata validation
2. RPC integration tests for jack lifecycle
3. CLI script tests in `cmd/bd/testdata/`

---

---

## Part 8: Design Review Findings (Round 1)

Six parallel review agents analyzed this design across API, Data Model, UX,
Scalability, Security, and Integration dimensions. This section captures the
critical findings and required changes before implementation.

### 8.1 Authorization Model (Critical)

**Finding**: No authorization check on `jack_target`. Any agent can target any
K8s resource, including production namespaces, databases, and secrets.

**Resolution**: Add tiered authorization:
- **Auto-approved**: Non-production namespaces, debug pods, `jack:debug` labels
- **Requires decision gate**: Production namespaces, StatefulSets, Secrets,
  ConfigMaps containing credentials
- **Implementation**: Add `TargetRig` to `JackOnArgs`, validate against agent's
  rig scope via existing `auth.go` infrastructure
- **Target format**: Define grammar:
  `<resource-type>/<namespace>/<name>` for K8s,
  `external://<system>/<type>/<id>` for non-K8s

### 8.2 Revert Plan Semantics (Critical)

**Finding**: Design is ambiguous on whether `--revert-plan` is required at
creation or can be deferred. Also unclear whether daemon executes the plan or
it's documentation-only.

**Resolution**:
- **Revert plan is documentation, not executable by daemon.** Agent is
  responsible for executing steps manually, then closing the jack.
- **Required at creation** for non-emergency jacks (reject if missing).
- **Optional at creation for P0 jacks** (`--priority=0`), but required before
  closing without `--force`.
- **`--force` renamed to `--skip-revert-check`** for clarity. Requires `--reason`
  explaining why revert was skipped (e.g., "pod recycled").

### 8.3 Error Handling (Critical)

**Finding**: No error catalog. Failure cases for verification, concurrent jacks,
TTL expiry during close, and invalid IDs are all unspecified.

**Resolution**: Add explicit error handling:

| Scenario | Behavior |
|----------|----------|
| `bd jack off <invalid-id>` | Error: "jack not found" with suggestion to run `bd jack list` |
| `bd jack off` on closed jack | Error: "jack already closed by {agent} at {time}" |
| `bd jack off --verify` fails | Error: "verification failed; fix infrastructure or use --skip-revert-check" |
| `bd jack on --target=<already-jacked>` | Warning: "existing jack {id} on target; use --allow-concurrent to override" |
| `bd jack on` without `--reason` | Error: "--reason is required" |
| `bd jack on` without `--revert-plan` (non-P0) | Error: "--revert-plan is required (use --priority=0 to defer)" |
| `bd jack extend` on expired jack | Allowed (extends from now); comment logged |
| `bd jack log` on closed jack | Error: "jack is closed; cannot log changes" |

### 8.4 Emergency Ergonomics (Critical)

**Finding**: Two required flags (`--target`, `--reason`) plus recommended
`--revert-plan` is too heavy for P0 outages.

**Resolution**: Define emergency mode:
```bash
# Minimum viable emergency jack (P0 defers revert plan):
bd jack on --target=pod/foo --reason="P0: outage" --priority=0

# Defaults applied:
#   --ttl=1h
#   --revert-plan="" (deferred, required before close)
#   --labels=jack:general (auto-added)
```

Also support positional target for speed:
```bash
bd jack on pod/foo --reason="P0: outage" -p0
```

### 8.5 Storage Strategy (Major)

**Finding**: Storing `jack_expires_at` in metadata JSON prevents indexing.
`bd jack check` requires full table scan + JSON parsing. At 27K+ issues, this
is too slow for a 5-minute sweep.

**Resolution**: Hybrid approach:
- **Promote `jack_expires_at`** to a denormalized column on the issues table
  (nullable, indexed). Updated by `handleJackOn` and `handleJackExtend`.
- **Keep `jack_changes`, `jack_revert_plan`, `jack_verify_cmd`** in metadata
  JSON (rarely queried, variable size).
- **Add limits**: Max 500 `jack_changes` entries per jack. Truncate
  `before`/`after` to 5KB each.

### 8.6 Sensitive Data Protection (Major)

**Finding**: `jack_changes` before/after values could contain secrets
(passwords, API keys, tokens). Stored in plaintext in JSONL/Dolt.

**Resolution**:
- Scan `before`/`after` for known secret patterns at `bd jack log` time
- If detected, require `--sensitive` flag acknowledgment
- Redact patterns in stored values (e.g., `DB_PASSWORD=[REDACTED]`)
- Display `[SENSITIVE - run bd jack show <id> --reveal]` in list/show output
- Full values available only via `--reveal` flag (logged as audit event)

### 8.7 TTL Enforcement (Major)

**Finding**: No maximum TTL. Agents can extend indefinitely, defeating the
"temporary by design" principle. Auto-escalation is optional.

**Resolution**:
- **Maximum cumulative TTL**: 7 days per jack
- **Maximum extensions**: 5 per jack
- **Maximum single extension**: 24 hours
- **Auto-escalation is default behavior** (not optional flag)
- **Sweep interval**: 5 minutes (configurable via `BEADS_JACK_SWEEP_INTERVAL`)
- **Escalation creates a decision point** (not just a P0 bead) requiring human
  response

### 8.8 Concurrent Jack Detection (Major)

**Finding**: Two agents can open jacks on the same target with conflicting
revert plans.

**Resolution**:
- `handleJackOn` checks for existing active jacks on same target
- Default: reject with error showing existing jack ID and agent
- Override: `--allow-concurrent` flag (with warning logged)

### 8.9 Notification Throttling (Major)

**Finding**: If agents log changes frequently, `jack.log` events could flood
Slack (500-1000 events/minute at scale).

**Resolution**:
- Suppress Slack notifications for `jack.log` events entirely
- Batch `jack.on` events if >10 in 1 minute (send summary)
- Keep `jack.off` and `jack.expired` as real-time alerts
- Deduplicate `jack.expired` alerts (one per jack per 6 hours)

### 8.10 Multi-Rig Visibility (Minor)

**Finding**: Cross-rig jack visibility and blocking semantics unspecified.

**Resolution**: Global jacks with rig field (Path A):
- Jacks are stored globally in Dolt (shared across rigs)
- `bd jack on` auto-sets `jack_rig` to current rig
- `bd jack list` shows all jacks by default; `--rig=<name>` to filter
- Cross-rig blocking works via standard dependency system

### 8.11 `--force` Flag Rename (Minor)

**Finding**: `--force` is ambiguous — could mean "bypass safety" vs. "revert was
manual." Name is dangerous.

**Resolution**: Rename to `--skip-revert-check`. Clarify in help:
```
--skip-revert-check   Close jack without verifying revert was executed.
                      Use when: pod was recycled, infrastructure was rebuilt,
                      or revert was performed manually out-of-band.
                      Requires --reason explaining why revert check was skipped.
```

### 8.12 Ambient Jack Awareness (Minor)

**Finding**: Agents modifying resources with active jacks get no warning.

**Resolution**:
- Add advice bead for session-start: check for active/expired jacks
- `bd show` on issues blocked by jacks should display jack info
- Future: `bd jack list --target=<resource>` before modifying resources

---

## Appendix A: Comparison with Existing Specialized Types

| Feature | Gate | Decision | Jack |
|---------|------|----------|------|
| Metaphor | Barrier/checkpoint | Question/choice | Automotive lift |
| Primary verb | `resolve` | `respond` | `on` / `off` |
| Blocks work? | Yes | Yes | Yes |
| Human required? | Sometimes | Always | Not necessarily |
| Auto-check? | `bd gate check` | `bd decision check` | `bd jack check` |
| TTL/timeout? | Yes | Yes | Yes (critical) |
| Created by | Formula/workflow | Agent | Agent |
| Purpose | Wait for condition | Get human input | Modify infrastructure |
| Change tracking | No | No | Yes (jack_changes) |
| Revert plan | N/A | N/A | Required |

## Appendix B: Why `on`/`off`, Not `create`/`close`

The standard bead verbs (`create`, `claim`, `close`) are intentionally NOT used
for jacks. The `on`/`off` verbs serve three purposes:

1. **Domain clarity**: "Jack on" immediately communicates "infrastructure is being
   modified." "Create" does not carry this weight.

2. **Combined operation**: `bd jack on` is equivalent to `create + claim` in one
   step. You never create a jack without activating it (you don't put a jack
   under a car and then walk away).

3. **Cleanup expectation**: `bd jack off` is equivalent to `revert + close`.
   The `off` verb encodes the expectation that reverting happened, not just that
   the bead was closed.

The standard verbs still work (`bd show`, `bd list --type=jack`, `bd update`)
for inspection and metadata updates. Only the lifecycle transitions get
domain-specific verbs.
