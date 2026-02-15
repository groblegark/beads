package main

import (
	"testing"
)

func TestOutputHealthResult_JSON(t *testing.T) {
	// Verify JSON output doesn't panic with various states
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()

	tests := []struct {
		name   string
		result healthResult
	}{
		{
			name: "healthy",
			result: healthResult{
				Status:         "healthy",
				Mode:           "local",
				Version:        "0.61.0",
				CLIVersion:     "0.61.0",
				Compatible:     true,
				Uptime:         3600,
				DBResponseTime: 1.5,
				ResponseTime:   12,
			},
		},
		{
			name: "unreachable remote",
			result: healthResult{
				Status:       "unreachable",
				Mode:         "remote",
				Host:         "https://daemon.example.com",
				CLIVersion:   "0.61.0",
				ResponseTime: 5000,
				Error:        "connection refused",
			},
		},
		{
			name: "version mismatch",
			result: healthResult{
				Status:         "healthy",
				Mode:           "remote",
				Host:           "10.0.0.1:8080",
				Version:        "0.60.0",
				CLIVersion:     "0.61.0",
				Compatible:     false,
				Uptime:         86400,
				DBResponseTime: 2.3,
				ResponseTime:   45,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify it doesn't panic
			outputHealthResult(tt.result)
		})
	}
}

func TestOutputHealthResult_Human(t *testing.T) {
	// Verify human output doesn't panic with various states
	origJSON := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = origJSON }()

	tests := []struct {
		name   string
		result healthResult
	}{
		{
			name: "healthy local",
			result: healthResult{
				Status:         "healthy",
				Mode:           "local",
				Version:        "0.61.0",
				CLIVersion:     "0.61.0",
				Compatible:     true,
				Uptime:         120,
				DBResponseTime: 0.8,
				ResponseTime:   5,
			},
		},
		{
			name: "degraded remote",
			result: healthResult{
				Status:         "degraded",
				Mode:           "remote",
				Host:           "daemon.example.com",
				Version:        "0.61.0",
				CLIVersion:     "0.61.0",
				Compatible:     true,
				Uptime:         7200,
				DBResponseTime: 50.0,
				ResponseTime:   200,
			},
		},
		{
			name: "unreachable no error",
			result: healthResult{
				Status:       "unreachable",
				Mode:         "local",
				CLIVersion:   "0.61.0",
				ResponseTime: 200,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputHealthResult(tt.result)
		})
	}
}
