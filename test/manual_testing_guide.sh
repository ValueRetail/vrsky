#!/bin/bash

# VRSky File Consumer/Producer Manual Testing Guide
# This script demonstrates how to manually test the components

set -euo pipefail

# Color codes
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INPUT_DIR="/tmp/vrsky-manual-test/input"
OUTPUT_DIR="/tmp/vrsky-manual-test/output"

echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}VRSky File Consumer/Producer Manual Testing Guide${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
echo ""

# Create test directories
echo -e "${YELLOW}1. Setting up test directories${NC}"
mkdir -p "${INPUT_DIR}" "${OUTPUT_DIR}"
echo "   Input directory:  ${INPUT_DIR}"
echo "   Output directory: ${OUTPUT_DIR}"
echo ""

# Build binaries
echo -e "${YELLOW}2. Building binaries${NC}"
cd "${PROJECT_ROOT}/src" || { echo "Failed to change to src directory"; exit 1; }
echo "   Building file-consumer..."
go build -o "${PROJECT_ROOT}/bin/file-consumer" ./cmd/file-consumer
echo "   Building file-producer..."
go build -o "${PROJECT_ROOT}/bin/file-producer" ./cmd/file-producer
echo -e "${GREEN}✓ Binaries built successfully${NC}"
echo ""

# Test scenario 1: Basic text file
echo -e "${YELLOW}3. Test Scenario 1: Basic Text File${NC}"
echo "   Creating test file..."
echo "Hello from VRSky File Consumer/Producer!" > "${INPUT_DIR}/message.txt"

echo "   Environment variables to set:"
echo "      FILE_INPUT_DIR=${INPUT_DIR}"
echo "      FILE_INPUT_PATTERN=*.txt"
echo "      FILE_INPUT_POLL_INTERVAL=5s"
echo "      FILE_OUTPUT_DIR=${OUTPUT_DIR}"
echo "      FILE_OUTPUT_FILENAME_FORMAT={{.ID}}.{{.Extension}}"
echo ""

# Test scenario 2: JSON file
echo -e "${YELLOW}4. Test Scenario 2: JSON File${NC}"
echo "   Creating JSON test file..."
cat > "${INPUT_DIR}/config.json" << 'EOF'
{
  "name": "VRSky Test",
  "version": "1.0.0",
  "enabled": true,
  "settings": {
    "timeout": 30,
    "retries": 3
  }
}
EOF
echo -e "${GREEN}✓ JSON file created${NC}"
echo ""

# Test scenario 3: CSV file
echo -e "${YELLOW}5. Test Scenario 3: CSV File${NC}"
echo "   Creating CSV test file..."
cat > "${INPUT_DIR}/data.csv" << 'EOF'
id,name,email,status
1,Alice,alice@example.com,active
2,Bob,bob@example.com,inactive
3,Charlie,charlie@example.com,active
EOF
echo -e "${GREEN}✓ CSV file created${NC}"
echo ""

# Manual testing instructions
echo -e "${YELLOW}6. Manual Testing Instructions${NC}"
echo ""
echo "   Step 1: Set up environment variables (in terminal 1)"
echo "      export FILE_INPUT_DIR=${INPUT_DIR}"
echo "      export FILE_INPUT_PATTERN=*"
echo "      export FILE_INPUT_POLL_INTERVAL=5s"
echo ""
echo "   Step 2: Start File Consumer (in terminal 1)"
echo "      ${PROJECT_ROOT}/bin/file-consumer"
echo "      (This will monitor ${INPUT_DIR} for files)"
echo ""
echo "   Step 3: In another terminal (terminal 2), set up producer"
echo "      export FILE_OUTPUT_DIR=${OUTPUT_DIR}"
echo "      export FILE_OUTPUT_FILENAME_FORMAT={{.ID}}.{{.Extension}}"
echo ""
echo "   Step 4: Add test files to input directory"
echo "      cp /path/to/your/test/files/*.* ${INPUT_DIR}/"
echo ""
echo "   Step 5: Check output directory for processed files"
echo "      ls -la ${OUTPUT_DIR}/"
echo ""
echo "   Step 6: Verify file contents"
echo "      cat ${OUTPUT_DIR}/*"
echo ""

# Show current test files
echo -e "${YELLOW}7. Available Test Files${NC}"
ls -lh "${INPUT_DIR}/" 2>/dev/null || echo "   (No files in input directory yet)"
echo ""

# Show directory structure
echo -e "${YELLOW}8. Directory Structure${NC}"
echo "   Project root:"
echo "      ${PROJECT_ROOT}"
echo "   Binaries:"
echo "      - ${PROJECT_ROOT}/bin/file-consumer"
echo "      - ${PROJECT_ROOT}/bin/file-producer"
echo "   Source code:"
echo "      - ${PROJECT_ROOT}/src/pkg/io/file_input.go"
echo "      - ${PROJECT_ROOT}/src/pkg/io/file_output.go"
echo "   Tests:"
echo "      - ${PROJECT_ROOT}/src/pkg/io/file_input_test.go"
echo "      - ${PROJECT_ROOT}/src/pkg/io/file_output_test.go"
echo "      - ${PROJECT_ROOT}/src/pkg/io/file_integration_test.go"
echo ""

# Configuration reference
echo -e "${YELLOW}9. Configuration Reference${NC}"
echo ""
echo "   FILE_INPUT_* Variables (FileConsumer):"
echo "      FILE_INPUT_DIR              Directory to monitor for files"
echo "                                   Default: /tmp/file-input"
echo "      FILE_INPUT_PATTERN          Glob pattern for file matching"
echo "                                   Default: *"
echo "      FILE_INPUT_POLL_INTERVAL    How often to check for new files"
echo "                                   Default: 5s"
echo "                                   Example: 100ms, 1s, 5s, 30s"
echo ""
echo "   FILE_OUTPUT_* Variables (FileProducer):"
echo "      FILE_OUTPUT_DIR             Directory to write output files"
echo "                                   Default: /tmp/file-output"
echo "      FILE_OUTPUT_FILENAME_FORMAT Template for output filenames"
echo "                                   Default: {{.ID}}.{{.Extension}}"
echo "                                   Available: {{.ID}}, {{.Extension}}"
echo "      FILE_OUTPUT_PERMISSIONS     File permissions (octal)"
echo "                                   Default: 0644"
echo "                                   Examples: 0600, 0644, 0755"
echo ""

# Run Go tests
echo -e "${YELLOW}10. Run Automated Tests${NC}"
echo ""
echo "   Run all file I/O tests:"
echo "      cd ${PROJECT_ROOT}/src"
echo "      go test -v ./pkg/io -timeout 30s"
echo ""
echo "   Run only File Consumer tests:"
echo "      go test -v ./pkg/io -run FileConsumer -timeout 15s"
echo ""
echo "   Run only File Producer tests:"
echo "      go test -v ./pkg/io -run FileProducer -timeout 15s"
echo ""
echo "   Run integration tests:"
echo "      go test -v ./pkg/io -run Pipeline -timeout 15s"
echo ""

echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}Manual Testing Guide Complete${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
