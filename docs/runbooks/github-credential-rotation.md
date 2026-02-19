# GitHub Credential Rotation for Gastown

This runbook covers rotating the GitHub Personal Access Token (PAT) used by
gastown namespaces (`gastown-next`, `gastown-rwx`) for git operations and
container image pulls from GHCR.

## Credential Landscape

The GitHub PAT is used in two places:

| Secret | Type | Source | Consumers |
|--------|------|--------|-----------|
| `git-credentials` | ExternalSecret (auto-synced) | AWS SM: `gastown/git-credentials` | Agent pods (`init-clone` container) for `git clone/fetch` |
| `ghcr-credentials` | Manual K8s secret | `kubectl create secret` | All pods via `imagePullSecrets` for GHCR image pulls |

### Current Architecture

```
GitHub PAT (fine-grained or classic)
    │
    ├─→ AWS Secrets Manager: gastown/git-credentials
    │       ├── username: <github-user>
    │       └── token: <pat-value>
    │       │
    │       └─→ ExternalSecret (refreshes every 15m)
    │               └─→ K8s Secret: git-credentials
    │                       ├── username
    │                       └── token
    │                       │
    │                       └─→ Agent pod env vars:
    │                           GIT_USERNAME, GIT_TOKEN
    │
    └─→ Manual K8s secret: ghcr-credentials
            └── .dockerconfigjson (ghcr.io auth)
                    │
                    └─→ imagePullSecrets on pods
```

### Required PAT Scopes

For a **classic** PAT:
- `repo` (full control of private repositories)
- `read:packages` (pull container images from GHCR)
- `write:packages` (only if agents push images)

For a **fine-grained** PAT (preferred):
- Repository access: `groblegark/beads`, `groblegark/gastown` (and any other cloned repos)
- Permissions: Contents (read), Packages (read)

## Rotation Procedure

### Step 1: Create a New GitHub PAT

1. Go to https://github.com/settings/tokens (or fine-grained: https://github.com/settings/personal-access-tokens/new)
2. Sign in as the `groblegark` user (or the org service account)
3. Create a new token with the scopes listed above
4. Set expiration (recommend 90 days for classic, or use fine-grained with auto-renewal)
5. Copy the new token value (starts with `ghp_` for classic or `github_pat_` for fine-grained)

### Step 2: Update AWS Secrets Manager

```bash
# Requires AWS credentials with secretsmanager:PutSecretValue permission.
# This is NOT available from agent pods — run from a workstation with AWS access.

aws secretsmanager put-secret-value \
  --secret-id "gastown/git-credentials" \
  --secret-string '{"username":"groblegark","token":"NEW_PAT_VALUE"}'
```

Verify the update:
```bash
aws secretsmanager get-secret-value \
  --secret-id "gastown/git-credentials" \
  --query 'SecretString' --output text | jq .
```

### Step 3: Verify ExternalSecrets Synced

The `git-credentials` ExternalSecret refreshes every 15 minutes. You can either
wait or force a sync:

```bash
# Check current sync status
kubectl get externalsecret git-credentials -n gastown-next

# Force immediate sync by annotating the ExternalSecret
kubectl annotate externalsecret git-credentials \
  -n gastown-next \
  force-sync=$(date +%s) --overwrite

# Verify the secret was updated (check resourceVersion changed)
kubectl get secret git-credentials -n gastown-next -o jsonpath='{.metadata.resourceVersion}'
```

Repeat for `gastown-rwx` if applicable:
```bash
kubectl get externalsecret git-credentials -n gastown-rwx
kubectl annotate externalsecret git-credentials \
  -n gastown-rwx \
  force-sync=$(date +%s) --overwrite
```

### Step 4: Update ghcr-credentials (Manual Secret)

The `ghcr-credentials` secret is NOT managed by ExternalSecrets. It must be
updated manually in each namespace:

```bash
# Delete and recreate for gastown-next
kubectl delete secret ghcr-credentials -n gastown-next
kubectl create secret docker-registry ghcr-credentials \
  --namespace=gastown-next \
  --docker-server=ghcr.io \
  --docker-username=groblegark \
  --docker-password=NEW_PAT_VALUE

# Repeat for gastown-rwx
kubectl delete secret ghcr-credentials -n gastown-rwx
kubectl create secret docker-registry ghcr-credentials \
  --namespace=gastown-rwx \
  --docker-server=ghcr.io \
  --docker-username=groblegark \
  --docker-password=NEW_PAT_VALUE
```

