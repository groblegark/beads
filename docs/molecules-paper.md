# Gastown Molecules: Architecture, Pouring Mechanics, and beads3d Visualization

**Author:** fast-bull-1 (bd-j7kyt)
**Date:** 2026-02-21

---

## Abstract

Gastown's molecule system is a chemistry-inspired workflow templating engine built on top of beads. Molecules encode reusable multi-step workflows as **formulas** (recipes), instantiate them via **pouring** (persistent) or **wisping** (ephemeral), and orchestrate them through an agent-driven patrol loop. This paper documents the complete molecule architecture, explains the pouring pipeline in detail, catalogs the existing molecule inventory, and proposes integration strategies for rendering molecules in beads3d's Three.js-based 3D graph visualization.

---

## 1. The Chemistry Metaphor

The molecule system maps chemistry phase transitions onto workflow lifecycle states:

| Phase       | Chemistry | Beads Concept | Storage | Lifecycle |
|-------------|-----------|---------------|---------|-----------|
| **Solid**   | Crystal   | Formula/Proto | `.formula.toml` files or DB | Immutable template |
| **Liquid**  | Molten    | Mol (persistent) | `.beads/issues.jsonl` + git | Mutable, auditable, permanent |
| **Vapor**   | Gas       | Wisp (ephemeral) | DB only (`Ephemeral=true`) | Auto-cleaned, not exported to JSONL |
| **Digest**  | Residue   | Squashed summary | JSONL | Archived result of completed molecule |

The **phase transition verbs** are:
- **Pour** (solid → liquid): Creates persistent, auditable work from a template
- **Wisp** (solid → vapor): Creates ephemeral work that evaporates when done
- **Squash** (vapor → digest): Condenses a wisp into a permanent summary
- **Burn** (vapor → nothing): Destroys a wisp without any trace

This metaphor is not merely decorative — it encodes real operational semantics about data retention, git synchronization, and audit trail expectations.

---

## 2. Formulas: The Solid Phase

### 2.1 What Is a Formula?

A formula is a TOML or JSON template that declares a workflow as a directed acyclic graph (DAG) of steps. Each step becomes an issue (bead) when instantiated. Formulas live in:

1. **Project level:** `.beads/formulas/*.formula.toml` (highest priority)
2. **Town level:** `$GT_ROOT/.beads/formulas/` (gastown workspace)
3. **User level:** `~/.beads/formulas/` (fallback)

Earlier paths shadow later ones — a project formula with the same name as a town formula wins.

### 2.2 Formula Anatomy

The core schema (`internal/formula/types.go`):

```go
type Formula struct {
    Formula     string              // Unique identifier (e.g., "shiny")
    Description string
    Version     int                 // Schema version (currently 1)
    Type        FormulaType         // "workflow", "expansion", "aspect"
    Extends     []string            // Parent formulas (inheritance)
    Vars        map[string]*VarDef  // Variable definitions with defaults
    Steps       []*Step             // Main workflow steps (the DAG)
    Compose     *ComposeRules       // Bonding rules for composition
    Advice      []*AdviceRule       // Aspect-oriented before/after/around
    Phase       string              // "liquid" or "vapor" recommendation
    Runbooks    []string            // Referenced runbook beads
}
```

Each **Step** maps to a single bead:

```go
type Step struct {
    ID          string      // Unique within formula
    Title       string      // Supports {{var}} substitution
    Description string      // Rich markdown with embedded commands
    Type        string      // "task", "bug", "feature", "epic", "chore"
    DependsOn   []string    // Step IDs this blocks on
    Assignee    string      // Supports {{var}} substitution
    Gate        *Gate       // Async wait condition
    Loop        *LoopSpec   // Fixed-count, range, or conditional iteration
    OnComplete  *OnCompleteSpec // Runtime expansion on completion
    Children    []*Step     // Nested steps (epic hierarchy)
    Expand      string      // Reference expansion formula inline
}
```

### 2.3 Example: The Shiny Formula

The simplest canonical formula — "Engineer in a Box":

