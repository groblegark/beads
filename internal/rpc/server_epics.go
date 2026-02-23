package rpc

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

func (s *Server) handleEpicStatus(req *Request) Response {
	var epicArgs EpicStatusArgs
	if err := json.Unmarshal(req.Args, &epicArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid epic status args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	epics, err := store.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get epic status: %v", err),
		}
	}

	if epicArgs.EligibleOnly {
		filtered := []*types.EpicStatus{}
		for _, epic := range epics {
			if epic.EligibleForClose {
				filtered = append(filtered, epic)
			}
		}
		epics = filtered
	}

	data, err := json.Marshal(epics)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal epics: %v", err),
		}
	}

	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleEpicOverview(req *Request) Response {
	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	overviews, err := store.GetEpicOverview(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get epic overview: %v", err),
		}
	}

	if overviews == nil {
		overviews = []*types.EpicOverview{}
	}

	data, err := json.Marshal(overviews)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal overview: %v", err),
		}
	}

	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleEpicOrphanedChildren(req *Request) Response {
	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	orphans, err := store.GetOrphanedChildren(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get orphaned children: %v", err),
		}
	}

	if orphans == nil {
		orphans = []*types.OrphanedChild{}
	}

	data, err := json.Marshal(orphans)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal orphans: %v", err),
		}
	}

	return Response{
		Success: true,
		Data:    data,
	}
}
