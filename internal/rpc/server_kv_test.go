package rpc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// setupKVTestServer creates a test server with SQLite storage
func setupKVTestServer(t *testing.T) (*Server, storage.Storage) {
	t.Helper()
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Initialize database with required config
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	server := NewServer("/tmp/test.sock", store, tmpDir, dbPath)
	return server, store
}

func TestHandleKVSet_Success(t *testing.T) {
	server, _ := setupKVTestServer(t)

	args := KVSetArgs{
		Key:   "mykey",
		Value: "myvalue",
	}
	argsJSON, _ := json.Marshal(args)

	req := &Request{
		Operation: OpKVSet,
		Args:      argsJSON,
		Actor:     "test",
	}

	resp := server.handleKVSet(req)
	if !resp.Success {
		t.Fatalf("kv set failed: %s", resp.Error)
	}

	var result KVSetResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if result.Key != "mykey" {
		t.Errorf("expected key 'mykey', got %q", result.Key)
	}
	if result.Value != "myvalue" {
		t.Errorf("expected value 'myvalue', got %q", result.Value)
	}
}

func TestHandleKVSet_InvalidKey(t *testing.T) {
	server, _ := setupKVTestServer(t)

	tests := []struct {
		name string
		key  string
	}{
		{"empty key", ""},
		{"whitespace only", "   "},
		{"nested kv prefix", "kv.test"},
		{"reserved prefix sync", "sync.test"},
		{"reserved prefix jira", "jira.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := KVSetArgs{
				Key:   tt.key,
				Value: "value",
			}
			argsJSON, _ := json.Marshal(args)

			req := &Request{
				Operation: OpKVSet,
				Args:      argsJSON,
				Actor:     "test",
			}

			resp := server.handleKVSet(req)
			if resp.Success {
				t.Errorf("expected failure for invalid key %q", tt.key)
			}
		})
	}
}

func TestHandleKVGet_Found(t *testing.T) {
	server, store := setupKVTestServer(t)

	// Set a value directly in the store
	ctx := context.Background()
	if err := store.SetConfig(ctx, "kv.testkey", "testvalue"); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}

	args := KVGetArgs{
		Key: "testkey",
	}
	argsJSON, _ := json.Marshal(args)

	req := &Request{
		Operation: OpKVGet,
		Args:      argsJSON,
		Actor:     "test",
	}

	resp := server.handleKVGet(req)
	if !resp.Success {
		t.Fatalf("kv get failed: %s", resp.Error)
	}

	var result KVGetResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if result.Key != "testkey" {
		t.Errorf("expected key 'testkey', got %q", result.Key)
	}
	if result.Value != "testvalue" {
		t.Errorf("expected value 'testvalue', got %q", result.Value)
	}
	if !result.Found {
		t.Error("expected Found to be true")
	}
}

func TestHandleKVGet_NotFound(t *testing.T) {
	server, _ := setupKVTestServer(t)

	args := KVGetArgs{
		Key: "nonexistent",
	}
	argsJSON, _ := json.Marshal(args)

	req := &Request{
		Operation: OpKVGet,
		Args:      argsJSON,
		Actor:     "test",
	}

	resp := server.handleKVGet(req)
	if !resp.Success {
		t.Fatalf("kv get failed: %s", resp.Error)
	}

	var result KVGetResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if result.Found {
		t.Error("expected Found to be false for nonexistent key")
	}
	if result.Value != "" {
		t.Errorf("expected empty value, got %q", result.Value)
	}
}

func TestHandleKVClear_Success(t *testing.T) {
	server, store := setupKVTestServer(t)

	// Set a value first
	ctx := context.Background()
	if err := store.SetConfig(ctx, "kv.deleteme", "value"); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}

	args := KVClearArgs{
		Key: "deleteme",
	}
	argsJSON, _ := json.Marshal(args)

	req := &Request{
		Operation: OpKVClear,
		Args:      argsJSON,
		Actor:     "test",
	}

	resp := server.handleKVClear(req)
	if !resp.Success {
		t.Fatalf("kv clear failed: %s", resp.Error)
	}

	var result KVClearResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if result.Key != "deleteme" {
		t.Errorf("expected key 'deleteme', got %q", result.Key)
	}
	if !result.Deleted {
		t.Error("expected Deleted to be true")
	}

	// Verify the key is gone
	value, _ := store.GetConfig(ctx, "kv.deleteme")
	if value != "" {
		t.Errorf("expected key to be deleted, but got value %q", value)
	}
}

func TestHandleKVClear_InvalidKey(t *testing.T) {
	server, _ := setupKVTestServer(t)

	args := KVClearArgs{
		Key: "kv.nested",
	}
	argsJSON, _ := json.Marshal(args)

	req := &Request{
		Operation: OpKVClear,
		Args:      argsJSON,
		Actor:     "test",
	}

	resp := server.handleKVClear(req)
	if resp.Success {
		t.Error("expected failure for invalid key with kv. prefix")
	}
}

