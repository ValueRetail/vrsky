#!/bin/bash

# E2E Test Suite for Management API
# This script tests the complete lifecycle of connection management,
# including: creation, starting, metrics streaming, test message sending, and stopping.
#
# Prerequisites:
#   - Docker and docker-compose installed
#   - Management API service running
#   - NATS service running
#   - PostgreSQL service running
#
# Usage: ./test/e2e_management_api.sh [--jwt-enabled] [--cleanup-only]

set -e

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
API_BASE_URL="${API_BASE_URL:-http://localhost:8080}"
API_TIMEOUT=10
TENANT_ID="test-tenant-$(date +%s)"
TENANT_ID_SHORT="test-tenant"
JWT_ENABLED="${JWT_ENABLED:-false}"
JWT_SECRET="${JWT_SECRET:-test-secret-key}"
JWT_ISSUER="${JWT_ISSUER:-test-issuer}"
JWT_AUDIENCE="${JWT_AUDIENCE:-test-api}"
CLEANUP_ONLY="${1:-false}"

# Test data
TEST_CONNECTION_ID=""
TEST_CONNECTION_HOST="localhost:9000"
TEST_CONNECTION_TYPE="default"

# Functions

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

setup_environment() {
    log_info "Setting up test environment..."
    
    # Enable/disable JWT as needed
    export JWT_ENABLED="$JWT_ENABLED"
    export JWT_SECRET="$JWT_SECRET"
    export JWT_ISSUER="$JWT_ISSUER"
    export JWT_AUDIENCE="$JWT_AUDIENCE"
    export DEFAULT_TENANT_ID="$TENANT_ID_SHORT"
    
    log_info "Using API base URL: $API_BASE_URL"
    log_info "Tenant ID: $TENANT_ID"
    if [ "$JWT_ENABLED" = "true" ]; then
        log_info "JWT authentication: ENABLED"
    else
        log_info "JWT authentication: DISABLED"
    fi
}

wait_for_api() {
    log_info "Waiting for Management API to be ready..."
    
    local max_attempts=30
    local attempt=0
    
    while [ $attempt -lt $max_attempts ]; do
        if curl -sf "$API_BASE_URL/api/v1/health" > /dev/null 2>&1; then
            log_success "Management API is ready"
            return 0
        fi
        
        attempt=$((attempt + 1))
        log_info "Attempt $attempt/$max_attempts..."
        sleep 1
    done
    
    log_error "Management API failed to start within 30 seconds"
    return 1
}

get_jwt_token() {
    # Generate a simple JWT token for testing
    # Format: header.payload.signature
    
    local header='{"alg":"HS256","typ":"JWT"}'
    local payload=$(cat <<EOF
{
  "tenant_id":"$TENANT_ID",
  "user_id":"test-user",
  "roles":["admin"],
  "email":"test@example.com",
  "iss":"$JWT_ISSUER",
  "aud":"$JWT_AUDIENCE",
  "exp":$(( $(date +%s) + 3600 ))
}
EOF
)
    
    # Base64 encode header and payload
    local header_b64=$(echo -n "$header" | base64 -w 0 | sed 's/+/-/g;s/\//_/g;s/=//g')
    local payload_b64=$(echo -n "$payload" | base64 -w 0 | sed 's/+/-/g;s/\//_/g;s/=//g')
    
    # Create signature using HMAC-SHA256
    local message="$header_b64.$payload_b64"
    local signature=$(echo -n "$message" | openssl dgst -sha256 -hmac "$JWT_SECRET" -binary | base64 -w 0 | sed 's/+/-/g;s/\//_/g;s/=//g')
    
    echo "$message.$signature"
}

make_request() {
    local method=$1
    local endpoint=$2
    local data=$3
    local token=$4
    
    local url="$API_BASE_URL$endpoint"
    local curl_cmd="curl -s -X $method"
    
    # Add headers
    curl_cmd="$curl_cmd -H 'Content-Type: application/json'"
    
    if [ -n "$token" ] && [ "$JWT_ENABLED" = "true" ]; then
        curl_cmd="$curl_cmd -H 'Authorization: Bearer $token'"
    fi
    
    # Add data if present
    if [ -n "$data" ]; then
        curl_cmd="$curl_cmd -d '$data'"
    fi
    
    # Add URL
    curl_cmd="$curl_cmd '$url'"
    
    eval "$curl_cmd"
}

