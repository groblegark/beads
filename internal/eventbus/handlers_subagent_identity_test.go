package eventbus

import (
	"context"
	"strings"
	"testing"
)

func TestSubagentIdentityHandler_InjectsName(t *testing.T) {
	h := &SubagentIdentityHandler{}

	event := &Event{
		Type:      EventSubagentStart,
		Actor:     "tall-duck",
		AgentType: "researcher",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if len(result.Inject) != 1 {
		t.Fatalf("expected 1 injected message, got %d", len(result.Inject))
	}

	injected := result.Inject[0]
	if !strings.Contains(injected, "tall-duck/researcher") {
		t.Errorf("expected injected message to contain 'tall-duck/researcher', got: %s", injected)
	}
	if !strings.Contains(injected, "BD_ACTOR=") {
		t.Errorf("expected injected message to contain 'BD_ACTOR=', got: %s", injected)
	}
}

func TestSubagentIdentityHandler_NoAgentType(t *testing.T) {
	h := &SubagentIdentityHandler{}

	event := &Event{
		Type:  EventSubagentStart,
		Actor: "tall-duck",
		// No AgentType
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if len(result.Inject) != 0 {
		t.Errorf("expected no injection when agent_type is empty, got %d", len(result.Inject))
	}
}

func TestSubagentIdentityHandler_NoParentActor(t *testing.T) {
	h := &SubagentIdentityHandler{}

	event := &Event{
		Type:      EventSubagentStart,
		AgentType: "Explore",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if len(result.Inject) != 1 {
		t.Fatalf("expected 1 injected message, got %d", len(result.Inject))
	}

	// Without parent actor, just use the type
	if !strings.Contains(result.Inject[0], `"explore"`) {
		t.Errorf("expected 'explore' in injection, got: %s", result.Inject[0])
	}
}

func TestSubagentIdentityHandler_NormalizesType(t *testing.T) {
	h := &SubagentIdentityHandler{}

	event := &Event{
		Type:      EventSubagentStart,
		Actor:     "keen-wasp",
		AgentType: "General Purpose",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if len(result.Inject) != 1 {
		t.Fatalf("expected 1 injected message, got %d", len(result.Inject))
	}

	if !strings.Contains(result.Inject[0], "keen-wasp/general-purpose") {
		t.Errorf("expected normalized name, got: %s", result.Inject[0])
	}
}

func TestSubagentIdentityHandler_InDefaultHandlers(t *testing.T) {
	handlers := DefaultHandlers()
	found := false
	for _, h := range handlers {
		if h.ID() == "subagent-identity" {
			found = true
			if h.Priority() != 15 {
				t.Errorf("expected priority 15, got %d", h.Priority())
			}
			break
		}
	}
	if !found {
		t.Error("subagent-identity handler not found in DefaultHandlers()")
	}
}
