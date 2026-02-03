#!/bin/bash

# VRSky File Consumer/Producer End-to-End Test Script
# This script tests the complete pipeline without requiring NATS

set -euo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BIN_DIR="${PROJECT_ROOT}/bin"
TEST_DIR="/tmp/vrsky-e2e-test-$$"
INPUT_DIR="${TEST_DIR}/input"
OUTPUT_DIR="${TEST_DIR}/output"
LOG_FILE="${TEST_DIR}/test.log"

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

# Conditional cleanup on exit based on test results and PRESERVE_TEST_DIR.
# Set PRESERVE_TEST_DIR to 1 to preserve the test directory even on success (useful for debugging).
cleanup() {
    # If any tests failed or PRESERVE_TEST_DIR is set to 1, preserve the test directory
    if [ "${TESTS_FAILED:-0}" -gt 0 ] || [ "${PRESERVE_TEST_DIR:-0}" -eq 1 ]; then
        echo -e "${BLUE}[Cleanup]${NC} Preserving test directory for debugging: ${TEST_DIR}"
        return
    fi
    echo -e "${BLUE}[Cleanup]${NC} Removing test directory: ${TEST_DIR}"
    rm -rf "${TEST_DIR}"
}

trap cleanup EXIT

# Helper functions
log() {
    local level=$1
    shift
    local message="$*"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [${level}] ${message}" | tee -a "${LOG_FILE}"
}

test_start() {
    TESTS_RUN=$((TESTS_RUN + 1))
    echo -e "${BLUE}[Test ${TESTS_RUN}]${NC} $1"
}

test_pass() {
    TESTS_PASSED=$((TESTS_PASSED + 1))
    echo -e "${GREEN}✓ PASS${NC}: $1"
}

test_fail() {
    TESTS_FAILED=$((TESTS_FAILED + 1))
    echo -e "${RED}✗ FAIL${NC}: $1"
}

assert_file_exists() {
    local file=$1
    if [ ! -f "${file}" ]; then
        test_fail "File does not exist: ${file}"
        return 1
    fi
    test_pass "File exists: ${file}"
    return 0
}

assert_file_content() {
    local file=$1
    local expected=$2
    if [ ! -f "${file}" ]; then
        test_fail "File does not exist: ${file}"
        return 1
    fi
    
    local actual
    actual=$(<"$file")
    if [ "${actual}" != "${expected}" ]; then
        test_fail "File content mismatch. Expected: '${expected}', Got: '${actual}'"
        return 1
    fi
    test_pass "File content matches: '${expected}'"
    return 0
}

assert_files_count() {
    local dir=$1
    local expected=$2
    local actual
    if ! actual=$(find "${dir}" -type f -print0 | grep -zc .); then
        actual=0
    fi
    if [ "${actual}" -ne "${expected}" ]; then
        test_fail "File count mismatch in ${dir}. Expected: ${expected}, Got: ${actual}"
        return 1
    fi
    test_pass "File count correct: ${actual} files in ${dir}"
    return 0
}

# Setup test environment
setup_test_env() {
    mkdir -p "${INPUT_DIR}" "${OUTPUT_DIR}" "${TEST_DIR}"
    echo "Test output log" > "${LOG_FILE}"
    log "INFO" "Setting up test environment"
}

# Helper to run Go tests from src directory
run_go_test() {
    local test_name=$1
    (cd "${PROJECT_ROOT}/src" && go test -v ./pkg/io -run "${test_name}" -timeout 10s)
}
test_simple_text_output() {
    test_start "Simple text output"
    
    export FILE_OUTPUT_DIR="${OUTPUT_DIR}/test1"
    mkdir -p "${FILE_OUTPUT_DIR}"
    
    # Run producer test via Go
    if run_go_test "TestFileProducer_WriteFile" > "${TEST_DIR}/test_file_producer_write_file.log" 2>&1; then
        test_pass "File Producer test passed"
    else
        test_fail "File Producer test failed"
        return 1
    fi
}

# Test 2: File Producer respects file permissions
test_file_permissions() {
    test_start "File Producer respects file permissions"
    
    if run_go_test "TestFileProducerPermissions" >/dev/null 2>&1; then
        test_pass "File permissions test passed"
    else
        test_fail "File permissions test failed"
        return 1
    fi
}

# Test 3: Envelope serialization works correctly
test_envelope_serialization() {
    test_start "Envelope serialization through pipeline"
    
    if run_go_test "TestEnvelopeSerializationThroughPipeline" >/dev/null 2>&1; then
        test_pass "Envelope serialization test passed"
    else
        test_fail "Envelope serialization test failed"
        return 1
    fi
}

# Test 4: Multiple files processed correctly
test_multiple_files() {
    test_start "Processing multiple files"
    
    if run_go_test "TestFileConsumerMultipleFiles" >/dev/null 2>&1; then
        test_pass "Multiple files test passed"
    else
        test_fail "Multiple files test failed"
        return 1
    fi
}

# Test 5: Metadata preservation
test_metadata_preservation() {
    test_start "Envelope metadata preservation"
    
    if run_go_test "TestFileConsumerMetadataPreservation" >/dev/null 2>&1; then
        test_pass "Metadata preservation test passed"
    else
        test_fail "Metadata preservation test failed"
        return 1
    fi
}

# Test 6: Complete pipeline
test_consumer_producer_pipeline() {
    test_start "Complete Consumer → Producer pipeline"
    
    if run_go_test "TestFileConsumerProducerPipeline" >/dev/null 2>&1; then
        test_pass "Consumer → Producer pipeline test passed"
    else
        test_fail "Consumer → Producer pipeline test failed"
        return 1
    fi
}

# Test 7: Pattern matching
test_pattern_matching() {
    test_start "File pattern matching"
    
    if run_go_test "TestFileConsumerPatternMatching" >/dev/null 2>&1; then
        test_pass "Pattern matching test passed"
    else
        test_fail "Pattern matching test failed"
        return 1
    fi
}

# Test 8: Graceful shutdown
test_graceful_shutdown() {
    test_start "Graceful shutdown and context cancellation"
    
    if run_go_test "TestFileConsumerProducerGracefulShutdown" >/dev/null 2>&1; then
        test_pass "Graceful shutdown test passed"
    else
        test_fail "Graceful shutdown test failed"
        return 1
    fi
}

# Run all tests
run_all_tests() {
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}VRSky File Consumer/Producer E2E Test Suite${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo ""
    
    log "INFO" "Starting E2E tests"
    
    # Run each test
    test_simple_text_output || true
    test_file_permissions || true
    test_envelope_serialization || true
    test_multiple_files || true
    test_metadata_preservation || true
    test_consumer_producer_pipeline || true
    test_pattern_matching || true
    test_graceful_shutdown || true
    
    echo ""
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}Test Results${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo "Total Tests Run: ${TESTS_RUN}"
    echo -e "Passed: ${GREEN}${TESTS_PASSED}${NC}"
    echo -e "Failed: ${RED}${TESTS_FAILED}${NC}"
    echo ""
    
    if [ ${TESTS_FAILED} -eq 0 ]; then
        echo -e "${GREEN}✓ All tests passed!${NC}"
        return 0
    else
        echo -e "${RED}✗ Some tests failed!${NC}"
        return 1
    fi
}

# Main
main() {
    setup_test_env
    run_all_tests
}

main "$@"