```toml
formula = "shiny"
type = "workflow"

[[steps]]
id = "design"
title = "Design {{feature}}"

[[steps]]
id = "implement"
needs = ["design"]
title = "Implement {{feature}}"

[[steps]]
id = "review"
needs = ["implement"]
title = "Review implementation"

[[steps]]
id = "test"
needs = ["review"]
title = "Test {{feature}}"

[[steps]]
id = "submit"
needs = ["test"]
title = "Submit for merge"

[vars.feature]
required = true
```

Five linear steps: design → implement → review → test → submit. One required variable (`feature`). Pouring this with `--var feature=auth` creates five beads with titles like "Design auth", "Implement auth", etc.

### 2.4 Advanced Formula Features

**Variables** (`VarDef`): Template parameters with types, defaults, and required flags. Substituted via `{{var}}` in titles, descriptions, assignees, and acceptance criteria.

**Composition** (`ComposeRules`): Define how molecules bond together:
- **Bond Points**: Named attachment sites (before/after/parallel to a step)
- **Hooks**: Automatic attachments triggered by conditions
- **Aspects**: Cross-cutting concerns applied via pointcut patterns (like AOP)

**Control Flow**:
- **Conditions**: Steps filtered at cook time based on variable truthiness
- **Loops**: Fixed count (`Count: 5`), range (`Range: "1..10"`), or conditional (`Until: "condition"`)
- **Gates**: Async wait conditions (GitHub run status, PR merge, timer, human decision, mail)
- **Decisions**: Human-in-the-loop selection points with options
- **OnComplete**: Runtime expansion — creates new molecules from step output

**Inheritance** (`Extends`): Formulas can extend parent formulas, inheriting and overriding steps. `shiny-enterprise` extends `shiny` with security audit steps.

---

## 3. The Pour Pipeline: Solid → Liquid

### 3.1 CLI Entry Point

```bash
bd mol pour mol-polecat-work --var issue=bd-abc123 --assignee swift-fox
```

The `pour` command (`cmd/bd/pour.go`) builds a `PourArgs` struct and sends it to the daemon via RPC:

```go
type PourArgs struct {
    ProtoID     string            // Formula name or proto bead ID
    Vars        map[string]string // Variable substitutions
    DryRun      bool              // Preview only
    Assignee    string            // Assign root issue
    Attachments []string          // Proto IDs to attach via bonding
    AttachType  string            // "sequential", "parallel", "conditional"
}
```

### 3.2 Server-Side Pour (handlePour)

The daemon's `handlePour()` in `server_write_ops.go` executes a multi-phase pipeline:

**Phase 1 — Resolution:**
1. Try to resolve `ProtoID` as an existing database proto (issue with `IsTemplate: true`)
2. If not found, try as a formula name via `parser.LoadByName()`
3. For formulas, run the full **cooking pipeline**

**Phase 2 — Cooking (cookFormulaFull):**
1. Load formula from filesystem (3-tier search)
2. Resolve inheritance chain (`Extends`)
3. Apply control flow transformations (branches, gates, loops)
4. Apply advice rules (before/after/around aspects)
5. Apply inline expansions (replace steps with expansion templates)
6. Return a `TemplateSubgraph` — the cooked, ready-to-instantiate DAG

**Phase 3 — Variable Validation:**
1. Apply defaults from `VarDef` entries
2. Extract required variables (those without defaults)
3. Reject pour if any required variables are missing

**Phase 4 — Instantiation:**
Call `formula.SpawnMolecule()` or `spawnSubgraph()`:

```go
// From instantiate.go — CloneSubgraph
func CloneSubgraph(ctx, store, subgraph, opts) (*InstantiateResult, error) {
    // Pass 1: Create issues with variable substitution
    for _, templateIssue := range subgraph.Issues {
        newIssue := clone(templateIssue)
        newIssue.Title = SubstituteVariables(newIssue.Title, opts.Vars)
        // ... substitute in Description, Design, Notes, Assignee
        store.CreateIssue(ctx, newIssue)
    }

    // Pass 2: Recreate dependency edges
    for _, dep := range subgraph.Dependencies {
        store.AddDependency(ctx, idMap[dep.IssueID], idMap[dep.DependsOnID], dep.Type)
    }

    // Pass 3: Add skill dependencies
    // Pass 4: Link runbooks (od-dv0.6)
}
```

**Phase 5 — Bonding (optional):**
If `Attachments` are specified, loop through each proto, load its subgraph, and bond it to the main molecule using the specified `AttachType` (sequential, parallel, conditional).

