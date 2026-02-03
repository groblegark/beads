// Package rpc provides RPC server handlers for kv operations (bd-hdq5).
// These handlers enable kv set/get/clear/list to work in daemon mode.
package rpc

import (
	"encoding/json"
	"fmt"
	"strings"
)

// kvPrefix is prepended to all user keys to separate them from internal config
const kvPrefix = "kv."

// validateKVKey checks if a key is valid for the KV store.
// Returns an error if the key is invalid.
func validateKVKey(key string) error {
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("key cannot be only whitespace")
	}
	// Prevent keys that would create nested kv.kv.* prefixes
	if strings.HasPrefix(key, "kv.") {
		return fmt.Errorf("key cannot start with 'kv.' (would create nested prefix)")
	}
	// Prevent keys that look like internal config
	if strings.HasPrefix(key, "sync.") || strings.HasPrefix(key, "conflict.") ||
		strings.HasPrefix(key, "federation.") || strings.HasPrefix(key, "jira.") ||
		strings.HasPrefix(key, "linear.") || strings.HasPrefix(key, "export.") {
		return fmt.Errorf("key cannot start with reserved prefix %q", strings.Split(key, ".")[0]+".")
	}
	return nil
}

// handleKVSet handles the kv set RPC operation
func (s *Server) handleKVSet(req *Request) Response {
	var args KVSetArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid arguments: %v", err)}
	}

	// Validate key
	if err := validateKVKey(args.Key); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid key: %v", err)}
	}

	ctx := s.reqCtx(req)
	storageKey := kvPrefix + args.Key

	if err := s.storage.SetConfig(ctx, storageKey, args.Value); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("setting key: %v", err)}
	}

	result := &KVSetResult{
		Key:   args.Key,
		Value: args.Value,
	}

	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleKVGet handles the kv get RPC operation
func (s *Server) handleKVGet(req *Request) Response {
	var args KVGetArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid arguments: %v", err)}
	}

	ctx := s.reqCtx(req)
	storageKey := kvPrefix + args.Key

	value, err := s.storage.GetConfig(ctx, storageKey)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("getting key: %v", err)}
	}

	result := &KVGetResult{
		Key:   args.Key,
		Value: value,
		Found: value != "",
	}

	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleKVClear handles the kv clear RPC operation
func (s *Server) handleKVClear(req *Request) Response {
	var args KVClearArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid arguments: %v", err)}
	}

	// Validate key
	if err := validateKVKey(args.Key); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid key: %v", err)}
	}

	ctx := s.reqCtx(req)
	storageKey := kvPrefix + args.Key

	if err := s.storage.DeleteConfig(ctx, storageKey); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("deleting key: %v", err)}
	}

	result := &KVClearResult{
		Key:     args.Key,
		Deleted: true,
	}

	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleKVList handles the kv list RPC operation
func (s *Server) handleKVList(req *Request) Response {
	ctx := s.reqCtx(req)

	allConfig, err := s.storage.GetAllConfig(ctx)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("listing keys: %v", err)}
	}

	// Filter for kv.* keys and strip prefix
	kvPairs := make(map[string]string)
	for k, v := range allConfig {
		if strings.HasPrefix(k, kvPrefix) {
			userKey := strings.TrimPrefix(k, kvPrefix)
			kvPairs[userKey] = v
		}
	}

	result := &KVListResult{
		Pairs: kvPairs,
	}

	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}
