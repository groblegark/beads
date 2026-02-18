---
name: decision
description: >
  Toggle decision checkpoint enforcement on or off. Use '/decision enable' to
  enforce checkpoints (for autonomous agents) or '/decision disable' to suppress
  them (when interacting directly). Supports per-agent overrides.
allowed-tools: "Bash(bd:*)"
version: "1.0.0"
author: "Steve Yegge <https://github.com/steveyegge>"
license: "MIT"
---

# Decision Checkpoint Toggle

Toggle whether the stop hook enforces decision checkpoints.

## Usage

Run the appropriate `bd decision mode` command based on the user's request:

- `/decision enable` → `bd decision mode enable` (global)
- `/decision disable` → `bd decision mode disable` (global)
- `/decision` (no args) → `bd decision mode` (show current state)
- `/decision disable --agent=X` → `bd decision mode disable --agent=X` (per-agent)
- `/decision enable --agent=X` → `bd decision mode enable --agent=X` (per-agent)
- `/decision clear --agent=X` → `bd decision mode clear --agent=X` (remove override)

Per-agent overrides take precedence over the global setting.

After running the command, report the result to the user.
