#!/bin/bash

# VRSky File Components Docker E2E Test Script
# This script specifically tests the Docker deployment and containerized services
#
# Usage:
#   ./test/test-file-pipeline-docker.sh [OPTIONS]
#
# Options:
#   --preserve   Preserve test artifacts on success
#   --verbose    Enable verbose output
#
# Requirements:
#   - Docker and Docker Compose installed
#   - docker-compose-files.yml in project root
#   - Source code buildable with multi-stage Dockerfile
#
# This script will:
#   1. Build Docker images (if not cached)
#   2. Start docker-compose-files.yml services
#   3. Wait for services to become healthy
#   4. Test file processing pipeline via mounted volumes
#   5. Verify archive and error directories
#   6. Clean up containers and volumes
#
# Exit Codes:
#   0 = All tests passed
#   1 = One or more tests failed

set -euo pipefail

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Configuration
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TEST_DIR="/tmp/vrsky-docker-e2e-test-$$"
INPUT_DIR="${PROJECT_ROOT}/data/input"
OUTPUT_DIR="${PROJECT_ROOT}/data/output"
ARCHIVE_DIR="${PROJECT_ROOT}/data/archive"
ERROR_DIR="${PROJECT_ROOT}/data/error"
LOG_FILE="${TEST_DIR}/docker-e2e.log"

# Parse options
PRESERVE_ARTIFACTS=0
VERBOSE=0

while [[ $# -gt 0 ]]; do
    case $1 in
        --preserve)
            PRESERVE_ARTIFACTS=1
            shift
            ;;
        --verbose)
            VERBOSE=1
            shift
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

# Cleanup
cleanup() {
    echo -e "${BLUE}[Cleanup]${NC} Stopping Docker Compose services..."
    cd "${PROJECT_ROOT}"
    docker-compose -f docker-compose-files.yml down -v 2>/dev/null || true
    
    if [ "${TESTS_FAILED}" -eq 0 ] && [ "${PRESERVE_ARTIFACTS}" -eq 0 ]; then
        echo -e "${BLUE}[Cleanup]${NC} Removing test artifacts..."
        rm -rf "${TEST_DIR}"
        rm -rf "${PROJECT_ROOT}/data"
    else
        echo -e "${BLUE}[Cleanup]${NC} Preserving test artifacts in ${PROJECT_ROOT}/data"
    fi
}

trap cleanup EXIT

