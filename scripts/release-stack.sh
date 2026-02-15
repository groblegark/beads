#!/bin/bash
# release-stack.sh - Automated release process for the gastown stack
# Usage: ./release-stack.sh <coop-version> <beads-version> <gastown-version>
# Example: ./release-stack.sh 0.2.0 0.60.0 0.7.5

set -e  # Exit on error

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Get versions from arguments or prompt
COOP_VERSION=${1:-}
BEADS_VERSION=${2:-}
GASTOWN_VERSION=${3:-}

if [ -z "$COOP_VERSION" ] || [ -z "$BEADS_VERSION" ] || [ -z "$GASTOWN_VERSION" ]; then
    echo -e "${YELLOW}Usage: $0 <coop-version> <beads-version> <gastown-version>${NC}"
    echo -e "${YELLOW}Example: $0 0.2.0 0.60.0 0.7.5${NC}"
    exit 1
fi

# Workspace paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BEADS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COOP_DIR="$(cd "$BEADS_DIR/../coop" && pwd)"
GASTOWN_DIR="$(cd "$BEADS_DIR/../gastown" && pwd)"

echo -e "${GREEN}=== Gastown Stack Release Process ===${NC}"
echo "Releasing:"
echo "  - Coop: v${COOP_VERSION}"
echo "  - Beads: v${BEADS_VERSION}"
echo "  - Gastown: v${GASTOWN_VERSION}"
echo ""
read -p "Continue? (y/N) " -n 1 -r
echo ""
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 1
fi

# Function to check if tag exists
tag_exists() {
    git tag -l "$1" | grep -q "^$1$"
}

# Function to wait for RWX build
wait_for_rwx_build() {
    local repo=$1
    local version=$2
    echo -e "${YELLOW}⏳ Waiting for RWX build of ${repo} v${version}...${NC}"
    echo "Check: https://app.rwx.com/orgs/groblegark/projects/${repo}/runs"
    echo "Press Enter when build is complete..."
    read -r
}

# =============================================================================
# STEP 1: Release Coop
# =============================================================================
echo -e "${GREEN}📦 Step 1: Releasing Coop v${COOP_VERSION}${NC}"
cd "$COOP_DIR"

# Check if we're on main and up to date
git checkout main
git pull origin main

# Check if tag already exists
if tag_exists "v${COOP_VERSION}"; then
    echo -e "${YELLOW}Tag v${COOP_VERSION} already exists in coop. Skipping...${NC}"
else
    # Update version in Cargo.toml
    sed -i '' "s/^version = \".*\"/version = \"${COOP_VERSION}\"/" Cargo.toml

    # Update Cargo.lock
    cargo update -p coopmux

    # Commit and tag
    git add -A
    git commit -m "chore: release v${COOP_VERSION}"
    git tag "v${COOP_VERSION}"

    # Push
    echo -e "${YELLOW}Pushing coop v${COOP_VERSION}...${NC}"
    git push origin main --tags

    wait_for_rwx_build "coop" "${COOP_VERSION}"
fi

# =============================================================================
# STEP 2: Release Beads
# =============================================================================
echo -e "${GREEN}📦 Step 2: Releasing Beads v${BEADS_VERSION}${NC}"
cd "$BEADS_DIR"

# Check if we're on main and up to date
git checkout main
git pull origin main

# Check if tag already exists
if tag_exists "v${BEADS_VERSION}"; then
    echo -e "${YELLOW}Tag v${BEADS_VERSION} already exists in beads. Skipping...${NC}"
else
    # Use the bump-version script
    if [ -f "./scripts/bump-version.sh" ]; then
        ./scripts/bump-version.sh "${BEADS_VERSION}"
    else
        echo -e "${RED}Error: bump-version.sh not found${NC}"
        exit 1
    fi

    # Commit and tag
    git add -A
    git commit -m "chore: release v${BEADS_VERSION}"
    git tag "v${BEADS_VERSION}"

    # Push
    echo -e "${YELLOW}Pushing beads v${BEADS_VERSION}...${NC}"
    git push origin main --tags

    wait_for_rwx_build "beads" "${BEADS_VERSION}"
