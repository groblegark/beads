package rpc

import "encoding/json"

// DirtyCountResult is the response for the dirty_count operation.
type DirtyCountResult struct {
	Count int `json:"count"`
}

// handleDirtyCount returns the number of dirty issues.
func (s *Server) handleDirtyCount(req *Request) Response {
	ctx, cancel := s.reqCtx(req)
	defer cancel()

	count, err := s.storage.DirtyCount(ctx)
	if err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	data, _ := json.Marshal(DirtyCountResult{Count: count})
	return Response{Success: true, Data: data}
}

// DirtyFlushResult is the response for the dirty_flush operation.
type DirtyFlushResult struct {
	Orphaned int64 `json:"orphaned"`
	Exported int64 `json:"exported"`
}

// handleDirtyFlush removes stale dirty issues and returns removal counts.
func (s *Server) handleDirtyFlush(req *Request) Response {
	ctx, cancel := s.reqCtx(req)
	defer cancel()

	orphaned, exported, err := s.storage.FlushStaleDirtyIssues(ctx)
	if err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	data, _ := json.Marshal(DirtyFlushResult{Orphaned: orphaned, Exported: exported})
	return Response{Success: true, Data: data}
}