**Phase 6 — Post-Processing:**
1. Emit `MutationCreate` event on the event bus
2. Collect runbook references for client-side materialization
3. Return `PourResult`

### 3.3 PourResult

```go
type PourResult struct {
    RootID   string   // Root bead ID of the created molecule
    Created  int      // Number of beads spawned
    Attached int      // Number attached via bonding
    Phase    string   // "liquid" (persistent)
    Runbooks []string // Auto-materialized runbook references
}
```

After receiving the result, the CLI auto-materializes any referenced runbooks to `.oj/runbooks/`.

### 3.4 Wisp: The Vapor Path

The `wisp` command (`cmd/bd/wisp.go`) follows a nearly identical pipeline but with `Ephemeral: true`:

```bash
bd mol wisp mol-deacon-patrol
```

Key differences from pour:
- Issues are created with `Ephemeral: true`
- Issues are NOT exported to JSONL (not synced via git)
- Issues are stored in the main database only
- Wisps can be garbage-collected after 1 hour of inactivity (`bd mol wisp gc`)
- Wisps can be promoted to persistent via `squash` or destroyed via `burn`

The wisp lifecycle:
```
Create → Execute → Squash (condense to digest) or Burn (discard)
```

---

## 4. Molecule Inventory

The beads repo ships 46 formula files in `.beads/formulas/`. They fall into distinct categories:

### 4.1 Patrol Molecules (Vapor Phase)

These are the heartbeat of gastown — continuous operational loops that run as wisps:

| Formula | Steps | Agent | Purpose |
|---------|-------|-------|---------|
| `mol-deacon-patrol` (v10) | 22 | Mayor/Deacon | Master patrol: inbox, gates, convoys, health, zombies, orphans, cleanup |
| `mol-witness-patrol` (v2) | 12 | Witness | Per-rig worker monitor: survey polecats, detect orphans, check refinery |
| `mol-refinery-patrol` (v4) | 12 | Refinery | Merge queue processor: rebase, test, merge, push, notify |
| `mol-goblin-scout-patrol` | - | Goblin Scout | Infrastructure monitoring |

The Deacon patrol is the most complex formula in the system — 22 steps covering inbox handling, orphan process cleanup, pending spawn nudging, gate evaluation, convoy management, cross-rig dependency resolution, notifications, health scanning, zombie detection, polecat nudging, plugin execution, dog pool maintenance, orphan detection, stale hooks cleanup, session GC, cost/patrol digests, log rotation, and context-aware loop/exit.

### 4.2 Polecat Work Molecules (Liquid Phase)

Templates for worker agents (polecats) performing actual development:

| Formula | Steps | Purpose |
|---------|-------|---------|
| `mol-polecat-work` (v4) | 10 | Full work lifecycle: context → branch → preflight → implement → review → test → cleanup → submit → exit |
| `mol-polecat-code-review` | - | Code review workflow |
| `mol-polecat-review-pr` | - | PR review workflow |
| `mol-polecat-conflict-resolve` | - | Merge conflict resolution |
| `mol-polecat-lease` | - | Temporary polecat allocation |

### 4.3 Release & Deployment Molecules

| Formula | Purpose |
|---------|---------|
| `beads-release` | Full beads release workflow |
| `gastown-release` | Gastown deployment pipeline |
| `gastown-github-release` | GitHub release creation |
| `upgrade-beads` | beads binary upgrade |

### 4.4 Engineering Workflow Templates (Liquid Phase)

Reusable development patterns:

| Formula | Purpose |
|---------|---------|
| `shiny` | Canonical 5-step: design → implement → review → test → submit |
| `shiny-enterprise` | Shiny + security audit steps |
| `shiny-secure` | Shiny with security focus |
| `code-review` | Standalone code review |
| `design` | Design-phase workflow |
| `security-audit` | Security audit process |
| `rule-of-five` | Estimation/sizing workflow |

### 4.5 Infrastructure & Operations Molecules