fi

# =============================================================================
# STEP 3: Update and Release Gastown
# =============================================================================
echo -e "${GREEN}📦 Step 3: Releasing Gastown v${GASTOWN_VERSION}${NC}"
cd "$GASTOWN_DIR"

# Check if we're on main and up to date
git checkout main
git pull origin main

# Check if tag already exists
if tag_exists "v${GASTOWN_VERSION}"; then
    echo -e "${YELLOW}Tag v${GASTOWN_VERSION} already exists in gastown. Skipping...${NC}"
else
    # Update gastown version.go
    sed -i '' "s/Version = \".*\"/Version = \"${GASTOWN_VERSION}\"/" internal/cmd/version.go

    # Update Chart.yaml - version and appVersion
    sed -i '' "s/^version: .*/version: ${GASTOWN_VERSION}/" helm/gastown/Chart.yaml
    sed -i '' "s/^appVersion: .*/appVersion: \"${GASTOWN_VERSION}\"/" helm/gastown/Chart.yaml

    # Update bd-daemon dependency version
    sed -i '' "/name: bd-daemon/,/version:/ s/version: .*/version: \"${BEADS_VERSION}\"/" helm/gastown/Chart.yaml

    # Update values.yaml with new image tags
    cat > /tmp/gastown-values-patch.yaml << EOF
# Default image tags for gastown stack components
coopmux:
  image:
    repository: ghcr.io/groblegark/coop
    tag: "${COOP_VERSION}"
    pullPolicy: IfNotPresent

beads:
  image:
    repository: ghcr.io/groblegark/beads
    tag: "${BEADS_VERSION}"
    pullPolicy: IfNotPresent
EOF

    # Apply the patch (this is a simplified version - you may need to adjust)
    echo -e "${YELLOW}Note: Please manually update helm/gastown/values.yaml with:${NC}"
    echo "  - coopmux.image.tag: ${COOP_VERSION}"
    echo "  - beads.image.tag: ${BEADS_VERSION}"
    echo "Press Enter when done..."
    read -r

    # Update dependencies
    cd helm/gastown
    helm dependency update
    cd ../..

    # Commit and tag
    git add -A
    git commit -m "chore: release v${GASTOWN_VERSION}

- Update beads to v${BEADS_VERSION}
- Update coop to v${COOP_VERSION}
- Update bd-daemon chart dependency to ${BEADS_VERSION}"

    git tag "v${GASTOWN_VERSION}"

    # Push
    echo -e "${YELLOW}Pushing gastown v${GASTOWN_VERSION}...${NC}"
    git push origin main --tags

    wait_for_rwx_build "gastown" "${GASTOWN_VERSION}"
fi

# =============================================================================
# STEP 4: Verification
# =============================================================================
echo -e "${GREEN}✅ Release Complete!${NC}"
echo ""
echo "Versions released:"
echo "  - Coop: v${COOP_VERSION}"
echo "  - Beads: v${BEADS_VERSION}"
echo "  - Gastown: v${GASTOWN_VERSION}"
echo ""
echo "Next steps:"
echo "1. Wait for all RWX builds to complete"
echo "2. Verify images are available:"
echo "   - ghcr.io/groblegark/coop:${COOP_VERSION}"
echo "   - ghcr.io/groblegark/beads:${BEADS_VERSION}"
echo "   - ghcr.io/groblegark/gastown:${GASTOWN_VERSION}"
echo "   - oci://ghcr.io/groblegark/charts/bd-daemon:${BEADS_VERSION}"
echo "   - oci://ghcr.io/groblegark/charts/gastown:${GASTOWN_VERSION}"
echo ""
echo "3. Deploy without overrides:"
echo "   helm upgrade gastown oci://ghcr.io/groblegark/charts/gastown \\"
echo "     --version ${GASTOWN_VERSION} \\"
echo "     --namespace gastown"
echo ""
echo -e "${GREEN}🎉 No more version overrides needed!${NC}"