# Anthropic API Key Rotation

This runbook covers rotating the Anthropic API key used by coopmux for Claude
access. With long-lived API keys replacing the OAuth token refresh flow, manual
rotation is required when keys expire or are compromised.

## Architecture

The API key flows through these components:

```
Anthropic Console (generate key)
    │
    ├─→ AWS Secrets Manager (recommended)
    │       └── key: anthropic-api-key
    │           └── property: api-key
    │       │
    │       └─→ ExternalSecret (refreshes every 15m)
    │               └─→ K8s Secret: <release>-bd-daemon-daemon-anthropic
    │                       └── api-key: sk-ant-...
    │
    ├─→ OR: Manual K8s Secret (anthropicApiKey.existingSecret)
    │       └─→ K8s Secret: <name>
    │               └── api-key: sk-ant-...
    │
    └─→ OR: Helm values (anthropicApiKey.key) — NOT recommended for production
            └─→ K8s Secret: <release>-bd-daemon-daemon-anthropic
                    └── api-key: sk-ant-...

K8s Secret
    └─→ env: ANTHROPIC_API_KEY
        ├── bd-daemon container
        ├── slack-bot sidecar
        └── standalone slack-bot deployment
```

### Helm Values Reference

```yaml
# Option 1: ExternalSecrets (recommended for production)
externalSecrets:
  enabled: true
  secretStoreName: "aws-secretsmanager"
  anthropicApiKey:
    enabled: true
    remoteRef: "anthropic-api-key"    # Key in AWS SM
    property: "api-key"               # JSON property within the secret

# Option 2: Existing K8s Secret
anthropicApiKey:
  existingSecret: "my-anthropic-secret"   # Pre-created secret with key "api-key"

# Option 3: Static value (dev/testing only)
anthropicApiKey:
  key: "sk-ant-..."
```

## Rotation Procedure

### Step 1: Generate a New API Key

1. Go to https://console.anthropic.com/settings/keys
2. Click "Create Key"
3. Name it descriptively (e.g., `gastown-production-2026-02`)
4. Copy the key (starts with `sk-ant-`)
5. **Do not revoke the old key yet** — running sessions need it during transition

### Step 2: Update the Secret Store

#### If using ExternalSecrets (recommended):

```bash
# Update AWS Secrets Manager
aws secretsmanager put-secret-value \
  --secret-id "anthropic-api-key" \
  --secret-string '{"api-key":"sk-ant-NEW_KEY_VALUE"}'

# Verify the update
aws secretsmanager get-secret-value \
  --secret-id "anthropic-api-key" \
  --query 'SecretString' --output text | jq .
```

Force ExternalSecret sync (or wait up to 15 minutes):

```bash
# Check current sync status
kubectl get externalsecret <release>-bd-daemon-daemon-anthropic -n <namespace>

# Force immediate sync
kubectl annotate externalsecret <release>-bd-daemon-daemon-anthropic \
  -n <namespace> \
  force-sync=$(date +%s) --overwrite

# Verify the secret was updated
kubectl get secret <release>-bd-daemon-daemon-anthropic -n <namespace> \
  -o jsonpath='{.metadata.resourceVersion}'
```

#### If using manual K8s Secret:

```bash
# Update the secret in-place
kubectl create secret generic <secret-name> \
  --from-literal=api-key="sk-ant-NEW_KEY_VALUE" \
  --namespace=<namespace> \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Step 3: Restart Pods to Pick Up New Key

Pods read environment variables at startup. A rolling restart is needed:

```bash
# Restart daemon deployment
kubectl rollout restart deployment <release>-bd-daemon-daemon -n <namespace>
kubectl rollout status deployment <release>-bd-daemon-daemon -n <namespace>

# Restart standalone slackbot (if deployed)
kubectl rollout restart deployment <release>-bd-daemon-slackbot -n <namespace> 2>/dev/null

