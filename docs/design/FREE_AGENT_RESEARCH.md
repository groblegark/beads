# Free-Agent Identity + Messaging Research Report

> **Epic**: bd-c9oxj | **Status**: Closed | **Date**: 2026-02-15

## Executive Summary

Research into how gastown manages agent identity, messaging, work dispatch, and how a "free agent" (bd CLI only, no gastown) could participate in beads workflows.

**Key finding**: Gastown's messaging is already beads-native (not NATS). Messages are stored as `type=message` issues. This means a free agent with just `bd` can already send/receive messages — the infrastructure is the beads database itself.

---

## 1. Gastown Mail System (bd-c9oxj.1)

### Architecture

Mail does NOT use NATS. All messages are beads issues with `type=message` stored in the town-level `.beads/` database.

### Message Schema

Messages are beads issues with:
- **Type**: `message`
- **Title**: Subject line
- **Description**: Message body
- **Assignee**: Recipient identity (for direct messages)
- **Priority**: 0=urgent, 1=high, 2=normal, 3=low
- **Status**: `open` (unread) → `closed` (read)
- **Labels**: `from:<sender>`, `thread:<id>`, `reply-to:<msg-id>`, `queue:<name>`, `claimed-by:<agent>`
- **Ephemeral flag**: For transient messages (not exported to JSONL)

### Delivery Patterns

1. **Direct**: `gt mail send gastown/Toast -s "Subject" -m "Body"` — single recipient
2. **Queue**: `gt mail send queue:work-queue -s "Task" -m "Details"` — one claimant
3. **Channel**: `gt mail send channel:alerts -s "Alert" -m "Details"` — broadcast with retention
4. **Group**: `gt mail send ops-team -s "Meeting"` — all group members

### Offline Handling

Messages persist in beads. Recipient pulls on demand via `gt mail inbox`. Best-effort tmux notification if session is active. No message loss.

### Key Insight for Free Agents

Since mail IS beads, a free agent can already:
- Send: `bd create --type=message --assignee=<recipient> --title="Subject" -d "Body"`
- Receive: `bd list --type=message --assignee=me --status=open`
- Read: `bd close <msg-id>` (marks as read)

**No gastown required for basic messaging.**

---

## 2. Agent Identity (bd-c9oxj.2)

### Identity Chain

```
GT_ROLE (env var)
  → BD_ACTOR (canonical address for beads attribution)
    → Agent Bead ID (persistent record with gt:agent label)
      → Assignee field (on work items and messages)
```

### Identity Formats by Role

| Role     | GT_ROLE                    | BD_ACTOR                   | BEADS_AGENT_NAME   |
|----------|---------------------------|----------------------------|--------------------|
| Mayor    | `mayor`                   | `mayor`                    | (not set)          |
| Deacon   | `deacon`                  | `deacon`                   | (not set)          |
| Witness  | `gastown/witness`         | `gastown/witness`          | (not set)          |
| Polecat  | `gastown/polecats/toast`  | `gastown/polecats/toast`   | `gastown/toast`    |
| Crew     | `gastown/crew/joe`        | `gastown/crew/joe`         | `gastown/joe`      |

### Actor Resolution in bd CLI

bd resolves actor via: `--actor` flag > `BD_ACTOR` env > `BEADS_ACTOR` env > `GT_ROLE` env > git user.name > `$USER`

### Agent Beads

Each agent has a persistent bead with:
- ID: `gt-<rig>-<role>[-<name>]` (e.g., `gt-gastown-polecat-toast`)
- Labels: `gt:agent,rig:gastown,role:polecat,agent:toast`
- Fields: `role_type`, `rig`, `agent_state`, `hook_bead`, `cleanup_status`, `active_mr`
- Status: `pinned` (permanent record)

### Key Insight for Free Agents

A free agent needs only:
1. `BD_ACTOR` env var (or `--actor` flag) — self-chosen name
2. No agent bead required for basic participation
3. No gastown infrastructure required

