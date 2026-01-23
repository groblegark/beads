#!/bin/bash
# Dolt Performance Benchmark Script
# Usage: ./scripts/dolt-benchmark.sh [options]
#
# Options:
#   -i, --iterations N    Number of iterations (default: 5)
#   -j, --json           Output results in JSON format (for CI)
#   -c, --compare FILE   Compare with previous benchmark results
#   -g, --go             Run Go benchmarks instead of bd doctor
#   -q, --quick          Run quick benchmarks (fewer iterations)
#   -h, --help           Show this help message
#
# Runs systematic performance benchmarks for Dolt backend.
# Requires Dolt backend to be configured (for bd doctor mode).

set -e

# Default values
ITERATIONS=5
JSON_OUTPUT=false
COMPARE_FILE=""
GO_BENCHMARKS=false
QUICK_MODE=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -i|--iterations)
            ITERATIONS="$2"
            shift 2
            ;;
        -j|--json)
            JSON_OUTPUT=true
            shift
            ;;
        -c|--compare)
            COMPARE_FILE="$2"
            shift 2
            ;;
        -g|--go)
            GO_BENCHMARKS=true
            shift
            ;;
        -q|--quick)
            QUICK_MODE=true
            ITERATIONS=2
            shift
            ;;
        -h|--help)
            head -17 "$0" | tail -14
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

RESULTS_DIR="docs/reports/benchmark-results"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
RESULTS_FILE="${RESULTS_DIR}/benchmark-${TIMESTAMP}.md"
JSON_FILE="${RESULTS_DIR}/benchmark-${TIMESTAMP}.json"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Create results directory
mkdir -p "${RESULTS_DIR}"

# Initialize JSON output
init_json() {
    cat > "${JSON_FILE}" << EOF
{
  "timestamp": "$(date -Iseconds)",
  "host": "$(hostname)",
  "os": "$(uname -s) $(uname -r)",
  "cpu": "$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 | xargs || echo "Unknown")",
  "iterations": ${ITERATIONS},
  "embedded": {},
  "server": {}
}
EOF
}

# Update JSON with embedded results
update_json_embedded() {
    local conn=$1 ready=$2 list=$3 show=$4 complex=$5
    # Use jq if available, otherwise use sed
    if command -v jq &> /dev/null; then
        jq ".embedded = {\"connection_ms\": $conn, \"ready_ms\": $ready, \"list_ms\": $list, \"show_ms\": $show, \"complex_ms\": $complex}" "$JSON_FILE" > "${JSON_FILE}.tmp" && mv "${JSON_FILE}.tmp" "$JSON_FILE"
    fi
}

# Update JSON with server results
update_json_server() {
    local conn=$1 ready=$2 list=$3 show=$4 complex=$5
    if command -v jq &> /dev/null; then
        jq ".server = {\"connection_ms\": $conn, \"ready_ms\": $ready, \"list_ms\": $list, \"show_ms\": $show, \"complex_ms\": $complex}" "$JSON_FILE" > "${JSON_FILE}.tmp" && mv "${JSON_FILE}.tmp" "$JSON_FILE"
    fi
}