| Formula | Purpose |
|---------|---------|
| `mol-orphan-scan` | Detect and recover orphaned work |
| `mol-session-gc` | Session garbage collection |
| `mol-convoy-feed` | Feed stranded convoys with workers |
| `mol-convoy-cleanup` | Clean up completed convoys |
| `mol-sync-workspace` | Workspace synchronization |
| `mol-patch-build-install` | Build and install patches |
| `mol-boot-triage` | Boot sequence triage |
| `mol-gastown-boot` | Full gastown boot sequence |
| `mol-shutdown-dance` | Graceful shutdown |
| `mol-town-shutdown` | Full town shutdown |
| `mol-dep-propagate` | Dependency propagation |
| `mol-upstream-pr-intake` | Upstream PR processing |

### 4.6 Testing & Quality Molecules

| Formula | Purpose |
|---------|---------|
| `e2e-fix` | E2E test fix workflow |
| `e2e-deflake` | E2E test deflaking |
| `e2e-test-fix` | E2E test repair |
| `mol-cicd-fix` | CI/CD pipeline fix |
| `mol-query-consistency-audit` | Database query audit |
| `mol-setup-validate` | Setup validation |
| `pihealth-commit-audit` | Commit health audit |

### 4.7 Meta Molecules

| Formula | Purpose |
|---------|---------|
| `mol-formula-iterate` | Iterating on formula design |
| `mol-digest-generate` | Generating digests |
| `setup-claude-flow` | Claude Code flow setup |
| `towers-of-hanoi-*` (4 variants) | Recursive workflow demos (7, 9, 10, default disks) |

---

## 5. Agent Dispatch: The Sling System

The `bd sling` command (`cmd/bd/sling.go`) is the dispatch mechanism that connects molecules to agents:

```bash
bd sling bd-abc bright-lark    # Explicit target
bd sling bd-abc                # Auto-select idle agent
bd sling bd-abc --spawn        # Spawn new Claude session
bd sling bd-abc --self         # Assign to yourself
```

### 5.1 Dispatch Pipeline

1. **Validate**: Check bead exists, not closed, not already assigned
2. **Resolve target**: Explicit name, `--self`, `--spawn` (generates adjective-noun name), or auto-select
3. **Auto-select algorithm**: Query roster → filter for non-reaped, non-self, no current task, idle >5s → pick longest-idle agent
4. **Assign**: Update bead status → `in_progress`, assignee → target agent
5. **Notify**: Push inbox message via daemon RPC (high priority)
6. **Spawn** (if `--spawn`): Launch `claude --dangerously-skip-permissions -p <prompt>` as background process with `BD_ACTOR`, `BEADS_AGENT_NAME`, `GIT_AUTHOR_NAME` env vars

### 5.2 The Sling-Pour Connection

In practice, molecules and sling work together:

```bash
# Mayor creates persistent work from a formula
bd mol pour shiny --var feature=auth --assignee swift-fox

# Or the Deacon dispatches a patrol wisp
bd mol wisp mol-witness-patrol
bd sling <wisp-root-id> --spawn
```

The polecat receives the work assignment via its inbox, reads the molecule steps with `bd ready`, works through them one by one via `bd close <step>`, and self-cleans via `gt done`.

---

## 6. The Graph Visualization System

### 6.1 bd graph: Terminal ASCII Rendering

The `bd graph` command (`cmd/bd/graph.go`) provides terminal-based molecule visualization:

- **Box mode** (default): ASCII boxes arranged by topological layer with dependency arrows
- **Compact mode**: Tree format with `├──`/`└──` connectors, one line per issue
- **All mode**: Shows all open issues grouped by connected component

The layout algorithm uses longest-path topological sort: Layer 0 nodes have no dependencies (can start immediately), higher layers depend on lower layers, nodes in the same layer can run in parallel.

Status icons convey state at a glance: `○` open, `◐` in_progress, `●` blocked, `✓` closed, `❄` deferred.

### 6.2 beads3d: Three.js 3D Force-Directed Graph

beads3d is a Three.js application built on `3d-force-graph` that renders the entire beads universe as an interactive 3D space:

**Visual Language:**
- **Regular beads**: Materia core spheres (FFVII-inspired) with subsurface scattering, breathing pulse for in-progress, halo sprites
- **Agents**: Retro lunar lander geometry (octahedron cabin, landing legs, descent stage, thruster, antenna) with rig-colored badges
- **Epics**: Purple wireframe icosahedron shells
- **Gates/Decisions**: Elongated diamonds with "?" markers
- **Blocked nodes**: Red wireframe spiky octahedron overlay
- **Links**: Colored lines with midpoint sprite icons, directional arrows, particle flow