# Wait for rollouts
kubectl rollout status deployment <release>-bd-daemon-daemon -n <namespace>
```

### Step 4: Verify New Key Is Active

```bash
# Check daemon pod logs for successful API calls
kubectl logs -n <namespace> \
  $(kubectl get pods -n <namespace> -l app.kubernetes.io/component=daemon -o name | head -1) \
  --tail=20

# Verify ANTHROPIC_API_KEY is set (check env without revealing value)
kubectl exec -n <namespace> deploy/<release>-bd-daemon-daemon -- \
  sh -c 'echo "ANTHROPIC_API_KEY length: ${#ANTHROPIC_API_KEY}"'

# Test via slackbot (if available): ask an agent to respond
# Or check coopmux logs for successful Claude API calls
```

### Step 5: Revoke the Old Key

After confirming all pods are using the new key (allow 10-15 minutes for
all sessions to restart):

1. Go to https://console.anthropic.com/settings/keys
2. Find the old key
3. Click "Revoke"

**Warning**: Revoking too early will break in-flight agent sessions that
haven't restarted yet.

## Grace Period

During rotation, both old and new keys are valid simultaneously:
- The new key is deployed to K8s secrets
- Pods are restarted to pick up the new key
- The old key remains valid until explicitly revoked

**Recommended grace period**: 15 minutes after all pods show Running status.
This accounts for:
- ExternalSecret refresh interval (up to 15m)
- Pod scheduling and startup time
- In-flight requests completing on old sessions

## Emergency Rotation (Compromised Key)

If a key is compromised, speed takes priority over grace:

```bash
# 1. Immediately revoke the compromised key in Anthropic Console

# 2. Generate and deploy new key (combine steps 1-3 above)
aws secretsmanager put-secret-value \
  --secret-id "anthropic-api-key" \
  --secret-string '{"api-key":"sk-ant-NEW_KEY_VALUE"}'

# 3. Force sync and restart
kubectl annotate externalsecret <release>-bd-daemon-daemon-anthropic \
  -n <namespace> force-sync=$(date +%s) --overwrite

kubectl rollout restart deployment <release>-bd-daemon-daemon -n <namespace>
kubectl rollout status deployment <release>-bd-daemon-daemon -n <namespace>
```

Agent sessions in progress will fail and need to be restarted by the controller.

## Troubleshooting

### ExternalSecret not syncing

```bash
# Check ExternalSecret status and events
kubectl describe externalsecret <release>-bd-daemon-daemon-anthropic -n <namespace>

# Check ClusterSecretStore health
kubectl get clustersecretstore aws-secretsmanager -o yaml

# Check external-secrets operator logs
kubectl logs -n external-secrets deploy/external-secrets --tail=20
```

### Pods not picking up new key

```bash
# Verify the K8s secret has the updated value
kubectl get secret <release>-bd-daemon-daemon-anthropic -n <namespace> \
  -o jsonpath='{.data.api-key}' | base64 -d | head -c 20
# Should show "sk-ant-..." prefix of the new key

# Check if pods were actually restarted (should be recent)
kubectl get pods -n <namespace> -l app.kubernetes.io/component=daemon \
  -o custom-columns='NAME:.metadata.name,AGE:.metadata.creationTimestamp'
```

### Claude API returning 401 after rotation

- Verify the new key is valid: test directly via curl
  ```bash
  curl -s https://api.anthropic.com/v1/messages \
    -H "x-api-key: sk-ant-NEW_KEY_VALUE" \
    -H "anthropic-version: 2023-06-01" \
    -H "content-type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}' \
    | jq .type
  # Should return "message", not "error"
  ```
- Check if the key was copied correctly (no trailing whitespace/newlines)
- Verify the secret property name matches: must be `api-key`

## Access Requirements

| Action | Requires |
|--------|----------|
| Generate Anthropic API key | Anthropic Console admin access |
| Update AWS Secrets Manager | AWS IAM with `secretsmanager:PutSecretValue` |
| Update K8s secrets | `kubectl` with secret write access |
| Restart pods | `kubectl` with deployment rollout permissions |

Note: Agent pods do NOT have AWS Secrets Manager access. This runbook must be
executed from a workstation with appropriate AWS and kubectl credentials.