# Run Go benchmarks
run_go_benchmarks() {
    echo "=== Go Dolt Performance Benchmarks ==="
    echo "Timestamp: ${TIMESTAMP}"
    echo ""

    local benchtime="1s"
    if $QUICK_MODE; then
        benchtime="100ms"
    fi

    if $JSON_OUTPUT; then
        # Run benchmarks and convert to JSON
        go test -bench=. -benchmem -benchtime="$benchtime" -run='^$' ./internal/storage/dolt/ -timeout=30m 2>&1 | tee "${RESULTS_DIR}/go-bench-${TIMESTAMP}.txt"

        # Parse and convert to JSON (basic conversion)
        echo "{" > "$JSON_FILE"
        echo "  \"timestamp\": \"$(date -Iseconds)\"," >> "$JSON_FILE"
        echo "  \"type\": \"go_benchmarks\"," >> "$JSON_FILE"
        echo "  \"benchmarks\": [" >> "$JSON_FILE"
        grep "^Benchmark" "${RESULTS_DIR}/go-bench-${TIMESTAMP}.txt" | while read -r line; do
            name=$(echo "$line" | awk '{print $1}')
            ops=$(echo "$line" | awk '{print $2}')
            nsop=$(echo "$line" | awk '{print $3}')
            echo "    {\"name\": \"$name\", \"ops\": $ops, \"ns_per_op\": $nsop}," >> "$JSON_FILE"
        done
        # Remove trailing comma and close
        sed -i '$ s/,$//' "$JSON_FILE"
        echo "  ]" >> "$JSON_FILE"
        echo "}" >> "$JSON_FILE"

        echo ""
        echo -e "${GREEN}JSON results saved to: ${JSON_FILE}${NC}"
    else
        go test -bench=. -benchmem -benchtime="$benchtime" -run='^$' ./internal/storage/dolt/ -timeout=30m
    fi

    echo ""
    echo -e "${GREEN}Go benchmarks complete!${NC}"
}

# Compare with previous results
compare_results() {
    if [ ! -f "$COMPARE_FILE" ]; then
        echo -e "${RED}Error: Comparison file not found: ${COMPARE_FILE}${NC}"
        exit 1
    fi

    echo "=== Benchmark Comparison ==="
    echo "Current: ${JSON_FILE}"
    echo "Previous: ${COMPARE_FILE}"
    echo ""

    if ! command -v jq &> /dev/null; then
        echo -e "${YELLOW}Warning: jq not installed. Cannot parse JSON for comparison.${NC}"
        echo "Install jq for detailed comparison output."
        return
    fi

    # Extract and compare metrics
    echo "| Metric | Previous | Current | Change |"
    echo "|--------|----------|---------|--------|"

    for metric in connection_ms ready_ms list_ms show_ms complex_ms; do
        prev=$(jq -r ".embedded.${metric} // 0" "$COMPARE_FILE")
        curr=$(jq -r ".embedded.${metric} // 0" "$JSON_FILE")
        if [ "$prev" != "0" ] && [ "$curr" != "0" ]; then
            change=$(echo "scale=1; (($curr - $prev) / $prev) * 100" | bc 2>/dev/null || echo "N/A")
            if [[ "$change" != "N/A" ]]; then
                if (( $(echo "$change > 20" | bc -l) )); then
                    change="${RED}+${change}%${NC} ⚠️"
                elif (( $(echo "$change < -10" | bc -l) )); then
                    change="${GREEN}${change}%${NC} 🚀"
                else
                    change="${change}%"
                fi
            fi
            echo "| ${metric} | ${prev}ms | ${curr}ms | ${change} |"
        fi
    done

    echo ""
    echo "Legend: 🚀 = Improved (>10% faster), ⚠️ = Regression (>20% slower)"
}

# Run bd doctor benchmarks
run_doctor_benchmarks() {
    echo "=== Dolt Performance Benchmark ==="
    echo "Iterations: ${ITERATIONS}"
    echo "Timestamp: ${TIMESTAMP}"
    echo ""

    # Check if Dolt backend is configured
    if ! bd doctor 2>&1 | grep -q "Backend: dolt"; then
        echo -e "${RED}Error: Not a Dolt backend. Configure with 'bd init --backend dolt'${NC}"
        echo -e "${YELLOW}Alternatively, use -g flag to run Go benchmarks directly${NC}"
        exit 1
    fi

    if $JSON_OUTPUT; then
        init_json
    fi

    # Start results file
    cat > "${RESULTS_FILE}" << EOF
# Dolt Performance Benchmark Results

**Date**: $(date -Iseconds)
**Host**: $(hostname)
**OS**: $(uname -s) $(uname -r)
**CPU**: $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 | xargs || echo "Unknown")
**RAM**: $(free -h 2>/dev/null | awk '/^Mem:/{print $2}' || echo "Unknown")