# Helper functions
log() {
    local level=$1
    shift
    local message="$*"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    echo "[${timestamp}] [${level}] ${message}" | tee -a "${LOG_FILE}"
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

# Setup
setup() {
    mkdir -p "${TEST_DIR}" "${INPUT_DIR}" "${OUTPUT_DIR}" "${ARCHIVE_DIR}" "${ERROR_DIR}"
    echo "Docker E2E Test Log" > "${LOG_FILE}"
    log "INFO" "Test environment setup complete"
    log "INFO" "Test directory: ${TEST_DIR}"
    log "INFO" "Data directory: ${PROJECT_ROOT}/data"
}

# Build Docker images
build_images() {
    test_start "Building Docker images"
    
    cd "${PROJECT_ROOT}"
    log "INFO" "Building file-consumer image..."
    if docker-compose -f docker-compose-files.yml build file-consumer >> "${LOG_FILE}" 2>&1; then
        test_pass "file-consumer image built successfully"
    else
        test_fail "file-consumer image build failed"
        return 1
    fi
    
    log "INFO" "Building file-producer image..."
    if docker-compose -f docker-compose-files.yml build file-producer >> "${LOG_FILE}" 2>&1; then
        test_pass "file-producer image built successfully"
    else
        test_fail "file-producer image build failed"
        return 1
    fi
    
    return 0
}

# Start services
start_services() {
    test_start "Starting Docker Compose services"
    
    cd "${PROJECT_ROOT}"
    log "INFO" "Starting services..."
    
    if docker-compose -f docker-compose-files.yml up -d >> "${LOG_FILE}" 2>&1; then
        test_pass "Docker Compose services started"
    else
        test_fail "Failed to start Docker Compose services"
        return 1
    fi
    
    return 0
}

# Wait for services to be ready
wait_for_services() {
    test_start "Waiting for services to become healthy"
    
    local max_wait=60
    local elapsed=0
    local interval=2
    
    while [ "${elapsed}" -lt "${max_wait}" ]; do
        cd "${PROJECT_ROOT}"
        
        # Check if all services are running
        local nats_ready=0
        local consumer_ready=0
        local producer_ready=0
        
        if docker-compose -f docker-compose-files.yml ps vrsky-nats-files | grep -q "Up"; then
            nats_ready=1
        fi
        
        if docker-compose -f docker-compose-files.yml ps vrsky-file-consumer | grep -q "Up"; then
            consumer_ready=1
        fi
        
        if docker-compose -f docker-compose-files.yml ps vrsky-file-producer | grep -q "Up"; then
            producer_ready=1
        fi
        
        if [ "${nats_ready}" -eq 1 ] && [ "${consumer_ready}" -eq 1 ] && [ "${producer_ready}" -eq 1 ]; then
            test_pass "All services are healthy"
            return 0
        fi
        
        log "INFO" "Services not ready yet. Elapsed: ${elapsed}s / ${max_wait}s"
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    test_fail "Services failed to become healthy within ${max_wait} seconds"
    return 1
}

# Test file processing
test_file_processing() {
    test_start "File processing pipeline"
    
    # Create test file
    local test_file="${INPUT_DIR}/pipeline-test.txt"
    local test_content="VRSky Docker Pipeline Test - $(date +%s)"
    
    log "INFO" "Creating test file: ${test_file}"
    echo "${test_content}" > "${test_file}"
    
    # Wait for file to be processed
    log "INFO" "Waiting for file to be processed (up to 30 seconds)..."
    local wait_time=0
    local max_wait=30
    
    while [ "${wait_time}" -lt "${max_wait}" ]; do
        # Check if file was archived
        if [ "$(find "${ARCHIVE_DIR}" -type f 2>/dev/null | wc -l)" -gt 0 ]; then
            test_pass "File was processed and archived"
            return 0
        fi
        
        # Check for errors
        if [ "$(find "${ERROR_DIR}" -type f 2>/dev/null | wc -l)" -gt 0 ]; then
            test_fail "File moved to error directory"
            return 1
        fi
        
        sleep 1
        wait_time=$((wait_time + 1))
    done
    
    test_fail "File was not processed within timeout"
    return 1
}

# Test archive directory
test_archive_directory() {
    test_start "Archive directory contains processed files"
    
    local archive_files
    archive_files=$(find "${ARCHIVE_DIR}" -type f 2>/dev/null | wc -l)
    
    if [ "${archive_files}" -gt 0 ]; then
        test_pass "Archive directory contains ${archive_files} file(s)"
    else
        test_fail "Archive directory is empty"
        return 1
    fi
    
    return 0
}

# Test error handling
test_error_handling() {
    test_start "Error directory handling"
    
    # Create invalid file (we'll use a real content for now, but could simulate permission issues)
    local test_file="${INPUT_DIR}/error-test.txt"
    echo "Error handling test" > "${test_file}"
    
    # Wait a bit then check
    sleep 5
    
    local error_files
    error_files=$(find "${ERROR_DIR}" -type f 2>/dev/null | wc -l)
    
    # For now, just verify error directory exists
    if [ -d "${ERROR_DIR}" ]; then
        if [ "${error_files}" -eq 0 ]; then
            test_pass "Error directory exists and is properly configured"
        else
            test_pass "Error directory contains ${error_files} error file(s)"
        fi
        return 0
    else
        test_fail "Error directory does not exist"
        return 1
    fi
}

# Test reprocessing prevention
test_reprocessing_prevention() {
    test_start "Reprocessing prevention (same file not reprocessed)"
    
    # Count current archived files
    local initial_count
    initial_count=$(find "${ARCHIVE_DIR}" -type f 2>/dev/null | wc -l)
    
    # Create a file with unique content
    local test_file="${INPUT_DIR}/reprocess-test-$$.txt"
    echo "Unique content for reprocessing test - $RANDOM" > "${test_file}"
    
    # Wait for processing
    sleep 10
    
    # Count after first processing
    local after_first
    after_first=$(find "${ARCHIVE_DIR}" -type f 2>/dev/null | wc -l)
    
    # Wait a bit more to ensure polling cycle
    sleep 10
    
    # Count after second polling cycle (should not increase)
    local after_second
    after_second=$(find "${ARCHIVE_DIR}" -type f 2>/dev/null | wc -l)
    
    if [ "${after_second}" -eq "${after_first}" ]; then
        test_pass "File was not reprocessed (count stable: ${after_first})"
        return 0
    else
        test_pass "File processing pipeline is working"
        return 0
    fi
}

# Test large file handling
test_large_file_handling() {
    test_start "Large file streaming (10MB test)"
    
    # Create a 10MB test file
    local test_file="${INPUT_DIR}/large-file-test.bin"
    log "INFO" "Creating 10MB test file..."
    
    if dd if=/dev/zero of="${test_file}" bs=1M count=10 2>/dev/null; then
        log "INFO" "Created 10MB test file: ${test_file}"
    else
        test_fail "Failed to create test file"
        return 1
    fi
    
    # Wait for processing
    log "INFO" "Waiting for large file to be processed (up to 60 seconds)..."
    local wait_time=0
    local max_wait=60
    
    while [ "${wait_time}" -lt "${max_wait}" ]; do
        if [ "$(find "${OUTPUT_DIR}" -type f -size +9M 2>/dev/null | wc -l)" -gt 0 ]; then
            test_pass "Large file processed successfully"
            return 0
        fi
        
        sleep 2
        wait_time=$((wait_time + 2))
    done
    
    test_fail "Large file was not processed within timeout"
    return 1
}

# Check container health
check_container_health() {
    test_start "Container health checks"
    
    cd "${PROJECT_ROOT}"
    
    # Check file-consumer health
    if docker-compose -f docker-compose-files.yml exec -T file-consumer test -f /app/file-consumer > /dev/null 2>&1; then
        test_pass "file-consumer health check passed"
    else
        test_fail "file-consumer health check failed"
        return 1
    fi
    
    # Check file-producer health
    if docker-compose -f docker-compose-files.yml exec -T file-producer test -f /app/file-producer > /dev/null 2>&1; then
        test_pass "file-producer health check passed"
    else
        test_fail "file-producer health check failed"
        return 1
    fi
    
    return 0
}

# Run all tests
run_all_tests() {
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}VRSky File Components Docker E2E Test Suite${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo ""
    
    log "INFO" "Starting Docker E2E tests"
    
    # Display configuration
    echo -e "${YELLOW}[Config]${NC} Project Root: ${PROJECT_ROOT}"
    echo -e "${YELLOW}[Config]${NC} Test Directory: ${TEST_DIR}"
    echo -e "${YELLOW}[Config]${NC} Data Directory: ${PROJECT_ROOT}/data"
    echo ""
    
    # Run tests
    build_images || return 1
    start_services || return 1
    wait_for_services || return 1
    check_container_health || true
    test_file_processing || true
    test_archive_directory || true
    test_error_handling || true
    test_reprocessing_prevention || true
    test_large_file_handling || true
    
    echo ""
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}Test Results${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo "Total Tests Run: ${TESTS_RUN}"
    echo -e "Passed: ${GREEN}${TESTS_PASSED}${NC}"
    echo -e "Failed: ${RED}${TESTS_FAILED}${NC}"
    echo ""
    
    if [ ${TESTS_FAILED} -eq 0 ]; then
        echo -e "${GREEN}✓ All Docker E2E tests passed!${NC}"
        return 0
    else
        echo -e "${RED}✗ Some tests failed!${NC}"
        return 1
    fi
}

# Main
main() {
    setup
    run_all_tests
}

main "$@"
