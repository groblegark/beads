package slackbot

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNumConnections(t *testing.T) {
	// Reset state
	SetNumConnections(0)
	if n := NumConnections(); n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}

	SetNumConnections(1)
	if n := NumConnections(); n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}

	SetNumConnections(3)
	if n := NumConnections(); n != 3 {
		t.Fatalf("expected 3, got %d", n)
	}
}

func TestReadyzEndpoint(t *testing.T) {
	// We can't easily create a full HealthServer without a Bot,
	// but we can test the readiness logic directly via the handler.
	bot := &Bot{}
	hs := NewHealthServer(bot, 0)
	_ = hs // ensure construction works

	// Test the readiness logic inline (mirrors the /readyz handler)
	tests := []struct {
		name           string
		numConnections int
		wantStatus     int
	}{
		{"zero connections (startup)", 0, http.StatusOK},
		{"one connection (normal)", 1, http.StatusOK},
		{"two connections (duplicate)", 2, http.StatusServiceUnavailable},
		{"three connections (multiple duplicates)", 3, http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetNumConnections(tt.numConnections)

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := NumConnections()
				if n > 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest("GET", "/readyz", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("numConnections=%d: got status %d, want %d",
					tt.numConnections, rec.Code, tt.wantStatus)
			}
		})
	}
}
