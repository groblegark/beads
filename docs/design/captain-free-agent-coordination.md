# Captain + Free-Agent Coordination: Complete Design

## 1. Executive Summary

This document describes a **lightweight multi-agent coordination pattern** using three already-built primitives: decisions, inbox, and beads. It works at two scales:

1. **Free-agent mode** — N Claude Code sessions on a laptop, coordinated by a human or AI captain. The captain is itself a Claude Code session that reasons about decisions intelligently. No infrastructure beyond the `bd` CLI.

2. **K8s gastown mode** — The same pattern running inside Kubernetes, where the captain maps to Mayor+Deacon roles and agents run as pods managed by the agent-controller.

The key insight: **these are the same system at different scales**. Free-agent mode is a strict subset of gastown. Everything that works locally also works in K8s.

---

## 2. The Three Primitives

### 2.1 Decisions (Agent -> Captain)

Agents generate **decision points** when they need human/captain input. The stop hook automatically creates these when an agent session ends.

```
Agent (stop hook) -> bd decision create -> decision_points table
                                              |
Captain (watch)  -> bd decision captain watch -> receives new decision
                                              |
Captain reasons  -> reads context, analyzes options, decides intelligently
                                              |
Captain responds -> bd decision captain respond -> resolves decision
                                              |
Response pushed -> inbox table -> agent drains on next hook
```

**Decision structure**: prompt, options (JSON array with id/label), context, urgency, requested_by (agent name).

**The captain is an intelligent agent, not a rules engine.** It reads the decision context, reasons about what the agent should do, and responds with a thoughtful rationale. The captain uses `bd decision captain watch` to block until a decision arrives, then thinks and responds via `bd decision captain respond`.

Stale decisions from dead sessions can be cleaned up with `bd decision captain sweep`.

### 2.2 Inbox (Captain -> Agent)

The captain sends messages/instructions to agents via inbox push. Messages are delivered on the next SessionStart or PreCompact hook.

```
Captain -> bd decision captain send <agent> "message"
        -> bd inbox push --to=<agent> --type=agent "message"
                              |
                    inbox table (DB)
                              |
               JetStream publish (K8s only): inbox.agent.<name>
                              |
          InboxDrainHandler (priority 31) on next hook fire
                              |
                   Injected as system-reminder into Claude context
```

**Inbox item types**: decision, alert, agent, mail, gate, system, event.
**Priority levels**: 0=critical, 2=normal, 4=low.
**Dedup**: INSERT IGNORE on dedup_key prevents duplicates.
**TTL**: Optional expiration (e.g., 10m for ephemeral alerts).
**Drain cap**: 20 items per drain cycle, ordered by priority then created_at.

### 2.3 Beads (Shared Work Queue)

Both captain and agents read/write the same beads database. Work is tracked as issues with priority, status, assignee, and dependencies.

```
Captain creates work -> bd create --title="..." --assignee=<agent>
Agent discovers work -> bd ready (unblocked, open/in_progress)
Agent claims work    -> bd update <id> --status=in_progress --assignee=me
Agent finishes       -> bd close <id>
```

---

## 3. Free-Agent Mode (Laptop Scale)

### 3.1 Architecture

```
                         CAPTAIN
       (Claude Code session running as intelligent supervisor)
       bd decision captain watch -> reason -> respond -> loop
                    |              |
           watches decisions    sends inbox msgs
                    |              |
     +-----------+  |  +-----------+  +-----------+
     | Terminal 1 | |  | Terminal 2 |  | Terminal 3 |
     | BD_ACTOR=  | |  | BD_ACTOR=  |  | BD_ACTOR=  |
     | alice      | |  | bob        |  | carol      |
     |            | |  |            |  |            |
     | claude     | |  | claude     |  | claude     |
     +-----------+  |  +-----------+  +-----------+
           |        |        |              |
           +--------+--------+--------------+
                         |
                   BD DAEMON (single process)
                   Dolt DB (.beads/)
```

**Bootstrap**:
1. Human starts daemon: `bd daemon start` (or it auto-starts)
2. Human opens N terminals, each with `BD_ACTOR=<name> claude`
3. Human starts captain: another Claude Code session that watches decisions and reasons about them
4. Agents run `bd ready`, claim work, do it, close it
5. Stop hooks fire decisions, captain reads context and responds intelligently

