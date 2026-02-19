#!/usr/bin/env bash
# test-helm-template.sh — Validate helm chart renders cleanly (bd-pzrud)
#
# Tests:
#   1) helm template renders without errors (default + HA values)
#   2) output is valid YAML (python yaml.safe_load)
#   3) expected resources present (Deployment, StatefulSet, Service, etc.)
#   4) label selectors consistent (deployment selector matches pod template)
#   5) no hardcoded secrets in rendered output
#
# Does NOT require cluster access.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CHART_DIR="$REPO_ROOT/helm/bd-daemon"

PASSED=0
FAILED=0
ERRORS=()

pass() {
    PASSED=$((PASSED + 1))
    echo "  PASS: $1"
}

fail() {
    FAILED=$((FAILED + 1))
    ERRORS+=("$1")
    echo "  FAIL: $1"
}

# --- Pre-flight: ensure helm is available ---
if ! command -v helm &>/dev/null; then
    echo "ERROR: helm not found in PATH"
    exit 1
fi

# --- Pre-flight: ensure python3 with yaml module ---
if ! python3 -c "import yaml" &>/dev/null; then
    echo "WARNING: python3 yaml module not available, skipping YAML validation tests"
    SKIP_YAML=true
else
    SKIP_YAML=false
fi

# --- Update dependencies (needed for redis subchart) ---
echo "Updating chart dependencies..."
helm dependency update "$CHART_DIR" --skip-refresh >/dev/null 2>&1 || true

# ===========================================================================
# Test 1: helm template renders without errors
# ===========================================================================
echo ""
echo "=== Test 1: helm template renders without errors ==="

# 1a: Default values
DEFAULT_OUTPUT=$(mktemp)
if helm template test-release "$CHART_DIR" > "$DEFAULT_OUTPUT" 2>&1; then
    pass "default values render"
else
    fail "default values render — helm template exited non-zero"
    cat "$DEFAULT_OUTPUT"
fi

# 1b: HA values
HA_VALUES="$CHART_DIR/values/gastown-ha.yaml"
HA_OUTPUT=$(mktemp)
if [[ -f "$HA_VALUES" ]]; then
    if helm template test-release "$CHART_DIR" -f "$HA_VALUES" > "$HA_OUTPUT" 2>&1; then
        pass "gastown-ha values render"
    else
        fail "gastown-ha values render — helm template exited non-zero"
        cat "$HA_OUTPUT"
    fi
else
    echo "  SKIP: gastown-ha.yaml not found"
fi

# 1c: All features enabled
ALL_OUTPUT=$(mktemp)
if helm template test-release "$CHART_DIR" \
    --set dolt.statefulSet.enabled=true \
    --set redis.enabled=true \
    --set nats.enabled=true \
    --set externalSecrets.enabled=true \
    > "$ALL_OUTPUT" 2>&1; then
    pass "all-features-enabled render"
else
    fail "all-features-enabled render — helm template exited non-zero"
    cat "$ALL_OUTPUT"
fi

# ===========================================================================
# Test 2: output is valid YAML
# ===========================================================================
echo ""
echo "=== Test 2: rendered output is valid YAML ==="

validate_yaml() {
    local file="$1"
    local label="$2"
    if [[ "$SKIP_YAML" == "true" ]]; then
        echo "  SKIP: $label (no python3 yaml)"
        return
    fi
    if python3 -c "
import yaml, sys
with open('$file') as f:
    docs = list(yaml.safe_load_all(f))
    if not docs or all(d is None for d in docs):
        print('WARNING: no YAML documents found', file=sys.stderr)
        sys.exit(1)
" 2>&1; then
        pass "$label — valid YAML"
    else
        fail "$label — invalid YAML"
    fi
}

validate_yaml "$DEFAULT_OUTPUT" "default values"
validate_yaml "$HA_OUTPUT" "gastown-ha values"
validate_yaml "$ALL_OUTPUT" "all-features-enabled"

# ===========================================================================
# Test 3: expected resources present
# ===========================================================================
echo ""
echo "=== Test 3: expected resources present ==="

check_resource() {
    local file="$1"
    local kind="$2"
    local label="$3"
    if grep -q "^kind: $kind" "$file"; then
        pass "$label has $kind"
    else
        fail "$label missing $kind"
    fi
}