## Baseline Measurements (Embedded Mode)

| Iteration | Connection | Ready-work | List-open | Show-issue | Complex |
|-----------|------------|------------|-----------|------------|---------|
EOF

    echo -e "${GREEN}Running embedded mode benchmarks...${NC}"

    # Arrays to store results for averaging
    declare -a conn_times ready_times list_times show_times complex_times

    for i in $(seq 1 $ITERATIONS); do
        echo "  Iteration $i/$ITERATIONS..."

        # Run diagnostics and capture output
        output=$(bd doctor --perf-dolt 2>&1)

        # Parse metrics (adjust patterns based on actual output)
        conn=$(echo "$output" | grep -oP 'Connection/Bootstrap:\s+\K\d+' || echo "0")
        ready=$(echo "$output" | grep -oP 'bd ready.*:\s+\K\d+' || echo "0")
        list=$(echo "$output" | grep -oP 'bd list.*:\s+\K\d+' || echo "0")
        show=$(echo "$output" | grep -oP 'bd show.*:\s+\K\d+' || echo "0")
        complex=$(echo "$output" | grep -oP 'Complex.*:\s+\K\d+' || echo "0")

        # Store for averaging
        conn_times+=($conn)
        ready_times+=($ready)
        list_times+=($list)
        show_times+=($show)
        complex_times+=($complex)

        # Write to results file
        echo "| $i | ${conn}ms | ${ready}ms | ${list}ms | ${show}ms | ${complex}ms |" >> "${RESULTS_FILE}"

        # Brief pause between iterations
        sleep 1
    done

    # Calculate averages
    calc_avg() {
        local arr=("$@")
        local sum=0
        for val in "${arr[@]}"; do
            sum=$((sum + val))
        done
        echo $((sum / ${#arr[@]}))
    }

    avg_conn=$(calc_avg "${conn_times[@]}")
    avg_ready=$(calc_avg "${ready_times[@]}")
    avg_list=$(calc_avg "${list_times[@]}")
    avg_show=$(calc_avg "${show_times[@]}")
    avg_complex=$(calc_avg "${complex_times[@]}")

    cat >> "${RESULTS_FILE}" << EOF
| **Avg** | **${avg_conn}ms** | **${avg_ready}ms** | **${avg_list}ms** | **${avg_show}ms** | **${avg_complex}ms** |

EOF

    if $JSON_OUTPUT; then
        update_json_embedded "$avg_conn" "$avg_ready" "$avg_list" "$avg_show" "$avg_complex"
    fi

    # Check if server mode is available
    if nc -z localhost 3306 2>/dev/null; then
        echo -e "${GREEN}Running server mode benchmarks...${NC}"

        cat >> "${RESULTS_FILE}" << EOF
## Server Mode Measurements

| Iteration | Connection | Ready-work | List-open | Show-issue | Complex |
|-----------|------------|------------|-----------|------------|---------|
EOF

        # Reset arrays
        conn_times=()
        ready_times=()
        list_times=()
        show_times=()
        complex_times=()

        # Set server mode env var
        export BEADS_DOLT_SERVER_MODE=1

        for i in $(seq 1 $ITERATIONS); do
            echo "  Iteration $i/$ITERATIONS..."

            output=$(bd doctor --perf-dolt 2>&1)

            conn=$(echo "$output" | grep -oP 'Connection/Bootstrap:\s+\K\d+' || echo "0")
            ready=$(echo "$output" | grep -oP 'bd ready.*:\s+\K\d+' || echo "0")
            list=$(echo "$output" | grep -oP 'bd list.*:\s+\K\d+' || echo "0")
            show=$(echo "$output" | grep -oP 'bd show.*:\s+\K\d+' || echo "0")
            complex=$(echo "$output" | grep -oP 'Complex.*:\s+\K\d+' || echo "0")

            conn_times+=($conn)
            ready_times+=($ready)
            list_times+=($list)
            show_times+=($show)
            complex_times+=($complex)

            echo "| $i | ${conn}ms | ${ready}ms | ${list}ms | ${show}ms | ${complex}ms |" >> "${RESULTS_FILE}"

            sleep 1
        done

        unset BEADS_DOLT_SERVER_MODE

        avg_conn_server=$(calc_avg "${conn_times[@]}")
        avg_ready_server=$(calc_avg "${ready_times[@]}")
        avg_list_server=$(calc_avg "${list_times[@]}")
        avg_show_server=$(calc_avg "${show_times[@]}")
        avg_complex_server=$(calc_avg "${complex_times[@]}")

        if $JSON_OUTPUT; then
            update_json_server "$avg_conn_server" "$avg_ready_server" "$avg_list_server" "$avg_show_server" "$avg_complex_server"
        fi

        cat >> "${RESULTS_FILE}" << EOF
| **Avg** | **${avg_conn_server}ms** | **${avg_ready_server}ms** | **${avg_list_server}ms** | **${avg_show_server}ms** | **${avg_complex_server}ms** |

## Comparison Summary

| Metric | Embedded | Server | Speedup |
|--------|----------|--------|---------|
| Connection | ${avg_conn}ms | ${avg_conn_server}ms | $(echo "scale=1; $avg_conn / $avg_conn_server" | bc 2>/dev/null || echo "N/A")x |
| Ready-work | ${avg_ready}ms | ${avg_ready_server}ms | $(echo "scale=1; $avg_ready / $avg_ready_server" | bc 2>/dev/null || echo "N/A")x |
| List-open | ${avg_list}ms | ${avg_list_server}ms | $(echo "scale=1; $avg_list / $avg_list_server" | bc 2>/dev/null || echo "N/A")x |
| Show-issue | ${avg_show}ms | ${avg_show_server}ms | $(echo "scale=1; $avg_show / $avg_show_server" | bc 2>/dev/null || echo "N/A")x |
| Complex | ${avg_complex}ms | ${avg_complex_server}ms | $(echo "scale=1; $avg_complex / $avg_complex_server" | bc 2>/dev/null || echo "N/A")x |

EOF

    else
        echo -e "${YELLOW}Server not running - skipping server mode benchmarks${NC}"
        echo -e "${YELLOW}To test server mode: dolt sql-server --data-dir .beads/dolt &${NC}"

        cat >> "${RESULTS_FILE}" << EOF
## Server Mode

Server not running during benchmark. To test server mode:
\`\`\`bash
dolt sql-server --data-dir .beads/dolt &
./scripts/dolt-benchmark.sh
\`\`\`

EOF
    fi

    # Add recommendations
    cat >> "${RESULTS_FILE}" << EOF
## Recommendations

Based on benchmark results:

EOF

    if [ "$avg_conn" -gt 500 ]; then
        echo "- **High bootstrap time in embedded mode** (${avg_conn}ms > 500ms): Consider using server mode" >> "${RESULTS_FILE}"
    fi

    if [ "$avg_ready" -gt 200 ]; then
        echo "- **Slow ready-work query** (${avg_ready}ms > 200ms): Review index on status column" >> "${RESULTS_FILE}"
    fi

    if [ "$avg_complex" -gt 500 ]; then
        echo "- **Slow complex queries** (${avg_complex}ms > 500ms): Review query patterns and indexes" >> "${RESULTS_FILE}"
    fi

    echo "" >> "${RESULTS_FILE}"
    echo "---" >> "${RESULTS_FILE}"
    echo "*Generated by dolt-benchmark.sh*" >> "${RESULTS_FILE}"

    echo ""
    echo -e "${GREEN}Benchmark complete!${NC}"
    echo "Results saved to: ${RESULTS_FILE}"
    if $JSON_OUTPUT; then
        echo "JSON saved to: ${JSON_FILE}"
    fi
}

# Main execution
if $GO_BENCHMARKS; then
    run_go_benchmarks
else
    run_doctor_benchmarks
fi

# Run comparison if requested
if [ -n "$COMPARE_FILE" ]; then
    compare_results
fi
