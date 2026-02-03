#!/bin/bash

# VRSky File Consumer/Producer End-to-End Test Script
# This script tests the complete pipeline with optional Docker support
#
# Usage:
#   ./test/e2e_file_components.sh [OPTIONS]
#
# Options:
#   --local              Run tests using local binaries (default)
#   --docker             Run tests using Docker Compose
#   --large-file         Include large file tests (>10MB)
#   --preserve           Preserve test directory on success
#
# Environment Variables:
#   PRESERVE_TEST_DIR    Set to 1 to preserve test directory on success (default: 0)
#
# Examples:
#   # Run local tests normally
#   ./test/e2e_file_components.sh
#
#   # Run tests with Docker
#   ./test/e2e_file_components.sh --docker
#
#   # Run local tests including large files
#   ./test/e2e_file_components.sh --local --large-file
#
#   # Run Docker tests with large files and preserve directory
#   ./test/e2e_file_components.sh --docker --large-file --preserve

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
ARCHIVE_DIR="${TEST_DIR}/archive"
ERROR_DIR="${TEST_DIR}/error"
LOG_FILE="${TEST_DIR}/test.log"

# Parse command line arguments
MODE="local"  # local or docker
INCLUDE_LARGE_FILES=0
PRESERVE_TEST_DIR="${PRESERVE_TEST_DIR:-0}"

while [[ $# -gt 0 ]]; do
    case $1 in
        --docker)
            MODE="docker"
            shift
            ;;
        --local)
            MODE="local"
            shift
            ;;
        --large-file)
            INCLUDE_LARGE_FILES=1
            shift
            ;;
        --preserve)
            PRESERVE_TEST_DIR=1
            shift
            ;;
        *)
            echo "Unknown option: $1"
            echo "Usage: $0 [--local|--docker] [--large-file] [--preserve]"
            exit 1
            ;;
    esac
done

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

# Conditional cleanup on exit based on test results and PRESERVE_TEST_DIR
cleanup() {
    if [ "${MODE}" == "docker" ]; then
        echo -e "${BLUE}[Cleanup]${NC} Stopping Docker Compose services..."
        cd "${PROJECT_ROOT}" && docker-compose -f docker-compose-files.yml down -v 2>/dev/null || true
    fi
    
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
    if ! actual=$(find "${dir}" -type f 2>/dev/null | wc -l); then
        test_fail "Failed to count files in ${dir}"
        return 1
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
    if [ "${MODE}" == "local" ]; then
        mkdir -p "${INPUT_DIR}" "${OUTPUT_DIR}" "${ARCHIVE_DIR}" "${ERROR_DIR}" "${TEST_DIR}"
    else
        mkdir -p "${PROJECT_ROOT}/data/input" "${PROJECT_ROOT}/data/output" \
                 "${PROJECT_ROOT}/data/archive" "${PROJECT_ROOT}/data/error"
    fi
    echo "Test output log" > "${LOG_FILE}"
    log "INFO" "Setting up test environment (mode: ${MODE})"
}

# Helper to run Go tests from src directory
run_go_test() {
    local test_name=$1
    if ! (cd "${PROJECT_ROOT}/src" && go test -v ./pkg/io -run "${test_name}" -timeout 30s); then
        local status=$?
        log "ERROR" "Test '${test_name}' failed with exit code ${status}"
        return "${status}"
    fi
}

