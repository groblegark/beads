#!/bin/bash
# Monitor RWX builds for stack releases

echo "📊 Monitoring RWX Builds for Stack v0.11.21/v0.60.0/v0.7.5"
echo "================================================="
echo ""

# Function to check image availability
check_image() {
    local image=$1
    local name=$2
    if docker manifest inspect "$image" >/dev/null 2>&1; then
        echo "✅ $name: Available"
        return 0
    else
        echo "⏳ $name: Building..."
        return 1
    fi
}

# Function to check helm chart
check_chart() {
    local chart=$1
    local name=$2
    if helm pull "$chart" --dry-run >/dev/null 2>&1; then
        echo "✅ $name: Published"
        return 0
    else
        echo "⏳ $name: Building..."
        return 1
    fi
}

while true; do
    clear
    echo "📊 Build Status Check - $(date '+%H:%M:%S')"
    echo "================================================="
    echo ""

    # Check Docker images
    echo "🐳 Docker Images:"
    check_image "ghcr.io/groblegark/coop:0.11.21" "Coop v0.11.21"
    COOP_READY=$?

    check_image "ghcr.io/groblegark/beads:0.60.0" "Beads v0.60.0"
    BEADS_READY=$?

    check_image "ghcr.io/groblegark/gastown:0.7.5" "Gastown v0.7.5"
    GASTOWN_READY=$?

    echo ""
    echo "📦 Helm Charts:"
    check_chart "oci://ghcr.io/groblegark/charts/bd-daemon --version 0.60.0" "bd-daemon v0.60.0"
    BD_CHART_READY=$?

    check_chart "oci://ghcr.io/groblegark/charts/gastown --version 0.7.5" "gastown v0.7.5"
    GASTOWN_CHART_READY=$?

    # Check if all ready
    if [ $COOP_READY -eq 0 ] && [ $BEADS_READY -eq 0 ] && [ $GASTOWN_READY -eq 0 ] && \
       [ $BD_CHART_READY -eq 0 ] && [ $GASTOWN_CHART_READY -eq 0 ]; then
        echo ""
        echo "🎉 All builds complete! Ready for deployment."
        echo ""
        echo "Deploy with:"
        echo "  helm upgrade gastown oci://ghcr.io/groblegark/charts/gastown \\"
        echo "    --version 0.7.5 \\"
        echo "    --namespace gastown"
        exit 0
    fi

    echo ""
    echo "Refreshing in 30 seconds... (Ctrl+C to exit)"
    sleep 30
done