**No infrastructure beyond bd CLI.** No NATS, no coop, no K8s, no gastown.

### 3.2 Agent Identity

Resolution chain (first match wins):
1. `--actor` flag
2. `BD_ACTOR` env var
3. `BEADS_ACTOR` env var
4. `GT_ROLE` env var (gastown only)
5. Daemon session registry (auto-assigned: `user`, `user-1`, `user-2`)
6. `git config user.name`
7. `$USER`

For free agents, set `BD_ACTOR` explicitly. The daemon also auto-assigns unique names for concurrent sessions (e.g., `matthewbaker-1`, `matthewbaker-2`).

**Gap**: Session names are in-memory only. Lost on daemon restart. Needs persistence (see implementation tasks).

### 3.3 Agent Discovery

Captain discovers agents **implicitly** through their activity:
- **Decisions**: `requested_by` field tells captain who is asking
- **bd news**: Shows who has in-progress work
- **bd list --status=in_progress**: Shows active assignees

No registry needed. Agents appear when they start working and generating decisions.

### 3.4 Work Dispatch

**Pull-first, push for steering.**

- **Default**: Agents run `bd ready` and pick highest-priority unblocked work
- **Directed**: Captain assigns via `bd update <id> --assignee=alice` or inbox message
- **Hybrid**: Captain creates prioritized issues, agents self-organize

Pull-first is correct because agents are autonomous and the captain may not always be online.

### 3.5 Health Monitoring

Without gastown Deacon, health is inferred from activity:

| Signal | Meaning |
|---|---|
| Recent decision (< 5m) | Active, healthy |
| In-progress work but no decision in 30m | Might be stuck |
| In-progress work but no decision in 2h | Probably dead |
| No in-progress work, no decisions | Idle or not started |

The captain can periodically run `bd decision captain sweep` to clean up stale decisions from dead agents.

**Gap**: No `bd news --agents` command for a quick agent activity summary. This is the biggest observability gap.

### 3.6 Lifecycle

- **Start**: Human opens terminals. (Future: `bd captain spawn <n>` script)
- **Continue/Stop**: Captain reasons about each decision and responds with appropriate action + rationale
- **Crash recovery**: Agent restarts, `bd prime` shows in-progress work, resumes. No coordination needed.
- **Shutdown**: Captain responds "stop" to decisions when it judges work is complete, or runs `bd decision captain sweep --age 0` to resolve all stale decisions

### 3.7 Escalation

```
Normal decision  -> captain reasons about it, responds with action + rationale
Complex decision -> captain sends inbox message asking human for guidance
Stale decision   -> captain sweeps (bd decision captain sweep)
```

The captain is an LLM — it can judge urgency, complexity, and risk. It escalates to the human when it's unsure, not based on rigid rules.

---

## 4. K8s Gastown Mode (Production Scale)

### 4.1 Architecture

In K8s, the same primitives operate but with richer infrastructure:

```
+-----------------------------------------------------------------+
|                    Gastown Namespace                              |
|                                                                   |
|  +------------------+     +-------------------+                  |
|  | BD Daemon Pod    |     | NATS StatefulSet  |                  |
|  | (RPC + HTTP)     |<--->| (JetStream)       |                  |
|  | Port 9876, 9080  |     | Port 4222         |                  |
|  +------------------+     +-------------------+                  |
|         |                          |                              |
|         |  +-------------------+   |                              |
|         +->| Agent Controller  |   |                              |
|            | (reconciler)      |   |                              |
|            | watches beads,    |   |                              |
|            | creates/deletes   |   |                              |
|            | agent pods        |   |                              |
|            +-------------------+   |                              |
|                   |                |                              |
|         +---------+---------+      |                              |
|         |         |         |      |                              |
|  +------v--+ +---v----+ +--v-----+|  +------------------+       |
|  | Agent   | | Agent  | | Agent  ||  | Coop Broker      |       |
|  | Pod 1   | | Pod 2  | | Pod 3  ||  | (OAuth + mux)    |       |
|  | polecat | | crew   | | crew   ||  | Port 8080, 9800  |       |
|  | Toast   | | Joe    | | Max    ||  +------------------+       |
|  +---------+ +--------+ +--------+|                              |
|                                    |                              |
|  +--------------------------------+|                              |
|  | Captain Pod                    ||                              |
|  | (Claude Code session,          ||                              |
|  |  watches + reasons + responds) ||                              |
|  +--------------------------------+|                              |
+-----------------------------------------------------------------+
```

