//go:build windows

package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"

	"github.com/steveyegge/beads/internal/types"
)

// runHook executes the hook and enforces a timeout on Windows.
// Windows lacks Unix-style process groups; on timeout we best-effort kill
// the started process. Descendant processes may survive if they detach,
// but this preserves previous behavior while keeping tests green on Windows.
func (r *Runner) runHook(hookPath, event string, issue *types.Issue) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	issueJSON, err := json.Marshal(issue)
	if err != nil {
		return fmt.Errorf("marshal issue for hook: %w", err)
	}

	cmd := exec.CommandContext(ctx, hookPath, issue.ID, event)
	cmd.Stdin = bytes.NewReader(issueJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start hook %s: %w", hookPath, err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return ctx.Err()
	case err := <-done:
		return fmt.Errorf("hook %s failed: %w", hookPath, err)
	}
}