### Step 5: Rolling Restart of Pods

Agent pods read `git-credentials` at init time (in the `init-clone` container).
Existing pods won't pick up new credentials until restarted.

```bash
# Restart agent pods (controller will recreate them)
kubectl delete pods -n gastown-next -l app.kubernetes.io/component=agent

# Restart daemon and slackbot (they use imagePullSecrets)
kubectl rollout restart deployment gastown-next-bd-daemon-daemon -n gastown-next
kubectl rollout restart deployment gastown-next-bd-daemon-slackbot -n gastown-next

# Restart git mirrors (they clone repos)
kubectl rollout restart deployment -n gastown-next -l app.kubernetes.io/component=git-mirror

# Wait for rollouts to complete
kubectl rollout status deployment gastown-next-bd-daemon-daemon -n gastown-next
kubectl rollout status deployment gastown-next-bd-daemon-slackbot -n gastown-next
```

### Step 6: Verify

```bash
# Check that new pods started successfully
kubectl get pods -n gastown-next --field-selector=status.phase=Running

# Verify an agent pod can clone (check init container logs)
kubectl logs -n gastown-next \
  $(kubectl get pods -n gastown-next -l gastown.io/role=polecat -o name | head -1) \
  -c init-clone

# Verify GHCR image pulls work (check for ImagePullBackOff)
kubectl get events -n gastown-next --field-selector reason=Failed --sort-by='.lastTimestamp' | tail -5

# Test git clone manually from a pod
kubectl exec -it -n gastown-next deploy/gastown-next-bd-daemon-daemon -- \
  git ls-remote https://groblegark:NEW_PAT_VALUE@github.com/groblegark/beads.git HEAD
```

## Troubleshooting

### ExternalSecret not syncing

```bash
# Check ExternalSecret status
kubectl describe externalsecret git-credentials -n gastown-next

# Check ClusterSecretStore health
kubectl get clustersecretstore aws-secretsmanager -o yaml

# Check external-secrets operator logs
kubectl logs -n external-secrets deploy/external-secrets --tail=20
```

### Image pull failures after rotation

```bash
# Check events for ImagePullBackOff
kubectl get events -n gastown-next --field-selector reason=Failed

# Verify ghcr-credentials secret has correct format
kubectl get secret ghcr-credentials -n gastown-next -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d | jq .

# Test GHCR auth manually
echo NEW_PAT_VALUE | docker login ghcr.io -u groblegark --password-stdin
```

### Agent pods can't clone repos

```bash
# Check init container logs
kubectl logs <pod-name> -n gastown-next -c init-clone

# Verify git-credentials secret content
kubectl get secret git-credentials -n gastown-next -o jsonpath='{.data.token}' | base64 -d
```

## Future Improvements

- **Migrate ghcr-credentials to ExternalSecrets**: Store the GHCR docker config
  in AWS SM and create an ExternalSecret, eliminating the manual step. The beads
  Helm chart already supports this via `externalSecrets.registryCredentials`
  in `values.yaml` (currently disabled).

- **GitHub App tokens**: Replace PATs with GitHub App installation tokens for
  automatic rotation and better audit trails. No expiration management needed.

- **OIDC federation**: Use AWS IAM OIDC federation with GitHub to eliminate
  long-lived credentials entirely for CI/CD workflows.

## Access Requirements

| Action | Requires |
|--------|----------|
| Create GitHub PAT | GitHub account with admin access to `groblegark` org |
| Update AWS Secrets Manager | AWS IAM with `secretsmanager:PutSecretValue` on `gastown/*` |
| Update K8s secrets | `kubectl` with write access to `gastown-next`/`gastown-rwx` namespaces |
| Restart pods | `kubectl` with pod delete/rollout permissions |

Note: Agent pods (service account `gastown-agent`) do NOT have AWS Secrets
Manager access. This runbook must be executed from a workstation with
appropriate AWS and kubectl credentials.