# Default values should have at minimum: Deployment, Service, ServiceAccount
check_resource "$DEFAULT_OUTPUT" "Deployment" "default"
check_resource "$DEFAULT_OUTPUT" "Service" "default"
check_resource "$DEFAULT_OUTPUT" "ServiceAccount" "default"

# HA values should additionally have: StatefulSet, ConfigMap
if [[ -f "$HA_VALUES" ]]; then
    check_resource "$HA_OUTPUT" "Deployment" "gastown-ha"
    check_resource "$HA_OUTPUT" "Service" "gastown-ha"
    check_resource "$HA_OUTPUT" "StatefulSet" "gastown-ha"
    check_resource "$HA_OUTPUT" "ConfigMap" "gastown-ha"
fi

# All-features should have StatefulSet (dolt + nats + redis)
check_resource "$ALL_OUTPUT" "StatefulSet" "all-features"
check_resource "$ALL_OUTPUT" "Service" "all-features"
check_resource "$ALL_OUTPUT" "Deployment" "all-features"

# ===========================================================================
# Test 4: label selectors consistent
# ===========================================================================
echo ""
echo "=== Test 4: label selectors consistent ==="

check_selectors() {
    local file="$1"
    local label="$2"
    if [[ "$SKIP_YAML" == "true" ]]; then
        echo "  SKIP: $label selector check (no python3 yaml)"
        return
    fi

    local result
    result=$(python3 -c "
import yaml, sys, json

with open('$file') as f:
    docs = list(yaml.safe_load_all(f))

errors = []
for doc in docs:
    if doc is None:
        continue
    kind = doc.get('kind', '')
    name = doc.get('metadata', {}).get('name', '')
    if kind not in ('Deployment', 'StatefulSet'):
        continue
    spec = doc.get('spec', {})
    selector = spec.get('selector', {}).get('matchLabels', {})
    template_labels = spec.get('template', {}).get('metadata', {}).get('labels', {})
    if not selector:
        errors.append(f'{kind}/{name}: missing spec.selector.matchLabels')
        continue
    for k, v in selector.items():
        if k not in template_labels:
            errors.append(f'{kind}/{name}: selector label {k}={v} missing from pod template')
        elif template_labels[k] != v:
            errors.append(f'{kind}/{name}: selector {k}={v} != template {k}={template_labels[k]}')

if errors:
    print('\\n'.join(errors))
    sys.exit(1)
" 2>&1)

    if [[ $? -eq 0 ]]; then
        pass "$label — selectors match pod template labels"
    else
        fail "$label — selector mismatch: $result"
    fi
}

check_selectors "$DEFAULT_OUTPUT" "default"
check_selectors "$HA_OUTPUT" "gastown-ha"
check_selectors "$ALL_OUTPUT" "all-features"

# ===========================================================================
# Test 5: no hardcoded secrets in rendered output
# ===========================================================================
echo ""
echo "=== Test 5: no hardcoded secrets in rendered output ==="

check_no_secrets() {
    local file="$1"
    local label="$2"

    # Patterns that should never appear in rendered YAML.
    # Excludes template references ({{ }}), secret key names, and env var refs.
    local patterns=(
        "BEGIN.*PRIVATE KEY"             # Private keys
        "AKIA[0-9A-Z]{16}"              # AWS access keys
        "ghp_[a-zA-Z0-9]{36}"           # GitHub personal access tokens
        "sk-[a-zA-Z0-9]{48}"            # OpenAI/Anthropic API keys
    )

    local found=false
    for pattern in "${patterns[@]}"; do
        if grep -qE "$pattern" "$file"; then
            fail "$label — found potential secret matching: $pattern"
            found=true
        fi
    done

    if [[ "$found" == "false" ]]; then
        pass "$label — no hardcoded secrets detected"
    fi
}

check_no_secrets "$DEFAULT_OUTPUT" "default"
check_no_secrets "$HA_OUTPUT" "gastown-ha"
check_no_secrets "$ALL_OUTPUT" "all-features"

# ===========================================================================
# Cleanup and summary
# ===========================================================================
rm -f "$DEFAULT_OUTPUT" "$HA_OUTPUT" "$ALL_OUTPUT"

echo ""
echo "==========================================="
echo "Results: $PASSED passed, $FAILED failed"
echo "==========================================="

if [[ $FAILED -gt 0 ]]; then
    echo "Failures:"
    for e in "${ERRORS[@]}"; do
        echo "  - $e"
    done
    exit 1
fi

exit 0
