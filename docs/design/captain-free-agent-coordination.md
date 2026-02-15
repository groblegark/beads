# Captain + Free-Agent Coordination: Complete Design

## 1. Executive Summary

This document describes a **lightweight multi-agent coordination pattern** using three already-built primitives: decisions, inbox, and beads. It works at two scales:

1. **Free-agent mode** — N Claude Code sessions on a laptop, coordinated by a human or AI captain via `bd decision captain auto`. No infrastructure beyond the `bd` CLI.

2. **K8s gastown mode** — The same pattern running inside Kubernetes, where the captain maps to Mayor+Deacon roles and agents run as pods managed by the agent-controller.

The key insight: **these are the same system at different scales**. Free-agent mode is a strict subset of gastown. Everything that works locally also works in K8s.

---

## 2. The Three Primitives

### 2.1 Decisions (Agent -> Captain)

Agents generate **decision points** when they need human/captain input. The stop hook automatically creates these when an agent session ends.

```
Agent (stop hook) -> bd decision create -> decision_points table
                                              |
Captain (polling) -> bd decision captain auto -> reads pending decisions
                                              |
Captain responds -> bd decision captain respond -> resolves decision
                                              |
Response pushed -> inbox table -> agent drains on next hook
```

**Decision structure**: prompt, options (JSON array with id/label), context, urgency, requested_by (agent name).

**Captain auto rules**:
- `--default-action continue` — select first non-stop option (keep agents working)
- `--default-action stop` — select "stop" option (shut agents down)
- `--urgent-action ask` — escalate high-urgency to human (print to stdout)
- `--sweep-age 30m` — auto-resolve stale decisions from dead sessions

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
       (human terminal or bd decision captain auto)
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
3. Human runs captain: `bd decision captain auto --default-action continue`
4. Agents run `bd ready`, claim work, do it, close it
5. Stop hooks fire decisions, captain responds, agents continue

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

The captain sweep mechanism (`--sweep-age 30m`) handles dead agents by auto-resolving their stale decisions.

**Gap**: No `bd news --agents` command for a quick agent activity summary. This is the biggest observability gap.

### 3.6 Lifecycle

- **Start**: Human opens terminals. (Future: `bd captain spawn <n>` script)
- **Continue/Stop**: Captain responds to decisions with continue or stop
- **Crash recovery**: Agent restarts, `bd prime` shows in-progress work, resumes
- **Shutdown**: Captain sends "stop" to all decisions, or `bd decision captain sweep --age 0`

### 3.7 Escalation

```
Normal decision -> captain auto-responds (continue/stop per rules)
Urgent decision -> captain prints to stdout -> human responds manually
Stale decision  -> captain sweeps -> agent stops
```

Config: `--default-action continue --urgent-action ask --sweep-age 30m`

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
|  | Captain (bd decision captain   ||                              |
|  |   auto, runs as agent or       ||                              |
|  |   standalone deployment)       ||                              |
|  +--------------------------------+|                              |
+-----------------------------------------------------------------+
```

### 4.2 How It Maps

| Free-Agent Concept | K8s Gastown Equivalent |
|---|---|
| `BD_ACTOR=alice` | `GT_ROLE=gastown/polecats/toast`, injected by pod manager |
| `bd ready` (pull work) | Hooked beads (GUPP): issue marked `status=hooked`, assigned to agent |
| `bd decision create` (stop hook) | Same — agents create decisions identically |
| `bd decision captain auto` | Mayor + Deacon: Mayor handles escalations, Deacon monitors health |
| `bd inbox push` | Same — inbox works identically via daemon RPC |
| Daemon session names (in-memory) | Agent beads with `gt:agent` label (persistent, controller-managed) |
| `bd news` (activity discovery) | Agent beads with `agent_state` field (spawning/working/stuck/done) |
| Captain sweep (stale decisions) | Deacon stuck detection (health checks, consecutive failure threshold) |
| Human starts terminals | Controller creates pods from agent beads |
| `BD_ACTOR` resolution chain | Pod env injection: GT_ROLE, BD_ACTOR, GT_RIG, GT_AGENT, BEADS_AGENT_NAME |

### 4.3 Captain Deployment in K8s

Three options, in order of preference:

**Option A: Run captain as an agent pod (recommended for gastown)**

The captain is just another agent. It runs `bd decision captain auto` as its main loop. The controller creates its pod like any other agent. This is the simplest option and requires no new infrastructure.

```yaml
# Agent bead for captain
type: agent
role: captain
agent_state: working
labels: [gt:agent, role:captain]

