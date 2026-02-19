package gate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSBackend stores gate state in a NATS KV bucket. Keys are formatted as
// "<agent-base-name>.<gate-id>". This provides daemon-authoritative state with
// TTLs, audit trail (KV history), and no cross-session fallback needed. (bd-vecxd)
type NATSBackend struct {
	kv nats.KeyValue
}

// NewNATSBackend creates a gate backend backed by the given NATS KV bucket.
func NewNATSBackend(kv nats.KeyValue) *NATSBackend {
	return &NATSBackend{kv: kv}
}

// kvKey builds the NATS KV key: "<agent>.<gateID>".
func kvKey(agent, gateID string) string {
	return agent + "." + gateID
}

func (b *NATSBackend) Mark(agent, gateID string, opts MarkOpts) error {
	entry := GateEntry{
		Satisfied: true,
		Mechanism: opts.Mechanism,
		Actor:     opts.Actor,
		SessionID: opts.SessionID,
		Timestamp: time.Now().Unix(),
	}
	if opts.TTL > 0 {
		entry.TTL = int(opts.TTL.Seconds())
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal gate entry: %w", err)
	}

	_, err = b.kv.Put(kvKey(agent, gateID), data)
	if err != nil {
		return fmt.Errorf("put gate %s.%s: %w", agent, gateID, err)
	}
	return nil
}

func (b *NATSBackend) Clear(agent, gateID string) error {
	err := b.kv.Delete(kvKey(agent, gateID))
	if err != nil && err != nats.ErrKeyNotFound {
		return fmt.Errorf("delete gate %s.%s: %w", agent, gateID, err)
	}
	return nil
}

func (b *NATSBackend) ClearAll(agent string) error {
	keys, err := b.kv.Keys()
	if err != nil {
		if err == nats.ErrNoKeysFound {
			return nil
		}
		return fmt.Errorf("list keys: %w", err)
	}
	prefix := agent + "."
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			_ = b.kv.Delete(k)
		}
	}
	return nil
}

func (b *NATSBackend) ClearGateAllAgents(gateID string) error {
	keys, err := b.kv.Keys()
	if err != nil {
		if err == nats.ErrNoKeysFound {
			return nil
		}
		return fmt.Errorf("list keys: %w", err)
	}
	suffix := "." + gateID
	for _, k := range keys {
		if strings.HasSuffix(k, suffix) {
			_ = b.kv.Delete(k)
		}
	}
	return nil
}

func (b *NATSBackend) IsSatisfied(agent, gateID string) bool {
	entry := b.Get(agent, gateID)
	return entry != nil && entry.Satisfied
}

func (b *NATSBackend) Get(agent, gateID string) *GateEntry {
	kve, err := b.kv.Get(kvKey(agent, gateID))
	if err != nil {
		return nil
	}

	var entry GateEntry
	if err := json.Unmarshal(kve.Value(), &entry); err != nil {
		return nil
	}

	// Check TTL expiration (application-level, since NATS KV bucket-level
	// TTL applies to all keys equally which isn't what we want).
	if entry.TTL > 0 {
		expires := time.Unix(entry.Timestamp, 0).Add(time.Duration(entry.TTL) * time.Second)
		if time.Now().After(expires) {
			// Expired — clean up and return nil.
			_ = b.kv.Delete(kvKey(agent, gateID))
			return nil
		}
	}

	return &entry
}