**Node Sizing** encodes priority: P0 (16 units) → P4 (5 units), with 1.8x epic multiplier and 2.5x agent multiplier.

**Color encodes status**: green (open), amber/glowing (in_progress), red (blocked), burnt orange (hooked), muted blue (deferred), bright blue (review), dark gray (closed).

**Data pipeline**: Connect-RPC `Graph()` call returns nodes with issue metadata + edges with dependency types. SSE streams (`/events`, `/bus/events`) push live mutations for real-time updates.

---

## 7. Molecule Visualization in beads3d: Current State and Proposals

### 7.1 Current State

beads3d already has partial molecule support via `focusMolecule()`:

```
?molecule=<id>  →  BFS traversal  →  highlight subgraph  →  spread physics  →  zoom to fit
```

When triggered, it:
1. Finds the root node by ID
2. BFS-traverses all connected nodes (the molecule's step beads)
3. Stores them in a `focusedMoleculeNodes` set
4. Applies pairwise repulsion + radial expansion physics to spread the subgraph
5. Enables persistent labels on all molecule nodes
6. Zooms camera to fit the component

This works but treats molecules as an ad-hoc filter over the general graph. The visualization doesn't distinguish molecule structure from arbitrary bead connections.

### 7.2 Proposal: First-Class Molecule Visualization

#### 7.2.1 Molecule Node Type

Add a distinct visual treatment for molecule root nodes:

- **Geometry**: Replace the standard materia sphere with a **molecular model** — a central core (the root bead) with orbiting satellite nodes for each step, connected by bond-like edges
- **Container shell**: A translucent convex hull or bounding sphere that encompasses all step beads, colored by molecule phase (amber for liquid, light blue for vapor)
- **Phase indicator**: A subtle shader effect — liquid molecules shimmer/flow, vapor molecules have a wispy particle trail, digests are solid/crystalline

#### 7.2.2 Molecule DAG Layout

When a molecule is focused, switch from force-directed to **DAG layout** within the molecule:

```
Layer 0 (ready)     Layer 1          Layer 2          Layer 3
┌───────────┐       ┌──────────┐     ┌──────────┐     ┌──────────┐
│  design   │──────>│implement │────>│  review  │────>│   test   │
└───────────┘       └──────────┘     └──────────┘     └──────────┘
                                                            │
                                                            v
                                                      ┌──────────┐
                                                      │  submit  │
                                                      └──────────┘
```

This mirrors what `bd graph` shows in the terminal but rendered in 3D:
- X-axis: Topological layer (execution order)
- Y-axis: Parallelism within a layer
- Z-axis: Depth for nested children/sub-molecules

Transition between force-directed (global view) and DAG (molecule focus) should be animated — nodes smoothly reposition when entering/leaving focus mode.

#### 7.2.3 Step Progress Visualization

Encode molecule progress directly in the visualization:

- **Completed steps**: Shrink and dim (or collapse into the root node)
- **In-progress step**: Pulse with amber glow, enlarge slightly
- **Ready steps** (all deps met): Green glow ring
- **Blocked steps**: Red octahedron overlay (existing)
- **Gate steps**: Diamond geometry with gate-type icon (clock for timer, GitHub logo for gh:run, person for human)

A **progress bar** on the molecule container: a ring or arc around the bounding shell that fills as steps complete, giving instant visual feedback on molecule health.

#### 7.2.4 Bond Visualization

When molecules are bonded together (via `--attach`), visualize the bond:

- **Sequential bond**: Thick directed arrow between molecule containers
- **Parallel bond**: Side-by-side containers with a shared start edge
- **Conditional bond**: Dashed directed arrow with a "?" gate node at the junction

#### 7.2.5 Patrol Molecule Animation

Patrol molecules (deacon, witness, refinery) are special — they loop continuously. Visualize this as:

- **Cycle ring**: Steps arranged in a circle rather than a line, with the loop-or-exit step connecting back to inbox-check
- **Cycle counter**: Displayed as a badge (e.g., "Cycle 7") on the molecule container
- **Activity pulse**: The current step position moves around the ring like a radar sweep

#### 7.2.6 Formula Browser

Add a new panel or mode: **Formula Gallery**

- Display all available formulas as collapsed previews (mini DAG thumbnails)
- Click to expand into full DAG view with step descriptions
- "Pour" button to instantiate directly from the visualization
- Color-coded by category (patrol=blue, work=green, release=orange, infrastructure=gray)

#### 7.2.7 Molecule Timeline

Add a time dimension to molecule visualization:

- **Swim lane view**: Steps arranged horizontally by start time, vertically by parallelism
- **Gantt overlay**: Show actual duration vs. planned duration
- **History playback**: Scrub through molecule lifecycle from pour to completion

This could integrate with beads3d's existing timeline scrubber (logarithmic time-range filtering).

### 7.3 Data Requirements

The current Graph API provides `issue_type`, `status`, `priority`, and dependency edges — enough for basic molecule rendering. For the proposals above, the API would need:

| New Field | Source | Purpose |
|-----------|--------|---------|
| `molecule_id` | Issue metadata | Group steps by parent molecule |
| `molecule_phase` | "liquid" / "vapor" / "digest" | Phase-dependent visual treatment |
| `step_layer` | Computed from DAG | Topological layer for DAG layout |
| `formula_name` | Issue metadata | Link back to formula definition |
| `is_ephemeral` | `Ephemeral` field | Filter/dim vapor-phase molecules |
| `gate_type` | Gate metadata | Gate-specific geometry |
| `bond_type` | Dependency metadata | Bond-specific edge styling |
| `loop_iteration` | Step metadata | Cycle counter for patrol molecules |

Most of these can be derived server-side in `server_graph.go` and included in the graph response without schema changes.

### 7.4 Implementation Priority

Ranked by impact/effort ratio:

1. **Molecule container shell** (medium effort, high impact) — Immediately distinguishes molecules from loose beads in the global view
2. **DAG layout on focus** (medium effort, high impact) — Makes molecule structure readable instead of force-directed chaos
3. **Step progress visualization** (low effort, high impact) — Completed/in-progress/ready coloring already exists per-node; just need molecule-scoped progress bar
4. **Patrol cycle ring** (medium effort, medium impact) — Gives patrol molecules a distinctive visual identity
5. **Bond visualization** (low effort, medium impact) — Extends existing edge rendering with bond-type awareness
6. **Formula browser** (high effort, medium impact) — New UI panel, requires formula listing API
7. **Molecule timeline** (high effort, high impact) — Integrates with existing timeline scrubber but needs swim lane layout

---

## 8. The Polecat Self-Cleaning Model

The `mol-polecat-work` formula (v4) embodies the most important operational pattern in gastown. It's worth understanding in full because molecule visualization needs to render its lifecycle accurately.

### 8.1 The Contract

A polecat is a self-cleaning worker. It:
1. Receives work via its hook (pinned molecule + issue)
2. Works through molecule steps using `bd ready` / `bd close <step>`
3. Completes and self-cleans via `gt done` (submit + nuke itself)
4. Is **gone** — the Refinery merges from MQ independently

The polecat does NOT: push directly to main, close its own issue, wait for merge, or handle rebase conflicts.

### 8.2 The 10-Step Lifecycle

```
load-context → context-check → branch-setup → preflight-tests →
implement → self-review → run-tests → cleanup-workspace →
prepare-for-review → submit-and-exit
```

Each step has rich descriptions with embedded bash commands, forming a runbook that the polecat agent follows. The **preflight-tests** step is particularly notable — it implements "The Scotty Principle": don't walk past a broken warp core, but don't let someone else's mess consume your entire mission.

### 8.3 Visualization Implications

For beads3d, polecat work molecules should show:
- **Linear progression**: 10 steps in a clear left-to-right flow
- **Current position**: Which step the polecat is executing
- **Branch status**: Visual indicator of git branch health (clean/dirty)
- **Self-destruct countdown**: When the polecat hits `submit-and-exit`, the molecule should visually dissolve (the polecat and its worktree are destroyed)

---

## 9. Cross-Rig Coordination

### 9.1 Convoys

Convoys are coordination beads that track multiple issues across rigs. The Deacon's patrol checks convoy completion each cycle — when all tracked issues close, the convoy auto-closes.

Visually, a convoy should appear as a **container** encompassing beads from multiple rigs, with progress indicators per-rig.

### 9.2 The Sling-Dispatch Chain

```
Mayor/Deacon → bd sling <bead> <target>  → Inbox push  → Agent wakes
                                          → NATS notify → Coop nudge
```

This chain should be visualizable as an animated pulse traveling from the dispatcher to the target agent, similar to the existing link particle effects.

### 9.3 The Mail System

Agents communicate via mail (beads with `issue_type=message`). The Deacon processes:
- `WITNESS_PING` — second-order monitoring (who watches the watchers?)
- `POLECAT_DONE` — worker completion
- `HELP` — escalation from stuck workers
- `DOG_DONE` — infrastructure task completion
- `MERGED` — refinery reports successful merge

beads3d could show mail in transit as small animated particles traveling between agent nodes, with different colors per mail type.

---

## 10. Conclusion

Gastown's molecule system transforms the flat bead model into a structured, reusable workflow engine. The chemistry metaphor (solid/liquid/vapor) maps cleanly to real operational needs: formulas are immutable templates, mols are persistent auditable work, and wisps are ephemeral operational chores that don't pollute the git history.

The pour pipeline is the critical path — it resolves formulas, cooks them through inheritance and aspect application, validates variables, and atomically spawns multi-step bead DAGs. Combined with the sling dispatch system and the patrol molecule loop, it enables autonomous agent orchestration at scale.

beads3d already has the foundation for molecule visualization (focus mode, subgraph spreading, force-directed layout). The key gaps are: distinguishing molecules from loose beads visually (container shells), rendering internal structure clearly (DAG layout), and showing operational progress (step progress bars, patrol cycle rings). These enhancements would transform beads3d from a flat graph viewer into a molecule-aware workflow observatory.

---

## Appendix A: Key Source Files

| File | Purpose |
|------|---------|
| `internal/formula/types.go` | Complete formula schema (906 lines) |
| `internal/formula/instantiate.go` | CloneSubgraph / SpawnMolecule (167 lines) |
| `internal/rpc/server_write_ops.go:578` | handlePour RPC implementation |
| `cmd/bd/pour.go` | CLI pour command (151 lines) |
| `cmd/bd/wisp.go` | CLI wisp command (580 lines) |
| `cmd/bd/sling.go` | CLI sling/dispatch command (344 lines) |
| `cmd/bd/graph.go` | Terminal graph visualization (870 lines) |
| `internal/molecules/molecules.go` | Molecule catalog loader |
| `internal/rpc/server_formula.go` | Formula RPC operations |
| `internal/rpc/server_graph.go` | Graph API for beads3d |

## Appendix B: Formula File Inventory

46 formula files in `.beads/formulas/`:

**Patrol (4):** `mol-deacon-patrol`, `mol-witness-patrol`, `mol-refinery-patrol`, `mol-goblin-scout-patrol`

**Polecat Work (5):** `mol-polecat-work`, `mol-polecat-code-review`, `mol-polecat-conflict-resolve`, `mol-polecat-review-pr`, `mol-polecat-lease`

**Release (4):** `beads-release`, `gastown-release`, `gastown-github-release`, `upgrade-beads`

**Engineering (7):** `shiny`, `shiny-enterprise`, `shiny-secure`, `code-review`, `design`, `security-audit`, `rule-of-five`

**Infrastructure (12):** `mol-orphan-scan`, `mol-session-gc`, `mol-convoy-feed`, `mol-convoy-cleanup`, `mol-sync-workspace`, `mol-patch-build-install`, `mol-boot-triage`, `mol-gastown-boot`, `mol-shutdown-dance`, `mol-town-shutdown`, `mol-dep-propagate`, `mol-upstream-pr-intake`

**Testing (7):** `e2e-fix`, `e2e-deflake`, `e2e-test-fix`, `mol-cicd-fix`, `mol-query-consistency-audit`, `mol-setup-validate`, `pihealth-commit-audit`

**Meta (7):** `mol-formula-iterate`, `mol-digest-generate`, `setup-claude-flow`, `towers-of-hanoi` (x4)
