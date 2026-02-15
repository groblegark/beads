#!/bin/bash
# Automated version of release-stack.sh for non-interactive execution
set -e

COOP_VERSION="0.11.25"
BEADS_VERSION="0.61.0"
GASTOWN_VERSION="0.7.6"

START_TIME=$(date +%s)
echo "🚀 Starting automated release at $(date)"
echo "Versions: Coop v${COOP_VERSION}, Beads v${BEADS_VERSION}, Gastown v${GASTOWN_VERSION}"

# Paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BEADS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COOP_DIR="$(cd "$BEADS_DIR/../coop" && pwd)"
GASTOWN_DIR="$(cd "$BEADS_DIR/../gastown" && pwd)"

# Function to check if tag exists
tag_exists() {
    git tag -l "$1" | grep -q "^$1$"
}

# =============================================================================
# STEP 1: Release Coop
# =============================================================================
echo "📦 Step 1: Releasing Coop v${COOP_VERSION}"
cd "$COOP_DIR"
git checkout main
git pull origin main

if tag_exists "v${COOP_VERSION}"; then
    echo "Tag v${COOP_VERSION} already exists in coop. Skipping..."
else
    # Update version
    sed -i '' "s/^version = \".*\"/version = \"${COOP_VERSION}\"/" Cargo.toml
    cargo update -p coopmux

    # Commit and tag
    git add -A
    git commit -m "chore: release v${COOP_VERSION}"
    git tag "v${COOP_VERSION}"
    git push origin main --tags
    echo "✅ Coop v${COOP_VERSION} pushed"
fi

# =============================================================================
# STEP 2: Release Beads
# =============================================================================
echo "📦 Step 2: Releasing Beads v${BEADS_VERSION}"
cd "$BEADS_DIR"
git checkout main
git pull origin main

if tag_exists "v${BEADS_VERSION}"; then
    echo "Tag v${BEADS_VERSION} already exists in beads. Skipping..."
else
    # Update version numbers across all components
    ./scripts/update-versions.sh "${BEADS_VERSION}"

    # Commit and tag
    git add -A
    git commit -m "chore: release v${BEADS_VERSION}"
    git tag "v${BEADS_VERSION}"
    git push origin main --tags
    echo "✅ Beads v${BEADS_VERSION} pushed"
fi

# =============================================================================
# STEP 3: Update and Release Gastown
# =============================================================================
echo "📦 Step 3: Releasing Gastown v${GASTOWN_VERSION}"
cd "$GASTOWN_DIR"
git checkout main
git pull origin main

if tag_exists "v${GASTOWN_VERSION}"; then
    echo "Tag v${GASTOWN_VERSION} already exists in gastown. Skipping..."
else
    # Update versions
    sed -i '' "s/Version = \".*\"/Version = \"${GASTOWN_VERSION}\"/" internal/cmd/version.go
    sed -i '' "s/^version: .*/version: ${GASTOWN_VERSION}/" helm/gastown/Chart.yaml
    sed -i '' "s/^appVersion: .*/appVersion: \"${GASTOWN_VERSION}\"/" helm/gastown/Chart.yaml

    # Update bd-daemon dependency — use the CHART version, not the app version.
    # The bd-daemon chart version lives in beads/helm/bd-daemon/Chart.yaml and
    # is bumped by update-versions.sh independently of the beads app version.
    BD_CHART_VERSION=$(grep '^version:' "$BEADS_DIR/helm/bd-daemon/Chart.yaml" | awk '{print $2}')
    if [ -z "$BD_CHART_VERSION" ]; then
        echo "❌ Could not determine bd-daemon chart version from $BEADS_DIR/helm/bd-daemon/Chart.yaml"
        exit 1
    fi
    echo "  bd-daemon chart version: ${BD_CHART_VERSION}"
    sed -i '' "/name: bd-daemon/,/version:/ s/version: .*/version: \"${BD_CHART_VERSION}\"/" helm/gastown/Chart.yaml

    # Update values.yaml - simplified for automation
    sed -i '' "s/tag: \".*\"/tag: \"${COOP_VERSION}\"/" helm/gastown/values.yaml || true

    # Update helm dependencies
    cd helm/gastown
    helm dependency update
    cd ../..

    # Commit and tag
    git add -A
    git commit -m "chore: release v${GASTOWN_VERSION}

- Update beads to v${BEADS_VERSION}
- Update coop to v${COOP_VERSION}
- Update bd-daemon chart dependency to ${BD_CHART_VERSION}"

    git tag "v${GASTOWN_VERSION}"
    git push origin main --tags
    echo "✅ Gastown v${GASTOWN_VERSION} pushed"
fi

# =============================================================================
# TIMING
# =============================================================================
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))
echo ""
echo "⏱️  Total release time: ${DURATION} seconds"
echo ""
echo "✅ Release complete! Triggered builds for:"
echo "  - ghcr.io/groblegark/coop:${COOP_VERSION}"
echo "  - ghcr.io/groblegark/beads:${BEADS_VERSION}"
echo "  - ghcr.io/groblegark/gastown:${GASTOWN_VERSION}"
echo "  - oci://ghcr.io/groblegark/charts/bd-daemon:${BD_CHART_VERSION:-unknown}"
echo "  - oci://ghcr.io/groblegark/charts/gastown:${GASTOWN_VERSION}"
echo ""
echo "Monitor builds at:"
echo "  - https://app.rwx.com/orgs/groblegark/projects/coop/runs"
echo "  - https://app.rwx.com/orgs/groblegark/projects/beads/runs"
echo "  - https://app.rwx.com/orgs/groblegark/projects/gastown/runs"