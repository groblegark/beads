package slackbot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// agentCardState is the serialized form of an agent status card.
type agentCardState struct {
	ChannelID string `json:"channel_id"`
	Timestamp string `json:"timestamp"`
}

// decisionMessageState is the serialized form of a decision's Slack message reference.
type decisionMessageState struct {
	ChannelID string `json:"channel_id"`
	Timestamp string `json:"timestamp"`
	Agent     string `json:"agent,omitempty"`
}

// dashboardState is the serialized form of the dashboard message reference.
type dashboardState struct {
	ChannelID string `json:"channel_id"`
	Timestamp string `json:"timestamp"`
	LastHash  string `json:"last_hash,omitempty"`
}

// slackState is the top-level persisted state.
type slackState struct {
	AgentCards       map[string]agentCardState       `json:"agent_cards"`
	DecisionMessages map[string]decisionMessageState `json:"decision_messages,omitempty"`
	Dashboard        *dashboardState                 `json:"dashboard,omitempty"`
}

// StateManager persists Slack bot state across restarts.
// Currently tracks agent status card message references so the bot can
// resume threading decisions under existing cards.
type StateManager struct {
	mu       sync.RWMutex
	filePath string
	state    slackState
}

// NewStateManager creates a StateManager that persists to the settings directory.
func NewStateManager(beadsDir string) *StateManager {
	if beadsDir == "" {
		beadsDir = os.Getenv("BEADS_DIR")
	}
	if beadsDir == "" {
		beadsDir = "."
	}

	filePath := filepath.Join(beadsDir, "..", "settings", "slack_state.json")
	if beadsDir == "." {
		filePath = filepath.Join("settings", "slack_state.json")
	}

	sm := &StateManager{
		filePath: filePath,
		state: slackState{
			AgentCards:       make(map[string]agentCardState),
			DecisionMessages: make(map[string]decisionMessageState),
		},
	}

	_ = sm.Load()

	return sm
}

// GetAgentCard returns the persisted status card info for an agent, if any.
func (sm *StateManager) GetAgentCard(agent string) (channelID, timestamp string, ok bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	card, exists := sm.state.AgentCards[agent]
	if !exists {
		return "", "", false
	}
	return card.ChannelID, card.Timestamp, true
}

// SetAgentCard persists a status card reference for an agent.
func (sm *StateManager) SetAgentCard(agent, channelID, timestamp string) error {
	sm.mu.Lock()
	sm.state.AgentCards[agent] = agentCardState{
		ChannelID: channelID,
		Timestamp: timestamp,
	}
	sm.mu.Unlock()

	return sm.Save()
}

// RemoveAgentCard removes a persisted status card reference.
func (sm *StateManager) RemoveAgentCard(agent string) error {
	sm.mu.Lock()
	delete(sm.state.AgentCards, agent)
	sm.mu.Unlock()

	return sm.Save()
}

// GetDecisionMessage returns the persisted message info for a decision, if any.
func (sm *StateManager) GetDecisionMessage(decisionID string) (channelID, timestamp, agent string, ok bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	msg, exists := sm.state.DecisionMessages[decisionID]
	if !exists {
		return "", "", "", false
	}
	return msg.ChannelID, msg.Timestamp, msg.Agent, true
}

// SetDecisionMessage persists a message reference for a decision.
func (sm *StateManager) SetDecisionMessage(decisionID, channelID, timestamp, agent string) error {
	sm.mu.Lock()
	sm.state.DecisionMessages[decisionID] = decisionMessageState{
		ChannelID: channelID,
		Timestamp: timestamp,
		Agent:     agent,
	}
	sm.mu.Unlock()

	return sm.Save()
}

// RemoveDecisionMessage removes a persisted decision message reference.
func (sm *StateManager) RemoveDecisionMessage(decisionID string) error {
	sm.mu.Lock()
	delete(sm.state.DecisionMessages, decisionID)
	sm.mu.Unlock()

	return sm.Save()
}

// AllAgentCards returns a copy of all persisted agent card references.
func (sm *StateManager) AllAgentCards() map[string]agentCardState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]agentCardState, len(sm.state.AgentCards))
	for k, v := range sm.state.AgentCards {
		result[k] = v
	}
	return result
}

// GetDashboard returns the persisted dashboard message reference, if any.
func (sm *StateManager) GetDashboard() (channelID, timestamp, lastHash string, ok bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state.Dashboard == nil {
		return "", "", "", false
	}
	return sm.state.Dashboard.ChannelID, sm.state.Dashboard.Timestamp, sm.state.Dashboard.LastHash, true
}

// SetDashboard persists the dashboard message reference.
func (sm *StateManager) SetDashboard(channelID, timestamp, lastHash string) error {
	sm.mu.Lock()
	sm.state.Dashboard = &dashboardState{
		ChannelID: channelID,
		Timestamp: timestamp,
		LastHash:  lastHash,
	}
	sm.mu.Unlock()

	return sm.Save()
}

// Save writes state to disk using atomic write (temp file + rename).
func (sm *StateManager) Save() error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	dir := filepath.Dir(sm.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}

	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmpPath := sm.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, sm.filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// Load reads state from disk. Returns nil if file doesn't exist yet.
func (sm *StateManager) Load() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	data, err := os.ReadFile(sm.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read state file: %w", err)
	}

	var state slackState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse state file: %w", err)
	}

	if state.AgentCards == nil {
		state.AgentCards = make(map[string]agentCardState)
	}
	if state.DecisionMessages == nil {
		state.DecisionMessages = make(map[string]decisionMessageState)
	}

	sm.state = state
	return nil
}

// GetFilePath returns the path to the state file.
func (sm *StateManager) GetFilePath() string {
	return sm.filePath
}