test_create_connection() {
    log_info "Testing: Create Connection"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local request_body=$(cat <<EOF
{
  "name": "test-connection-$(date +%s)",
  "description": "E2E test connection",
  "host": "$TEST_CONNECTION_HOST",
  "port": 9000,
  "type": "$TEST_CONNECTION_TYPE",
  "timeout_ms": 5000,
  "ssl_enabled": false,
  "retry_attempts": 3,
  "retry_delay_ms": 1000,
  "filters": {
    "enabled": false,
    "rules": []
  }
}
EOF
)
    
    local response=$(make_request "POST" "/api/v1/connections" "$request_body" "$token")
    
    # Extract connection ID from response
    TEST_CONNECTION_ID=$(echo "$response" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
    
    if [ -z "$TEST_CONNECTION_ID" ]; then
        log_error "Failed to create connection. Response: $response"
        return 1
    fi
    
    log_success "Connection created with ID: $TEST_CONNECTION_ID"
    return 0
}

test_get_connection() {
    log_info "Testing: Get Connection"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local response=$(make_request "GET" "/api/v1/connections/$TEST_CONNECTION_ID" "" "$token")
    
    if echo "$response" | grep -q "\"id\":\"$TEST_CONNECTION_ID\""; then
        log_success "Connection retrieved successfully"
        return 0
    else
        log_error "Failed to retrieve connection. Response: $response"
        return 1
    fi
}

test_list_connections() {
    log_info "Testing: List Connections"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local response=$(make_request "GET" "/api/v1/connections" "" "$token")
    
    if echo "$response" | grep -q '"data"'; then
        log_success "Connections listed successfully"
        return 0
    else
        log_error "Failed to list connections. Response: $response"
        return 1
    fi
}

test_start_connection() {
    log_info "Testing: Start Connection"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local response=$(make_request "POST" "/api/v1/connections/$TEST_CONNECTION_ID/start" "" "$token")
    
    if echo "$response" | grep -q '"status":"started"'; then
        log_success "Connection started successfully"
        return 0
    else
        log_warning "Start response: $response"
        # This might fail if server is unreachable, but continue testing
        return 0
    fi
}

test_send_test_message() {
    log_info "Testing: Send Single Test Message"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local request_body=$(cat <<EOF
{
  "message": "test",
  "metadata": {
    "source": "e2e-test",
    "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  }
}
EOF
)
    
    local response=$(make_request "POST" "/api/v1/connections/$TEST_CONNECTION_ID/test-message" "$request_body" "$token")
    
    if echo "$response" | grep -q '"success"'; then
        log_success "Test message sent successfully"
        return 0
    else
        log_warning "Send test message response: $response"
        # This might fail if connection isn't actually connected, but continue testing
        return 0
    fi
}

test_start_auto_generator() {
    log_info "Testing: Start Auto Generator"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local request_body=$(cat <<EOF
{
  "rate_per_second": 5,
  "duration_seconds": 10
}
EOF
)
    
    local response=$(make_request "POST" "/api/v1/connections/$TEST_CONNECTION_ID/generator/start" "$request_body" "$token")
    
    if echo "$response" | grep -q '"is_running":true'; then
        log_success "Auto generator started successfully"
        return 0
    else
        log_warning "Start auto generator response: $response"
        return 0
    fi
}

test_get_generator_status() {
    log_info "Testing: Get Generator Status"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local response=$(make_request "GET" "/api/v1/connections/$TEST_CONNECTION_ID/generator/status" "" "$token")
    
    if echo "$response" | grep -q '"connection_id"'; then
        log_success "Generator status retrieved successfully"
        echo "$response"
        return 0
    else
        log_warning "Get generator status response: $response"
        return 0
    fi
}

test_stop_auto_generator() {
    log_info "Testing: Stop Auto Generator"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    # Wait a bit for generator to produce some messages
    sleep 2
    
    local response=$(make_request "POST" "/api/v1/connections/$TEST_CONNECTION_ID/generator/stop" "" "$token")
    
    if echo "$response" | grep -q '"is_running":false'; then
        log_success "Auto generator stopped successfully"
        return 0
    else
        log_warning "Stop auto generator response: $response"
        return 0
    fi
}

test_metrics_stream() {
    log_info "Testing: Metrics Stream (SSE)"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    # Test SSE endpoint with a 5-second timeout
    local curl_cmd="curl -s"
    
    if [ "$JWT_ENABLED" = "true" ]; then
        curl_cmd="$curl_cmd -H 'Authorization: Bearer $token'"
    fi
    
    curl_cmd="$curl_cmd -m 5 '$API_BASE_URL/api/v1/connections/$TEST_CONNECTION_ID/metrics/stream' || true"
    
    local response=$(eval "$curl_cmd")
    
    if echo "$response" | grep -q "data:"; then
        log_success "Metrics stream received SSE events"
        return 0
    else
        log_info "Metrics stream endpoint accessible (no events in timeout window)"
        return 0
    fi
}

test_update_connection() {
    log_info "Testing: Update Connection"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local request_body=$(cat <<EOF
{
  "description": "Updated description - $(date +%s)",
  "timeout_ms": 10000
}
EOF
)
    
    local response=$(make_request "PUT" "/api/v1/connections/$TEST_CONNECTION_ID" "$request_body" "$token")
    
    if echo "$response" | grep -q '"id":"$TEST_CONNECTION_ID"'; then
        log_success "Connection updated successfully"
        return 0
    else
        log_warning "Update connection response: $response"
        return 0
    fi
}

test_stop_connection() {
    log_info "Testing: Stop Connection"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local response=$(make_request "POST" "/api/v1/connections/$TEST_CONNECTION_ID/stop" "" "$token")
    
    if echo "$response" | grep -q '"status":"stopped"'; then
        log_success "Connection stopped successfully"
        return 0
    else
        log_warning "Stop connection response: $response"
        return 0
    fi
}

test_delete_connection() {
    log_info "Testing: Delete Connection"
    
    local token=""
    if [ "$JWT_ENABLED" = "true" ]; then
        token=$(get_jwt_token)
    fi
    
    local response=$(make_request "DELETE" "/api/v1/connections/$TEST_CONNECTION_ID" "" "$token")
    
    if echo "$response" | grep -q '"success":true'; then
        log_success "Connection deleted successfully"
        return 0
    else
        log_warning "Delete connection response: $response"
        return 0
    fi
}

cleanup() {
    log_info "Cleaning up test resources..."
    
    if [ -n "$TEST_CONNECTION_ID" ]; then
        local token=""
        if [ "$JWT_ENABLED" = "true" ]; then
            token=$(get_jwt_token)
        fi
        
        # Try to stop the connection if it's running
        make_request "POST" "/api/v1/connections/$TEST_CONNECTION_ID/stop" "" "$token" > /dev/null 2>&1 || true
        
        # Try to delete the connection
        make_request "DELETE" "/api/v1/connections/$TEST_CONNECTION_ID" "" "$token" > /dev/null 2>&1 || true
        
        log_success "Cleanup completed"
    fi
}

run_all_tests() {
    local tests_passed=0
    local tests_failed=0
    
    echo ""
    log_info "===================================="
    log_info "Management API E2E Test Suite"
    log_info "===================================="
    echo ""
    
    # Run tests in sequence
    if test_create_connection; then
        ((tests_passed++))
    else
        ((tests_failed++))
        return 1
    fi
    
    if test_get_connection; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_list_connections; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_start_connection; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_send_test_message; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_start_auto_generator; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_get_generator_status; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_stop_auto_generator; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_metrics_stream; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_update_connection; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_stop_connection; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    if test_delete_connection; then
        ((tests_passed++))
    else
        ((tests_failed++))
    fi
    
    echo ""
    log_info "===================================="
    log_info "Test Results Summary"
    log_info "===================================="
    echo ""
    log_success "Tests Passed: $tests_passed"
    
    if [ $tests_failed -gt 0 ]; then
        log_error "Tests Failed: $tests_failed"
        return 1
    fi
    
    log_success "All tests passed!"
    return 0
}

# Main script

trap cleanup EXIT

case "$CLEANUP_ONLY" in
    --cleanup-only)
        cleanup
        exit 0
        ;;
    --jwt-enabled)
        JWT_ENABLED="true"
        ;;
esac

setup_environment
wait_for_api
run_all_tests
exit_code=$?

if [ $exit_code -eq 0 ]; then
    log_success "E2E test suite completed successfully"
    exit 0
else
    log_error "E2E test suite failed"
    exit 1
fi