The bd CLI already supports this — `BD_ACTOR=alice bd create --title="Fix bug"` works today.

---

## 3. Beads-as-Inbox (bd-c9oxj.3)

### Message-as-Issue Pattern

Already works. `type=message` is a gastown custom type. The Issue struct has built-in fields:
- `Sender` (line 83 of types.go): "Who sent this"
- `Ephemeral` (line 84): "Not exported to JSONL"
- Thread support via `DepRepliesTo` dependency type with `ThreadID`

### Conversation Mechanisms

1. **Comments** (simple, flat): `bd comments add <id> "text"` — good for annotations
2. **Reply chains** (threaded): Create reply message linked via `DepRepliesTo` dependency — good for async conversations. `bd show --thread <id>` displays full thread.

### Inbox Queries (Already Work)

```bash
bd list --assignee=me --type=message --status=open          # Unread messages
bd list --assignee=me --type=message --sort=updated          # All messages
bd list --type=message --label=from:alice --status=open      # Messages from alice
bd list --type=message --created-after="2h ago"              # Recent messages
```

Performance: ~100-200ms per query (SQLite/Dolt).

### Recommendation

- **MVP**: `bd list --assignee=me --type=message` as inbox (works today)
- **UX**: Add `bd inbox` alias command
- **Extend bd news**: Add `--type=message` support for message display
- **Real-time**: Not needed initially; polling every 1-2 min is fine for <100 agents

---

## 4. Hooked Beads (bd-c9oxj.5)

### What is a Hooked Bead?

A work item with `status=hooked` assigned to an agent. Represents active work the agent must execute immediately (GUPP principle: "If it's on your hook, YOU RUN IT").

### Hook Flow

```
1. bd update <bead> --status=hooked --assignee=<agent>
2. bd slot set <agent-bead> hook <work-bead>
3. Agent receives startup nudge with "assigned" topic
4. gt prime outputs "AUTONOMOUS WORK MODE" section
5. Agent executes immediately
```

### Status Model

```
open → hooked → closed
         ↑
   (in_progress is optional, rarely used for hooked work)
```

### Stuck Detection

Deacon monitors agents via:
- Health check nudges every 30-60 seconds
- 3 consecutive failures → agent marked stuck
- Stale hooks (>1hr with dead session) → work unhooked back to `open`

### What Free Agents Can't Do

Hooked beads require gastown infrastructure:
1. Agent bead with `gt:agent` label (for hook_bead slot)
2. Tmux session (for health monitoring)
3. Town routing context (for mail delivery)
4. Deacon monitoring (for stuck/stale detection)

**However**, a free agent can still:
- Set `status=in_progress` and `assignee=self` (simpler than hooked)
- Close work when done
- The hooked pattern is just gastown's optimized version of claim → work → close

---

## 5. Implications for Free-Agent Design

### What Already Works (No Changes Needed)

1. **Self-naming**: `BD_ACTOR=alice` or `--actor=alice`
2. **Creating issues**: `bd create --title="..." --assignee=alice`
3. **Claiming work**: `bd update <id> --status=in_progress --assignee=alice`
4. **Closing work**: `bd close <id>`
5. **Sending messages**: `bd create --type=message --assignee=bob --title="Hi" -d "body"` (requires custom type config)
6. **Receiving messages**: `bd list --assignee=alice --type=message --status=open`
7. **bd news**: Already shows what others are working on

### What Needs Building

1. **`bd inbox` command**: Alias for message polling (small, easy)
2. **Message type registration**: Auto-configure `types.custom` to include `message` for free agents
3. **`bd send` command**: Convenience wrapper for creating message issues
4. **bd prime for free agents**: Recognize non-gastown context, suggest bd news + bd inbox workflow
5. **Notification hook**: Optional webhook/polling hook that checks for new messages on SessionStart

### What's Explicitly NOT Needed

