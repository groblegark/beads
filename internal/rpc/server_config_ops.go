package rpc

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/eventbus"
)

// handleGetConfig retrieves a config value from the database
func (s *Server) handleGetConfig(req *Request) Response {
	var args GetConfigArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid get_config args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Get config value from database
	value, err := store.GetConfig(ctx, args.Key)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get config %q: %v", args.Key, err),
		}
	}

	result := GetConfigResponse{
		Key:   args.Key,
		Value: value,
	}

	return jsonOK(result)
}

// handleConfigSet sets a config value in the database (bd-wmil)
func (s *Server) handleConfigSet(req *Request) Response {
	var args ConfigSetArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid config_set args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Set config value in database
	if err := store.SetConfig(ctx, args.Key, args.Value); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to set config %q: %v", args.Key, err),
		}
	}

	s.emitConfigEvent(eventbus.EventConfigSet, eventbus.ConfigEventPayload{
		Key:   args.Key,
		Value: args.Value,
		Actor: s.reqActor(req),
	}, s.reqActor(req))

	result := ConfigSetResponse{
		Key:   args.Key,
		Value: args.Value,
	}

	return jsonOK(result)
}

// handleConfigList lists config values from the database, optionally filtered by namespace (bd-wmil, bd-32tl9)
func (s *Server) handleConfigList(req *Request) Response {
	var args ConfigListArgs
	if req.Args != nil {
		// Args are optional — ignore parse errors for backward compatibility
		_ = json.Unmarshal(req.Args, &args)
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Get all config values from database
	config, err := store.GetAllConfig(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list config: %v", err),
		}
	}

	// Filter by namespace prefix if specified (bd-32tl9)
	if args.Namespace != "" {
		prefix := args.Namespace
		// Normalize: ensure prefix ends with "/" for matching
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		filtered := make(map[string]string)
		for key, value := range config {
			if strings.HasPrefix(key, prefix) {
				filtered[key] = value
			}
		}
		config = filtered
	}

	result := ConfigListResponse{
		Config: config,
	}

	return jsonOK(result)
}

// handleConfigUnset deletes a config value from the database (bd-wmil)
func (s *Server) handleConfigUnset(req *Request) Response {
	var args ConfigUnsetArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid config_unset args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Delete config value from database
	if err := store.DeleteConfig(ctx, args.Key); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to unset config %q: %v", args.Key, err),
		}
	}

	s.emitConfigEvent(eventbus.EventConfigUnset, eventbus.ConfigEventPayload{
		Key:   args.Key,
		Actor: s.reqActor(req),
	}, s.reqActor(req))

	result := ConfigUnsetResponse{
		Key: args.Key,
	}

	return jsonOK(result)
}