### 4.2 How It Maps

| Free-Agent Concept | K8s Gastown Equivalent |
|---|---|
| `BD_ACTOR=alice` | `GT_ROLE=gastown/polecats/toast`, injected by pod manager |
| `bd ready` (pull work) | Hooked beads (GUPP): issue marked `status=hooked`, assigned to agent |
| `bd decision create` (stop hook) | Same — agents create decisions identically |
| Captain agent (watch + reason + respond) | Mayor + Deacon: Mayor handles escalations, Deacon monitors health |
| `bd inbox push` | Same — inbox works identically via daemon RPC |
| Daemon session names (in-memory) | Agent beads with `gt:agent` label (persistent, controller-managed) |
| `bd news` (activity discovery) | Agent beads with `agent_state` field (spawning/working/stuck/done) |
| Captain sweep (stale decisions) | Deacon stuck detection (health checks, consecutive failure threshold) |
| Human starts terminals | Controller creates pods from agent beads |
| `BD_ACTOR` resolution chain | Pod env injection: GT_ROLE, BD_ACTOR, GT_RIG, GT_AGENT, BEADS_AGENT_NAME |

### 4.3 Captain Deployment in K8s

Three options, in order of preference:

**Option A: Run captain as an agent pod (recommended for gastown)**

The captain is just another agent — a Claude Code session with a supervisory role. It uses `bd decision captain watch` to receive decisions, reasons about them, and responds via `bd decision captain respond`. The controller creates its pod like any other agent. This is the simplest option and requires no new infrastructure.

```yaml
# Agent bead for captain
type: agent
role: captain
agent_state: working
labels: [gt:agent, role:captain]

# Pod gets:
GT_ROLE=captain
BD_ACTOR=captain
# Runs: Claude Code session with captain prompt/workflow
```

Advantages: No new Helm templates, reuses existing agent pod infrastructure, gets coop/credentials automatically, can be managed like any other agent. The captain reasons intelligently about each decision rather than applying static rules.

**Option B: Integrated into existing Mayor/Deacon**

Captain responsibilities could be folded into the Mayor's workflow. Mayor already handles escalations and strategic decisions — the captain role is a natural subset.

This is the eventual convergence point but requires gastown code changes.

### 4.4 K8s-Specific Enhancements

Things the K8s deployment gets for free that free agents lack:

1. **Real-time inbox delivery** via JetStream: `inbox.agent.<name>` subject, coop writes to JSONL, nudges agent
2. **Agent bead registry**: Persistent agent records with `agent_state` tracking (spawning/working/stuck/done)
3. **Pod-level health monitoring**: Controller status reporter maps pod phases to beads agent_state
4. **Coop session management**: Terminal mux, session resume, credential distribution
5. **Automated lifecycle**: Controller creates/deletes pods based on agent beads
6. **NATS event bus**: Real-time event propagation (decision created, resolved, etc.)

### 4.5 Gastown Role Mapping

| Role | Free-Agent Equivalent | Notes |
|---|---|---|
| Mayor | Captain agent (intelligent supervisor) | Strategic decisions, escalation handling |
| Deacon | Captain sweep + activity inference | Health monitoring, stuck detection |
| Witness | N/A (single project scope) | Per-rig monitoring, not needed for single-project |
| Polecat | Free agent (ephemeral worker) | Short-lived, task-focused |
| Crew | Free agent (persistent worker) | Long-lived, domain-focused |
| Refinery | N/A | Data processing, not applicable |

---

## 5. Graceful Degradation Model

The system degrades gracefully as infrastructure is removed:

```
Full gastown K8s:
  NATS events + Coop terminal + Agent beads + Deacon health + Mayor coordination
  + Controller pod lifecycle + JetStream real-time inbox + Credential distribution

Captain + free agents (daemon only):
  Decisions (stop hook) + Inbox (DB + drain) + Beads (shared queue)
  + Captain agent (intelligent reasoning) + Activity inference + Pull-based work

Single agent (minimal):
  bd ready + bd close + stop hook (creates decision, times out, agent stops)
  No captain needed — human responds to decisions via CLI
```

Each layer adds capability without breaking the layer below. A free agent that works on a laptop will work identically in a K8s gastown namespace.

---

## 6. Implementation Gaps

