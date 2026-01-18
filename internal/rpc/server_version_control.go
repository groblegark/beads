package rpc

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// =============================================================================
// Version Control Operations (Dolt backend only)
// =============================================================================

// handleVCHistory handles the vc_history operation
func (s *Server) handleVCHistory(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	var args VCHistoryArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments: %v", err),
		}
	}

	if args.IssueID == "" {
		return Response{
			Success: false,
			Error:   "issue_id is required",
		}
	}

	ctx := s.reqCtx(req)
	entries, err := vs.History(ctx, args.IssueID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get history: %v", err),
		}
	}

	// Convert to RPC response format
	var vcEntries []*VCHistoryEntry
	for _, e := range entries {
		vcEntries = append(vcEntries, &VCHistoryEntry{
			CommitHash: e.CommitHash,
			Committer:  e.Committer,
			CommitDate: e.CommitDate.Format("2006-01-02T15:04:05Z07:00"),
			Issue:      e.Issue,
		})
	}

	data, _ := json.Marshal(VCHistoryResponse{Entries: vcEntries})
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleVCCommit handles the vc_commit operation
func (s *Server) handleVCCommit(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	var args VCCommitArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments: %v", err),
		}
	}

	if args.Message == "" {
		return Response{
			Success: false,
			Error:   "commit message is required",
		}
	}

	ctx := s.reqCtx(req)
	if err := vs.Commit(ctx, args.Message); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to commit: %v", err),
		}
	}

	// Get the commit hash of the new commit
	commitHash, err := vs.GetCurrentCommit(ctx)
	if err != nil {
		// Commit succeeded but couldn't get hash - still return success
		data, _ := json.Marshal(VCCommitResponse{CommitHash: ""})
		return Response{
			Success: true,
			Data:    data,
		}
	}

	data, _ := json.Marshal(VCCommitResponse{CommitHash: commitHash})
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleVCCurrentCommit handles the vc_current_commit operation
func (s *Server) handleVCCurrentCommit(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	ctx := s.reqCtx(req)
	commitHash, err := vs.GetCurrentCommit(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get current commit: %v", err),
		}
	}

	data, _ := json.Marshal(VCCurrentCommitResponse{CommitHash: commitHash})
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleVCBranch handles the vc_branch operation
func (s *Server) handleVCBranch(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	var args VCBranchArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments: %v", err),
		}
	}

	if args.Name == "" {
		return Response{
			Success: false,
			Error:   "branch name is required",
		}
	}

	ctx := s.reqCtx(req)
	if err := vs.Branch(ctx, args.Name); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create branch: %v", err),
		}
	}

	return Response{
		Success: true,
	}
}

// handleVCCurrentBranch handles the vc_current_branch operation
func (s *Server) handleVCCurrentBranch(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	ctx := s.reqCtx(req)
	branch, err := vs.CurrentBranch(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get current branch: %v", err),
		}
	}

	data, _ := json.Marshal(VCCurrentBranchResponse{Branch: branch})
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleVCListBranches handles the vc_list_branches operation
func (s *Server) handleVCListBranches(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	ctx := s.reqCtx(req)
	branches, err := vs.ListBranches(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list branches: %v", err),
		}
	}

	data, _ := json.Marshal(VCListBranchesResponse{Branches: branches})
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleVCDiff handles the vc_diff operation
func (s *Server) handleVCDiff(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	var args VCDiffArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments: %v", err),
		}
	}

	if args.FromRef == "" || args.ToRef == "" {
		return Response{
			Success: false,
			Error:   "from_ref and to_ref are required",
		}
	}

	ctx := s.reqCtx(req)
	entries, err := vs.Diff(ctx, args.FromRef, args.ToRef)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get diff: %v", err),
		}
	}

	// Convert to RPC response format
	var vcEntries []*VCDiffEntry
	for _, e := range entries {
		vcEntries = append(vcEntries, &VCDiffEntry{
			IssueID:  e.IssueID,
			DiffType: e.DiffType,
			OldValue: e.OldValue,
			NewValue: e.NewValue,
		})
	}

	data, _ := json.Marshal(VCDiffResponse{Entries: vcEntries})
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleVCMerge handles the vc_merge operation
func (s *Server) handleVCMerge(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	var args VCMergeArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments: %v", err),
		}
	}

	if args.Branch == "" {
		return Response{
			Success: false,
			Error:   "branch name is required",
		}
	}

	ctx := s.reqCtx(req)
	conflicts, err := vs.Merge(ctx, args.Branch)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to merge: %v", err),
		}
	}

	// Convert conflicts to RPC format
	var vcConflicts []*VCConflict
	for _, c := range conflicts {
		vcConflicts = append(vcConflicts, &VCConflict{
			IssueID:     c.IssueID,
			Field:       c.Field,
			OursValue:   c.OursValue,
			TheirsValue: c.TheirsValue,
		})
	}

	data, _ := json.Marshal(VCMergeResponse{
		Success:   len(vcConflicts) == 0,
		Conflicts: vcConflicts,
	})
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleVCGetConflicts handles the vc_get_conflicts operation
func (s *Server) handleVCGetConflicts(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	ctx := s.reqCtx(req)
	conflicts, err := vs.GetConflicts(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get conflicts: %v", err),
		}
	}

	// Convert conflicts to RPC format
	var vcConflicts []*VCConflict
	for _, c := range conflicts {
		vcConflicts = append(vcConflicts, &VCConflict{
			IssueID:     c.IssueID,
			Field:       c.Field,
			OursValue:   c.OursValue,
			TheirsValue: c.TheirsValue,
		})
	}

	data, _ := json.Marshal(VCGetConflictsResponse{Conflicts: vcConflicts})
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleVCResolveConflicts handles the vc_resolve_conflicts operation
func (s *Server) handleVCResolveConflicts(req *Request) Response {
	vs, ok := storage.AsVersioned(s.storage)
	if !ok {
		return Response{
			Success: false,
			Error:   "version control operations require Dolt backend",
		}
	}

	var args VCResolveConflictsArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments: %v", err),
		}
	}

	if args.Table == "" {
		return Response{
			Success: false,
			Error:   "table name is required",
		}
	}

	if args.Strategy != "ours" && args.Strategy != "theirs" {
		return Response{
			Success: false,
			Error:   "strategy must be 'ours' or 'theirs'",
		}
	}

	ctx := s.reqCtx(req)
	if err := vs.ResolveConflicts(ctx, args.Table, args.Strategy); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve conflicts: %v", err),
		}
	}

	return Response{
		Success: true,
	}
}