1. No NATS — mail is already beads-native
2. No agent beads — free agents can participate without them
3. No tmux monitoring — free agents self-manage
4. No gastown routing — direct beads queries work
5. No special protocol — standard bd commands suffice

### Graceful Degradation Model

```
Full gastown:  NATS events + mail router + agent beads + Deacon monitoring + GUPP
Free agent:    bd CLI + polling + self-naming + bd news + bd inbox
```

The free agent path is a strict subset. Everything that works for free agents also works in gastown (but gastown adds real-time delivery, health monitoring, and automated work dispatch on top).

---

# Free-Agent Bootstrap Design (bd-c9oxj.4)

## Overview

A free agent is a Claude Code session (or similar) with beads access but no gastown infrastructure. This design covers how such an agent bootstraps itself into a functional participant in beads workflows.

**Design principle**: Zero-config by default, progressive enhancement with config.

---

## 1. Self-Naming

### How It Works Today

bd resolves the actor identity via this chain (first match wins):
```
--actor flag > BD_ACTOR env > BEADS_ACTOR env > GT_ROLE env > git user.name > $USER
```

For most free agents, `git user.name` or `$USER` provides a reasonable default without any configuration.

### Proposed Enhancement

Add a persistent actor config to beads:

```bash
bd config set actor "alice"     # Persistent, stored in .beads/config.yaml
bd whoami                        # Show current resolved identity
```

**Resolution chain (updated)**:
```
--actor flag > BD_ACTOR env > BEADS_ACTOR env > GT_ROLE env > bd config actor > git user.name > $USER
```

### `bd whoami` Command

New command to show resolved identity:
```
$ bd whoami
Actor: alice
Source: bd config (actor)
Daemon: connected (gastown-uat:9867)
Agent bead: none (free agent)
```

**Implementation**: ~30 lines in `cmd/bd/whoami.go`. Read from same resolution chain as `getActor()`, display source.

---

## 2. Registration

### With Daemon (Remote or Local)

When a daemon is running, the free agent is already "registered" — every bd command goes through the daemon, which tracks the actor via the activity log (`daemon/activity.json`).

No additional registration step needed. The daemon already knows about agents through their commands.

### Standalone (No Daemon)

When running standalone (local `.beads/` directory only), the agent operates on the local database. No registration required — the agent IS the only user.

### Optional: Agent Bead Creation

For richer participation (discoverable by others, can receive messages):

```bash
bd register                      # Create an agent bead for yourself
# Equivalent to:
# bd create --type=agent --id="agent-$(bd whoami)" --title="Free Agent: alice" \
#   --labels="gt:agent,role:free" --pinned
```

This is **optional** — a free agent can work without it. But having an agent bead enables:
- Discovery by other agents (`bd list --type=agent --label=gt:agent`)
- Message routing (assignee matching)
- Status tracking (agent_state field)

**Implementation**: ~50 lines in `cmd/bd/register.go`. Check if agent bead exists, create if not. Requires `types.custom` to include `agent`.

---

## 3. Announcing Existence

### Passive Announcement

Every bd command a free agent runs creates attribution:
- `created_by` on issues they create
- `assignee` when they claim work
- Activity in `bd news` output

Other agents running `bd news` will see the free agent's activity automatically.

### Active Announcement (Optional)

For teams that want explicit awareness:

```bash
bd send @all "alice is online and available"    # Broadcast message
```

**For MVP**: Skip active announcement. Passive discovery via `bd news` is sufficient.

---

## 4. Discovering Available Work

### Already Works

```bash
bd ready                         # Issues with no blockers
bd list --assignee=alice         # Work assigned to me
bd list --status=in_progress     # My active work
bd news                          # What others are working on
```

### Enhanced: `bd mine`

Convenience command showing everything relevant to the current agent:

