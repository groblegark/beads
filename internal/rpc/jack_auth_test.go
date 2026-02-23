package rpc

import (
	"testing"
)

func TestParseJackTarget(t *testing.T) {
	tests := []struct {
		name         string
		target       string
		wantType     string
		wantNS       string
		wantName     string
		wantErr      bool
	}{
		{
			name:     "simple pod target",
			target:   "pod/my-app",
			wantType: "pod", wantNS: "", wantName: "my-app",
		},
		{
			name:     "pod with namespace",
			target:   "pod/default/my-app",
			wantType: "pod", wantNS: "default", wantName: "my-app",
		},
		{
			name:     "deployment with namespace",
			target:   "deployment/gastown-rwx/bd-daemon",
			wantType: "deployment", wantNS: "gastown-rwx", wantName: "bd-daemon",
		},
		{
			name:     "statefulset",
			target:   "statefulset/gastown/dolt",
			wantType: "statefulset", wantNS: "gastown", wantName: "dolt",
		},
		{
			name:     "configmap",
			target:   "configmap/default/app-config",
			wantType: "configmap", wantNS: "default", wantName: "app-config",
		},
		{
			name:     "external target",
			target:   "external://aws/rds/mydb-prod",
			wantType: "external", wantNS: "aws", wantName: "rds/mydb-prod",
		},
		{
			name:    "empty target",
			target:  "",
			wantErr: true,
		},
		{
			name:    "invalid external target",
			target:  "external://aws",
			wantErr: true,
		},
		{
			name:    "bare word",
			target:  "myapp",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resType, ns, name, err := ParseJackTarget(tt.target)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseJackTarget(%q) expected error, got nil", tt.target)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseJackTarget(%q) unexpected error: %v", tt.target, err)
			}
			if resType != tt.wantType {
				t.Errorf("type = %q, want %q", resType, tt.wantType)
			}
			if ns != tt.wantNS {
				t.Errorf("namespace = %q, want %q", ns, tt.wantNS)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
		})
	}
}

func TestAuthorizeJackTarget(t *testing.T) {
	tests := []struct {
		name         string
		target       string
		labels       []string
		wantAllowed  bool
		wantGate     bool
	}{
		{
			name:        "debug label auto-approves",
			target:      "statefulset/prod/dolt",
			labels:      []string{"jack:debug"},
			wantAllowed: true,
		},
		{
			name:        "non-prod pod auto-approved",
			target:      "pod/dev/my-app",
			labels:      []string{"jack:hotfix"},
			wantAllowed: true,
		},
		{
			name:        "pod without namespace auto-approved",
			target:      "pod/my-app",
			labels:      []string{"jack:config"},
			wantAllowed: true,
		},
		{
			name:     "statefulset requires gate",
			target:   "statefulset/dev/dolt",
			labels:   []string{"jack:failover"},
			wantGate: true,
		},
		{
			name:     "secret requires gate",
			target:   "secret/default/db-creds",
			labels:   []string{"jack:config"},
			wantGate: true,
		},
		{
			name:     "production namespace requires gate",
			target:   "pod/production/my-app",
			labels:   []string{"jack:hotfix"},
			wantGate: true,
		},
		{
			name:     "gastown namespace requires gate",
			target:   "deployment/gastown/bd-daemon",
			labels:   []string{"jack:config"},
			wantGate: true,
		},
		{
			name:     "gastown-uat namespace requires gate",
			target:   "pod/gastown-uat/bd-daemon",
			labels:   []string{"jack:debug"},
			wantAllowed: true, // jack:debug overrides
		},
		{
			name:     "external target requires gate",
			target:   "external://aws/rds/mydb-prod",
			labels:   []string{"jack:config"},
			wantGate: true,
		},
		{
			name:   "invalid target not allowed",
			target: "",
			labels: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AuthorizeJackTarget(tt.target, tt.labels)
			if tt.wantAllowed && !result.Allowed {
				t.Errorf("expected Allowed=true, got Allowed=%v RequiresGate=%v Reason=%q",
					result.Allowed, result.RequiresGate, result.Reason)
			}
			if tt.wantGate && !result.RequiresGate {
				t.Errorf("expected RequiresGate=true, got Allowed=%v RequiresGate=%v Reason=%q",
					result.Allowed, result.RequiresGate, result.Reason)
			}
			if !tt.wantAllowed && !tt.wantGate {
				if result.Allowed || result.RequiresGate {
					t.Errorf("expected neither Allowed nor RequiresGate, got Allowed=%v RequiresGate=%v",
						result.Allowed, result.RequiresGate)
				}
			}
		})
	}
}

func TestValidateJackTarget(t *testing.T) {
	valid := []string{
		"pod/my-app",
		"deployment/default/bd-daemon",
		"statefulset/gastown/dolt",
		"configmap/dev/app-config",
		"external://aws/rds/mydb-prod",
	}
	for _, target := range valid {
		if err := ValidateJackTarget(target); err != nil {
			t.Errorf("ValidateJackTarget(%q) unexpected error: %v", target, err)
		}
	}

	invalid := []string{
		"",
		"myapp",
		"external://aws",
	}
	for _, target := range invalid {
		if err := ValidateJackTarget(target); err == nil {
			t.Errorf("ValidateJackTarget(%q) expected error, got nil", target)
		}
	}
}