### 6.1 Must-Have (MVP for free-agent captain pattern)

**A. `bd news --agents` — Agent activity summary**
- Shows: agent name, in-progress count, last decision time, inferred status
- Data: joins `issues` (assignee, status=in_progress) with `decision_points` (requested_by, created_at)
- Critical for captain observability — the biggest gap today
- Effort: Small (query + formatting in news.go)

**B. Captain workflow prompt/primer**
- Define a captain-specific `bd prime` output that teaches the captain agent its role
- Include: how to watch for decisions, how to reason about them, when to escalate
- Could be a `.beads/CAPTAIN.md` or a captain-specific prime mode
- Effort: Small (template in prime.go)

**C. Agent name persistence**
- Daemon session registry is in-memory only, lost on restart
- Persist `session_registry` to Dolt (session_key -> assigned_name)
- Ensures agent names survive daemon restarts
- Effort: Medium (new table, migration, read-on-startup)

### 6.2 Nice-to-Have

**D. `bd captain start <n>` — Launch script**
- Opens N terminal sessions with BD_ACTOR set
- Creates initial work assignments from `bd ready`
- Starts captain in a dedicated terminal
- Effort: Medium (shell script or Go command)

**E. `bd captain dashboard` — Live TUI**
- Auto-refreshing view of agent activity, decisions, inbox
- Decision queue depth, response rate, sweep count
- Effort: Large (TUI framework, real-time updates)

**F. Skill-based routing in daemon mode**
- `bd ready --with-skills` currently TODO in daemon RPC
- Enables heterogeneous agent pools (e.g., "go agent" vs "rust agent")
- Effort: Medium (RPC handler, skill dependency resolution)

**G. K8s captain Helm template**
- Optional Deployment for captain in gastown namespace
- ConfigMap for captain rules, Service for health endpoint
- Effort: Medium (follows existing coop-broker pattern)

---

## 7. Example Workflows

### 7.1 Human Captain, 3 Free Agents (Laptop)

```bash
# Terminal 1: Start daemon
bd daemon start

# Terminals 2-4: Worker agents
BD_ACTOR=alice claude   # Agent picks from bd ready, works, closes
BD_ACTOR=bob claude     # Same
BD_ACTOR=carol claude   # Same

# Terminal 5: Human responds to decisions as they arrive
bd decision captain list        # See what's pending
bd decision captain watch       # Block until next decision
bd decision captain respond hq-abc123 --select=continue --text="Good work, keep going"
```

### 7.2 AI Captain, N Free Agents (Laptop)

```bash
# Terminal 1: Captain (Claude Code session with captain role)
BD_ACTOR=captain claude
# Captain's workflow:
#   bd prime                            # Get context
#   bd decision captain watch           # Block until decision arrives
#   (reads decision context, reasons)   # Think about what agent should do
#   bd decision captain respond <id> --select=... --text="rationale"
#   bd decision captain send <agent> "follow-up instructions"
#   (loop)

# Terminals 2-N: Worker agents
BD_ACTOR=alice claude   # Works autonomously, generates decisions via stop hook
BD_ACTOR=bob claude     # Same
```

### 7.3 K8s Gastown Deployment

```yaml
# Captain runs as an agent pod with role=captain
# Controller spawns it like any other agent
# Captain uses same watch/reason/respond loop
# Inbox delivery via JetStream (real-time)
# Captain agent reasons about each decision with full LLM capability
```

---

## 8. Security Considerations

- **Single captain per daemon**: Multiple captain agents watching the same decisions would race. Coordinate via convention (one captain role per project).
- **Captain identity**: Captain responds as `responded_by: "captain"` (or custom `--by` flag). Audit trail shows who responded.
- **Inbox trust**: Agents should verify inbox messages come from expected sources. Currently no auth on inbox items — trust is based on daemon access.
- **Decision spoofing**: Anyone with daemon access can create/respond to decisions. In K8s, daemon token is per-namespace secret. In free-agent mode, daemon is localhost-only.

---

## 9. Future Directions

1. **Multi-project captains**: Watch decisions across multiple beads databases / daemon instances
3. **Slack escalation**: Captain sends Slack notifications for urgent decisions (reuse existing slackbot infra)
4. **Captain handoff**: Smooth transition from free-agent captain to gastown Mayor/Deacon as project scales
5. **Federation**: Multiple captains across organizations, coordinating via beads sync