# Pod gets:
GT_ROLE=captain
BD_ACTOR=captain
# Runs: bd decision captain auto --default-action continue
```

Advantages: No new Helm templates, reuses existing agent pod infrastructure, gets coop/credentials automatically, can be managed like any other agent.

**Option B: Dedicated deployment (recommended for free-agent-first setups)**

A standalone Deployment in the gastown namespace, similar to coop-broker:

```yaml
captain:
  enabled: false
  image:
    repository: ghcr.io/groblegark/beads
    tag: "latest"
  resources:
    requests: { cpu: 100m, memory: 256Mi }
    limits: { cpu: 500m, memory: 512Mi }
  config:
    defaultAction: continue
    urgentAction: ask
    sweepAge: 30m
    pollInterval: 5s
```

Pod runs: `bd decision captain auto` with config from ConfigMap. Gets daemon token, NATS URL, coop broker URL via env vars.

Advantages: Independent lifecycle, clear RBAC, can be enabled/disabled per namespace.

**Option C: Integrated into existing Mayor/Deacon**

Captain logic could be merged into the Mayor's workflow. Mayor already handles escalations; captain auto-respond is a subset of that.

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
| Mayor | Human captain + `bd decision captain auto` | Strategic decisions, escalation handling |
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
  + Captain auto-respond + Activity inference + Pull-based work

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

**B. Captain config defaults**
- Store captain preferences in `bd config`: `captain.default-action`, `captain.urgent-action`, `captain.sweep-age`, `captain.poll-interval`
- `bd decision captain auto` reads from config, CLI flags override
- Effort: Small (read config in captain auto, add config keys)

**C. Agent name persistence**
- Daemon session registry is in-memory only, lost on restart
- Persist `session_registry` to Dolt (session_key -> assigned_name)
- Ensures agent names survive daemon restarts
- Effort: Medium (new table, migration, read-on-startup)

### 6.2 Nice-to-Have

**D. `bd captain start <n>` — Launch script**
- Opens N terminal sessions with BD_ACTOR set
- Creates initial work assignments from `bd ready`
- Starts captain auto in a dedicated terminal
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

# Terminal 2: Captain (human watches stdout)
bd decision captain auto --default-action continue --urgent-action ask

# Terminals 3-5: Agents
BD_ACTOR=alice claude   # Agent picks from bd ready, works, closes
BD_ACTOR=bob claude     # Same
BD_ACTOR=carol claude   # Same

# Captain sees decisions as they arrive:
# captain auto: hq-abc123 [continue] -> "Alice completed task, what next?" (auto-continue)
# captain auto: hq-def456 [URGENT] -> prints to stdout, human types response
```

### 7.2 AI Captain, N Free Agents (Laptop)

```bash
# Captain runs unattended
bd decision captain auto \
  --default-action continue \
  --sweep-age 30m \
  --notify

# Agents work autonomously
# Captain auto-continues normal decisions
# Captain sweeps stale decisions from dead agents
# Captain sends inbox notifications on each response
```

### 7.3 K8s Gastown Deployment

```yaml
# Helm values
captain:
  enabled: true
  config:
    defaultAction: continue
    urgentAction: ask
    sweepAge: 30m

# Controller spawns agents as pods
# Captain watches decisions from all agent pods
# Inbox delivery via JetStream (real-time)
# Captain logs to stdout (collected by log aggregator)
```

---

## 8. Security Considerations

- **Single captain per daemon**: Multiple captain auto instances would race on decisions. Enforce via lock file or singleton check.
- **Captain identity**: Captain responds as `responded_by: "captain-auto"` (or custom `--by` flag). Audit trail shows who responded.
- **Inbox trust**: Agents should verify inbox messages come from expected sources. Currently no auth on inbox items — trust is based on daemon access.
- **Decision spoofing**: Anyone with daemon access can create/respond to decisions. In K8s, daemon token is per-namespace secret. In free-agent mode, daemon is localhost-only.

---

## 9. Future Directions

1. **Captain-as-agent**: Captain itself runs as a Claude Code session, can analyze decisions with LLM reasoning before responding (beyond simple rules)
2. **Multi-project captains**: Watch decisions across multiple beads databases / daemon instances
3. **Slack escalation**: Captain sends Slack notifications for urgent decisions (reuse existing slackbot infra)
4. **Captain handoff**: Smooth transition from free-agent captain to gastown Mayor/Deacon as project scales
5. **Federation**: Multiple captains across organizations, coordinating via beads sync