# Test 1: File Producer writes file
test_file_producer_write_file() {
    test_start "File Producer writes file"
    
    if [ "${MODE}" == "local" ]; then
        export FILE_OUTPUT_DIR="${OUTPUT_DIR}/test1"
        mkdir -p "${FILE_OUTPUT_DIR}"
        
        if run_go_test "TestFileProducer_WriteFile" > "${TEST_DIR}/test_file_producer_write_file.log" 2>&1; then
            test_pass "File Producer test passed"
        else
            test_fail "File Producer test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 2: File Producer respects file permissions
test_file_permissions() {
    test_start "File Producer respects file permissions"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileProducerPermissions" >/dev/null 2>&1; then
            test_pass "File permissions test passed"
        else
            test_fail "File permissions test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 3: Envelope serialization works correctly
test_envelope_serialization() {
    test_start "Envelope serialization through pipeline"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestEnvelopeSerializationThroughPipeline" >/dev/null 2>&1; then
            test_pass "Envelope serialization test passed"
        else
            test_fail "Envelope serialization test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 4: Multiple files processed correctly
test_multiple_files() {
    test_start "Processing multiple files"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileConsumerMultipleFiles" >/dev/null 2>&1; then
            test_pass "Multiple files test passed"
        else
            test_fail "Multiple files test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 5: Metadata preservation
test_metadata_preservation() {
    test_start "Envelope metadata preservation"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileConsumerMetadataPreservation" >/dev/null 2>&1; then
            test_pass "Metadata preservation test passed"
        else
            test_fail "Metadata preservation test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 6: Complete pipeline
test_consumer_producer_pipeline() {
    test_start "Complete Consumer → Producer pipeline"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileConsumerProducerPipeline" >/dev/null 2>&1; then
            test_pass "Consumer → Producer pipeline test passed"
        else
            test_fail "Consumer → Producer pipeline test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 7: Pattern matching
test_pattern_matching() {
    test_start "File pattern matching"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileConsumerPatternMatching" >/dev/null 2>&1; then
            test_pass "Pattern matching test passed"
        else
            test_fail "Pattern matching test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 8: Graceful shutdown
test_graceful_shutdown() {
    test_start "Graceful shutdown and context cancellation"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileConsumerProducerGracefulShutdown" >/dev/null 2>&1; then
            test_pass "Graceful shutdown test passed"
        else
            test_fail "Graceful shutdown test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 9: Archive directory functionality
test_archive_directory() {
    test_start "Archive directory management"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileConsumerArchiveDirectory" >/dev/null 2>&1; then
            test_pass "Archive directory test passed"
        else
            test_fail "Archive directory test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 10: Error directory functionality
test_error_directory() {
    test_start "Error directory management"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileConsumerErrorDirectory" >/dev/null 2>&1; then
            test_pass "Error directory test passed"
        else
            test_fail "Error directory test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 11: Reprocessing prevention
test_reprocessing_prevention() {
    test_start "Reprocessing prevention with file hashing"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileConsumerReprocessingPrevention" >/dev/null 2>&1; then
            test_pass "Reprocessing prevention test passed"
        else
            test_fail "Reprocessing prevention test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Test 12: Large file streaming (optional)
test_large_file_streaming() {
    if [ "${INCLUDE_LARGE_FILES}" -ne 1 ]; then
        return 0
    fi
    
    test_start "Large file streaming (>10MB)"
    
    if [ "${MODE}" == "local" ]; then
        if run_go_test "TestFileProducerLargeFile" >/dev/null 2>&1; then
            test_pass "Large file streaming test passed"
        else
            test_fail "Large file streaming test failed"
            return 1
        fi
    else
        test_pass "Skipped in Docker mode (integration test)"
    fi
}

# Docker-specific tests
test_docker_services() {
    if [ "${MODE}" != "docker" ]; then
        return 0
    fi
    
    test_start "Docker Compose services are running"
    
    cd "${PROJECT_ROOT}"
    
    # Start Docker Compose
    log "INFO" "Starting Docker Compose services..."
    if ! docker-compose -f docker-compose-files.yml up -d > "${TEST_DIR}/docker_startup.log" 2>&1; then
        test_fail "Failed to start Docker Compose services"
        return 1
    fi
    
    # Wait for services to be healthy
    log "INFO" "Waiting for services to become healthy..."
    sleep 10
    
    # Check NATS is running
    if ! docker-compose -f docker-compose-files.yml ps | grep -q "vrsky-nats-files"; then
        test_fail "NATS service is not running"
        return 1
    fi
    test_pass "NATS service is running"
    
    # Check file-consumer is running
    if ! docker-compose -f docker-compose-files.yml ps | grep -q "vrsky-file-consumer"; then
        test_fail "File Consumer service is not running"
        return 1
    fi
    test_pass "File Consumer service is running"
    
    # Check file-producer is running
    if ! docker-compose -f docker-compose-files.yml ps | grep -q "vrsky-file-producer"; then
        test_fail "File Producer service is not running"
        return 1
    fi
    test_pass "File Producer service is running"
    
    return 0
}

# Docker file processing test
test_docker_file_processing() {
    if [ "${MODE}" != "docker" ]; then
        return 0
    fi
    
    test_start "Docker: File processing pipeline"
    
    cd "${PROJECT_ROOT}"
    
    # Create test file in input directory
    local test_file="${PROJECT_ROOT}/data/input/test-docker.txt"
    local expected_content="Docker test content"
    
    mkdir -p "${PROJECT_ROOT}/data/input"
    echo "${expected_content}" > "${test_file}"
    
    log "INFO" "Created test file: ${test_file}"
    
    # Wait for consumer to process the file
    log "INFO" "Waiting for file to be processed (up to 30 seconds)..."
    local wait_time=0
    local max_wait=30
    local found=0
    
    while [ "${wait_time}" -lt "${max_wait}" ]; do
        if [ -d "${PROJECT_ROOT}/data/archive" ] && \
           [ "$(find "${PROJECT_ROOT}/data/archive" -type f 2>/dev/null | wc -l)" -gt 0 ]; then
            found=1
            break
        fi
        sleep 1
        wait_time=$((wait_time + 1))
    done
    
    if [ "${found}" -eq 1 ]; then
        test_pass "File was processed and archived"
    else
        test_fail "File was not processed within timeout period"
        return 1
    fi
    
    return 0
}

# Run all tests
run_all_tests() {
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}VRSky File Consumer/Producer E2E Test Suite${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo ""
    
    log "INFO" "Starting E2E tests (mode: ${MODE})"
    
    # Display configuration info
    echo -e "${YELLOW}[Config]${NC} Test mode: ${MODE}"
    if [ "${INCLUDE_LARGE_FILES}" -eq 1 ]; then
        echo -e "${YELLOW}[Config]${NC} Large file tests: ENABLED"
    else
        echo -e "${YELLOW}[Config]${NC} Large file tests: disabled (use --large-file to enable)"
    fi
    if [ "${PRESERVE_TEST_DIR:-0}" -eq 1 ]; then
        echo -e "${YELLOW}[Config]${NC} Test directory will be preserved"
    fi
    echo -e "${YELLOW}[Config]${NC} Test directory: ${TEST_DIR}"
    echo ""
    
    # Run tests based on mode
    if [ "${MODE}" == "docker" ]; then
        echo -e "${BLUE}Running Docker Integration Tests${NC}"
        test_docker_services || true
        test_docker_file_processing || true
    else
        echo -e "${BLUE}Running Local Unit Tests${NC}"
        test_file_producer_write_file || true
        test_file_permissions || true
        test_envelope_serialization || true
        test_multiple_files || true
        test_metadata_preservation || true
        test_consumer_producer_pipeline || true
        test_pattern_matching || true
        test_graceful_shutdown || true
        test_archive_directory || true
        test_error_directory || true
        test_reprocessing_prevention || true
        test_large_file_streaming || true
    fi
    
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
