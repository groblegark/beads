package rpc

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Rig registration handlers (bd-jzx1m).
// Rigs are lightweight repository registrations stored as issues with TypeRig.
// They carry metadata (remote URL, local path, default branch, prefix) but
// perform no filesystem operations — purely a registry for associating beads
// with repositories and detecting branch conflicts at the roster level.

// rigMetaFromIssue extracts Rig fields from an issue's metadata JSON.
func rigMetaFromIssue(issue *types.Issue) *Rig {
	rig := &Rig{
		Name:      issue.Title,
		CreatedAt: issue.CreatedAt.Format(time.RFC3339),
		UpdatedAt: issue.UpdatedAt.Format(time.RFC3339),
	}
	if issue.Metadata == nil {
		return rig
	}
	var meta map[string]string
	if err := json.Unmarshal(issue.Metadata, &meta); err != nil {
		return rig
	}
	rig.RemoteURL = meta["remote_url"]
	rig.LocalPath = meta["local_path"]
	rig.DefaultBranch = meta["default_branch"]
	rig.Prefix = meta["prefix"]
	return rig
}

// rigToMetadata converts rig registration args to a JSON metadata blob.
func rigToMetadata(args *RigRegisterArgs) json.RawMessage {
	meta := map[string]string{
		"remote_url":     args.RemoteURL,
		"local_path":     args.LocalPath,
		"default_branch": args.DefaultBranch,
		"prefix":         args.Prefix,
	}
	data, _ := json.Marshal(meta)
	return data
}

// handleRigRegister creates or updates a rig registration.
func (s *Server) handleRigRegister(req *Request) Response {
	var args RigRegisterArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid rig_register args: %v", err)}
	}

	args.Name = strings.TrimSpace(args.Name)
	if args.Name == "" {
		return Response{Success: false, Error: "rig name is required"}
	}
	args.RemoteURL = strings.TrimSpace(args.RemoteURL)
	if args.RemoteURL == "" {
		return Response{Success: false, Error: "remote_url is required"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	actor := s.reqActor(req)

	// Derive the bead ID: prefix + "rig-" + name
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	idPrefix := ""
	if prefix != "" {
		idPrefix = prefix + "-"
	}
	rigID := idPrefix + "rig-" + strings.ToLower(strings.ReplaceAll(args.Name, " ", "-"))

	// Check if this rig already exists
	existing, _ := store.GetIssue(ctx, rigID)
	created := existing == nil

	now := time.Now().UTC()
	metadata := rigToMetadata(&args)

	if existing != nil {
		// Update existing rig
		updates := map[string]interface{}{
			"title":       args.Name,
			"description": fmt.Sprintf("Rig: %s (%s)", args.Name, args.RemoteURL),
			"metadata":    metadata,
			"updated_at":  now,
		}
		if err := store.UpdateIssue(ctx, rigID, updates, actor); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("failed to update rig: %v", err)}
		}
	} else {
		// Create new rig issue
		issue := &types.Issue{
			ID:          rigID,
			Title:       args.Name,
			Description: fmt.Sprintf("Rig: %s (%s)", args.Name, args.RemoteURL),
			Status:      types.StatusOpen,
			IssueType:   types.TypeRig,
			Priority:    4, // backlog — rigs are infrastructure, not work items
			Metadata:    metadata,
			CreatedAt:   now,
			UpdatedAt:   now,
			CreatedBy:   actor,
		}
		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("failed to create rig: %v", err)}
		}
	}

	// Emit mutation event
	updated, _ := store.GetIssue(ctx, rigID)
	if updated != nil {
		if created {
			s.emitMutationFor(MutationCreate, updated)
		} else {
			s.emitMutationFor(MutationUpdate, updated)
		}
	}

	nowStr := now.Format(time.RFC3339)
	result := Rig{
		Name:          args.Name,
		RemoteURL:     args.RemoteURL,
		LocalPath:     args.LocalPath,
		DefaultBranch: args.DefaultBranch,
		Prefix:        args.Prefix,
		CreatedAt:     nowStr,
		UpdatedAt:     nowStr,
	}
	return jsonOK(result)
}

// handleRigList lists all registered rigs.
func (s *Server) handleRigList(req *Request) Response {
	ctx, cancel := s.reqCtx(req)
	defer cancel()

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	filter := types.IssueFilter{
		IssueType: func() *types.IssueType { t := types.TypeRig; return &t }(),
	}
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to search rigs: %v", err)}
	}

	var rigs []Rig
	for _, issue := range issues {
		if issue.Status == types.StatusClosed {
			continue
		}
		rig := rigMetaFromIssue(issue)
		rigs = append(rigs, *rig)
	}

	result := RigListResult{Rigs: rigs}
	return jsonOK(result)
}

// handleRigShow returns details for a single rig by name.
func (s *Server) handleRigShow(req *Request) Response {
	var args RigShowArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid rig_show args: %v", err)}
	}

	args.Name = strings.TrimSpace(args.Name)
	if args.Name == "" {
		return Response{Success: false, Error: "rig name is required"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	// Try exact ID lookup first: prefix-rig-name
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	idPrefix := ""
	if prefix != "" {
		idPrefix = prefix + "-"
	}
	rigID := idPrefix + "rig-" + strings.ToLower(strings.ReplaceAll(args.Name, " ", "-"))

	issue, err := store.GetIssue(ctx, rigID)
	if err != nil || issue == nil {
		// Fall back to searching by title
		filter := types.IssueFilter{
			IssueType: func() *types.IssueType { t := types.TypeRig; return &t }(),
		}
		issues, searchErr := store.SearchIssues(ctx, args.Name, filter)
		if searchErr != nil {
			return Response{Success: false, Error: fmt.Sprintf("rig %q not found", args.Name)}
		}
		for _, iss := range issues {
			if iss.Status != types.StatusClosed && strings.EqualFold(iss.Title, args.Name) {
				issue = iss
				break
			}
		}
		if issue == nil {
			return Response{Success: false, Error: fmt.Sprintf("rig %q not found", args.Name)}
		}
	}

	rig := rigMetaFromIssue(issue)
	return jsonOK(rig)
}

// handleRigRemove soft-deletes a rig by closing its issue.
func (s *Server) handleRigRemove(req *Request) Response {
	var args RigRemoveArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid rig_remove args: %v", err)}
	}

	args.Name = strings.TrimSpace(args.Name)
	if args.Name == "" {
		return Response{Success: false, Error: "rig name is required"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	actor := s.reqActor(req)

	// Find the rig by ID
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	idPrefix := ""
	if prefix != "" {
		idPrefix = prefix + "-"
	}
	rigID := idPrefix + "rig-" + strings.ToLower(strings.ReplaceAll(args.Name, " ", "-"))

	issue, err := store.GetIssue(ctx, rigID)
	if err != nil || issue == nil {
		return Response{Success: false, Error: fmt.Sprintf("rig %q not found", args.Name)}
	}

	if issue.Status == types.StatusClosed {
		return Response{Success: false, Error: fmt.Sprintf("rig %q is already removed", args.Name)}
	}

	// Soft-delete: close the issue
	updates := map[string]interface{}{
		"status": string(types.StatusClosed),
	}
	if err := store.UpdateIssue(ctx, rigID, updates, actor); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to remove rig: %v", err)}
	}

	s.emitMutationFor(MutationUpdate, issue)

	return jsonOK(map[string]string{"removed": args.Name})
}