```bash
$ bd mine
In-progress (2):
  bd-c9oxj.4  Design: free-agent bootstrap    2m ago
  beads-q4m9  gt mayor K8s routing             1h ago

Assigned to you (3):
  beads-o5g9  Add created_by to list/ready     3h ago
  bd-abc123   Fix login flow                    1d ago
  bd-def456   Update docs                       2d ago

Messages (1 unread):
  msg-xyz  "Code review needed" from bob        5m ago
```

**For MVP**: Skip this. `bd list --assignee=me` + `bd list --type=message --assignee=me` covers it.

---

## 5. Receiving Messages

### `bd inbox` Command

Convenience wrapper:

```bash
$ bd inbox
2 unread messages:

  msg-a1b2  "Deploy approval needed"  from bob    5m ago
    Need your sign-off on the staging deploy...

  msg-c3d4  "Bug in auth module"      from carol  1h ago
    Found a regression in the login flow...

$ bd inbox read msg-a1b2        # Mark as read (bd close msg-a1b2)
$ bd inbox reply msg-a1b2 "Approved, go ahead"
```

**Implementation**: ~100 lines in `cmd/bd/inbox.go`.
- `bd inbox` — list unread messages
- `bd inbox read <id>` — mark read (close issue)
- `bd inbox reply <id> "text"` — create reply message with DepRepliesTo link
- `bd inbox count` — just the count (for prompts/hooks)

### Integration with bd prime

Add to bd prime output:
```
You have 2 unread messages. Run `bd inbox` to read them.
```

---

## 6. Sending Messages

### `bd send` Command

```bash
bd send bob "Subject line" "Message body"
bd send bob "Subject line" -f message.md       # Body from file
bd send bob "Subject line" --priority=high
bd send bob "Subject line" --ephemeral          # Won't persist in JSONL
```

**Implementation**: ~60 lines in `cmd/bd/send.go`.

### Compatibility

Works identically with gastown agents. Messages from free agents appear alongside gastown mail. Both query the same beads database.

---

## 7. New Commands Summary

| Command | Description | Lines | Priority |
|---------|-------------|-------|----------|
| `bd whoami` | Show resolved identity and source | ~30 | P1 |
| `bd inbox` | List/read/reply to messages | ~100 | P1 |
| `bd send` | Send a message to another agent | ~60 | P1 |
| `bd register` | Create agent bead (opt-in) | ~50 | P2 |
| `bd mine` | Show all work + messages for me | ~80 | P3 |

---

## 8. Implementation Plan

### Phase 1: Core Identity + Messaging (MVP)

1. **`bd whoami`** — Show resolved actor identity
2. **`bd inbox`** — List/read/reply to messages
3. **`bd send`** — Send messages
4. **Auto-register `message` type** — If `types.custom` doesn't include `message`, auto-add on first `bd send` or `bd inbox`
5. **bd prime integration** — Show unread message count

### Phase 2: Enhanced Discovery

6. **`bd register`** — Optional agent bead creation
7. **`bd mine`** — Combined view of work + messages
8. **`bd news --inbox`** — Show messages in news output

### Phase 3: Gastown Compatibility

9. **Mail router integration** — Ensure gastown `gt mail send` creates beads-compatible messages
10. **Cross-daemon messaging** — Message routing between separate daemon instances
11. **Notification hooks** — SessionStart hook checks for new messages

---

## 9. Migration Path

### From Gastown Agent to Free Agent

A gastown agent that loses its gastown connection degrades gracefully:
- `gt mail inbox` fails → fall back to `bd inbox`
- `gt hook` fails → fall back to `bd list --assignee=me --status=in_progress`
- `gt prime` fails → fall back to `bd prime`
- All beads data persists locally

### From Free Agent to Gastown Agent

A free agent that joins a gastown town gets enhanced:
- `bd inbox` still works, but `gt mail inbox` adds real-time notifications
- `bd register` agent bead gets picked up by gastown registry
- Messages sent via `bd send` appear in gastown mail system
- No data migration needed — same beads database