func TestHandleKVList_Empty(t *testing.T) {
	server, _ := setupKVTestServer(t)

	req := &Request{
		Operation: OpKVList,
		Args:      []byte(`{}`),
		Actor:     "test",
	}

	resp := server.handleKVList(req)
	if !resp.Success {
		t.Fatalf("kv list failed: %s", resp.Error)
	}

	var result KVListResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if result.Pairs == nil {
		t.Error("expected non-nil Pairs map")
	}
	if len(result.Pairs) != 0 {
		t.Errorf("expected empty pairs, got %d", len(result.Pairs))
	}
}

func TestHandleKVList_WithValues(t *testing.T) {
	server, store := setupKVTestServer(t)

	// Set some values
	ctx := context.Background()
	store.SetConfig(ctx, "kv.key1", "value1")
	store.SetConfig(ctx, "kv.key2", "value2")
	store.SetConfig(ctx, "kv.key3", "value3")
	// This should NOT appear (not a kv. prefix)
	store.SetConfig(ctx, "other.config", "ignored")

	req := &Request{
		Operation: OpKVList,
		Args:      []byte(`{}`),
		Actor:     "test",
	}

	resp := server.handleKVList(req)
	if !resp.Success {
		t.Fatalf("kv list failed: %s", resp.Error)
	}

	var result KVListResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	// Should have exactly 3 kv pairs (not the other.config)
	if len(result.Pairs) != 3 {
		t.Errorf("expected 3 pairs, got %d: %v", len(result.Pairs), result.Pairs)
	}

	if result.Pairs["key1"] != "value1" {
		t.Errorf("expected key1=value1, got %q", result.Pairs["key1"])
	}
	if result.Pairs["key2"] != "value2" {
		t.Errorf("expected key2=value2, got %q", result.Pairs["key2"])
	}
	if result.Pairs["key3"] != "value3" {
		t.Errorf("expected key3=value3, got %q", result.Pairs["key3"])
	}
}

func TestHandleKVSet_InvalidArgs(t *testing.T) {
	server, _ := setupKVTestServer(t)

	req := &Request{
		Operation: OpKVSet,
		Args:      []byte(`{"invalid json`),
		Actor:     "test",
	}

	resp := server.handleKVSet(req)
	if resp.Success {
		t.Error("expected failure for invalid JSON args")
	}
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestHandleKVGet_InvalidArgs(t *testing.T) {
	server, _ := setupKVTestServer(t)

	req := &Request{
		Operation: OpKVGet,
		Args:      []byte(`{"invalid json`),
		Actor:     "test",
	}

	resp := server.handleKVGet(req)
	if resp.Success {
		t.Error("expected failure for invalid JSON args")
	}
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestHandleKVClear_InvalidArgs(t *testing.T) {
	server, _ := setupKVTestServer(t)

	req := &Request{
		Operation: OpKVClear,
		Args:      []byte(`{"invalid json`),
		Actor:     "test",
	}

	resp := server.handleKVClear(req)
	if resp.Success {
		t.Error("expected failure for invalid JSON args")
	}
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

// TestKVRoundtrip tests the full set/get/list/clear cycle
func TestKVRoundtrip(t *testing.T) {
	server, _ := setupKVTestServer(t)

	// 1. Set a value
	setArgs := KVSetArgs{Key: "roundtrip", Value: "test123"}
	setJSON, _ := json.Marshal(setArgs)
	setResp := server.handleKVSet(&Request{Operation: OpKVSet, Args: setJSON, Actor: "test"})
	if !setResp.Success {
		t.Fatalf("set failed: %s", setResp.Error)
	}

	// 2. Get the value
	getArgs := KVGetArgs{Key: "roundtrip"}
	getJSON, _ := json.Marshal(getArgs)
	getResp := server.handleKVGet(&Request{Operation: OpKVGet, Args: getJSON, Actor: "test"})
	if !getResp.Success {
		t.Fatalf("get failed: %s", getResp.Error)
	}
	var getResult KVGetResult
	json.Unmarshal(getResp.Data, &getResult)
	if getResult.Value != "test123" {
		t.Errorf("expected 'test123', got %q", getResult.Value)
	}

	// 3. List values (should contain our key)
	listResp := server.handleKVList(&Request{Operation: OpKVList, Args: []byte(`{}`), Actor: "test"})
	if !listResp.Success {
		t.Fatalf("list failed: %s", listResp.Error)
	}
	var listResult KVListResult
	json.Unmarshal(listResp.Data, &listResult)
	if listResult.Pairs["roundtrip"] != "test123" {
		t.Errorf("expected 'roundtrip' in list, got %v", listResult.Pairs)
	}

	// 4. Clear the value
	clearArgs := KVClearArgs{Key: "roundtrip"}
	clearJSON, _ := json.Marshal(clearArgs)
	clearResp := server.handleKVClear(&Request{Operation: OpKVClear, Args: clearJSON, Actor: "test"})
	if !clearResp.Success {
		t.Fatalf("clear failed: %s", clearResp.Error)
	}

	// 5. Verify it's gone
	getResp2 := server.handleKVGet(&Request{Operation: OpKVGet, Args: getJSON, Actor: "test"})
	if !getResp2.Success {
		t.Fatalf("get after clear failed: %s", getResp2.Error)
	}
	var getResult2 KVGetResult
	json.Unmarshal(getResp2.Data, &getResult2)
	if getResult2.Found {
		t.Error("expected key to not be found after clear")
	}
}
