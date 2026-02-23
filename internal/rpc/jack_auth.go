package rpc

import (
	"fmt"
	"strings"
)

// JackTargetAuthResult represents the authorization decision for a jack target.
type JackTargetAuthResult struct {
	Allowed      bool   // Whether the target is auto-approved
	RequiresGate bool   // Whether a decision gate is needed
	Reason       string // Explanation of the authorization decision
}

// JackTargetGrammar defines the expected format for jack targets.
// K8s format: <resource-type>/<namespace>/<name> or <resource-type>/<name>
// External: external://<system>/<type>/<id>
const JackTargetGrammar = `Target format:
  K8s:      <resource-type>/<namespace>/<name>  (e.g., pod/default/my-app)
            <resource-type>/<name>              (e.g., pod/my-app, uses current namespace)
  External: external://<system>/<type>/<id>     (e.g., external://aws/rds/mydb-prod)`

// ParseJackTarget parses a target string into its components.
// Returns resource type, namespace (may be empty), and name.
func ParseJackTarget(target string) (resourceType, namespace, name string, err error) {
	if target == "" {
		return "", "", "", fmt.Errorf("target is required")
	}

	// External targets: external://<system>/<type>/<id>
	if strings.HasPrefix(target, "external://") {
		parts := strings.SplitN(target[len("external://"):], "/", 3)
		if len(parts) < 3 {
			return "", "", "", fmt.Errorf("invalid external target %q: expected external://<system>/<type>/<id>", target)
		}
		return "external", parts[0], parts[1] + "/" + parts[2], nil
	}

	// K8s targets: <resource-type>/<namespace>/<name> or <resource-type>/<name>
	parts := strings.SplitN(target, "/", 3)
	switch len(parts) {
	case 2:
		// resource-type/name (no namespace)
		return parts[0], "", parts[1], nil
	case 3:
		// resource-type/namespace/name
		return parts[0], parts[1], parts[2], nil
	default:
		return "", "", "", fmt.Errorf("invalid target %q: expected <resource-type>/<name> or <resource-type>/<namespace>/<name>", target)
	}
}

// AuthorizeJackTarget checks whether a jack target requires additional authorization.
// Returns an authorization result indicating auto-approved, requires-gate, or denied.
//
// Tiered authorization per design doc Part 8.1:
//   - Auto-approved: non-production namespaces, debug pods, jack:debug labels
//   - Requires decision gate: production namespaces, StatefulSets, Secrets,
//     ConfigMaps containing credentials
func AuthorizeJackTarget(target string, labels []string) JackTargetAuthResult {
	resourceType, namespace, _, err := ParseJackTarget(target)
	if err != nil {
		return JackTargetAuthResult{
			Allowed: false,
			Reason:  err.Error(),
		}
	}

	// Check if jack:debug label is present (always auto-approved)
	for _, label := range labels {
		if label == "jack:debug" {
			return JackTargetAuthResult{
				Allowed: true,
				Reason:  "auto-approved: jack:debug label",
			}
		}
	}

	// External targets always require a gate
	if resourceType == "external" {
		return JackTargetAuthResult{
			RequiresGate: true,
			Reason:       "external targets require human approval",
		}
	}

	// Sensitive resource types always require a gate
	sensitiveTypes := map[string]bool{
		"statefulset": true,
		"secret":      true,
		"secrets":     true,
	}
	if sensitiveTypes[strings.ToLower(resourceType)] {
		return JackTargetAuthResult{
			RequiresGate: true,
			Reason:       fmt.Sprintf("%s resources require human approval", resourceType),
		}
	}

	// Production namespaces require a gate
	prodNamespaces := map[string]bool{
		"production": true, "prod": true,
		"gastown":     true,
		"gastown-uat": true,
	}
	if prodNamespaces[strings.ToLower(namespace)] {
		return JackTargetAuthResult{
			RequiresGate: true,
			Reason:       fmt.Sprintf("production namespace %q requires human approval", namespace),
		}
	}

	// Non-production namespaces and debug resources are auto-approved
	return JackTargetAuthResult{
		Allowed: true,
		Reason:  "auto-approved: non-production target",
	}
}

// ValidateJackTarget validates that a target string is well-formed.
func ValidateJackTarget(target string) error {
	_, _, _, err := ParseJackTarget(target)
	return err
}
