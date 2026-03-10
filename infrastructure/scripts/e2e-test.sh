#!/bin/bash
set -e

# VRSky Phase 3 E2E Integration Test
# Tests Management API and WebSocket metrics streaming end-to-end

API_ENDPOINT="${API_ENDPOINT:-http://localhost:8080}"
NAMESPACE="${NAMESPACE:-vrsky-platform}"
TEST_TIMEOUT="${TEST_TIMEOUT:-30}"

# Color codes for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

# Helper functions
log_info() {
	echo -e "${GREEN}[INFO]${NC} $1"
}

log_error() {
	echo -e "${RED}[ERROR]${NC} $1"
}

log_test() {
	echo -e "${YELLOW}[TEST]${NC} $1"
}

test_result() {
	TESTS_RUN=$((TESTS_RUN + 1))
	if [ $1 -eq 0 ]; then
		log_info "✓ $2"
		TESTS_PASSED=$((TESTS_PASSED + 1))
	else
		log_error "✗ $2"
		TESTS_FAILED=$((TESTS_FAILED + 1))
	fi
}

# Verify prerequisites
verify_prerequisites() {
	log_test "Verifying prerequisites"

	# Check kubectl
	if ! command -v kubectl &>/dev/null; then
		log_error "kubectl not found in PATH"
		return 1
	fi

	# Check curl
	if ! command -v curl &>/dev/null; then
		log_error "curl not found in PATH"
		return 1
	fi

	# Check jq for JSON parsing
	if ! command -v jq &>/dev/null; then
		log_error "jq not found in PATH. Install with: apt-get install jq"
		return 1
	fi

	log_info "All prerequisites verified"
	return 0
}

# Wait for services to be ready
wait_for_services() {
	log_test "Waiting for services to reach Ready state"

	# Wait for Management API pod
	local max_attempts=30
	local attempt=0
	while [ $attempt -lt $max_attempts ]; do
		local ready=$(kubectl get pods -n $NAMESPACE -l app=vrsky-management-api -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "False")
		if [ "$ready" = "True" ]; then
			log_info "Management API pod is ready"
			return 0
		fi
		attempt=$((attempt + 1))
		echo -n "."
		sleep 1
	done

	log_error "Management API pod did not reach Ready state within ${max_attempts}s"
	return 1
}

# Test 1: Create connection via REST API
test_create_connection() {
	log_test "Test 1: Create connection via REST API"

	local response=$(curl -s -X POST "$API_ENDPOINT/api/v1/connections" \
		-H "Content-Type: application/json" \
		-H "X-Tenant-ID: test-tenant" \
		-d '{
			"name": "e2e-test-connection",
			"source": {
				"type": "http",
				"config": {
					"url": "https://webhook.example.com",
					"method": "POST"
				}
			},
			"destination": {
				"type": "http",
				"config": {
					"url": "https://api.example.com/webhook",
					"method": "POST"
				}
			}
		}')

	# Extract connection ID
	CONN_ID=$(echo "$response" | jq -r '.id // empty' 2>/dev/null)
	local status=$(echo "$response" | jq -r '.status // empty' 2>/dev/null)

	if [ -n "$CONN_ID" ] && [ "$CONN_ID" != "null" ]; then
		test_result 0 "Connection created with ID: $CONN_ID"
		return 0
	else
		log_error "Response: $response"
		test_result 1 "Failed to create connection"
		return 1
	fi
}

# Test 2: Start connection via REST API
test_start_connection() {
	log_test "Test 2: Start connection via REST API"

	if [ -z "$CONN_ID" ]; then
		test_result 1 "Skipping - no connection ID from test 1"
		return 1
	fi

	local response=$(curl -s -X POST "$API_ENDPOINT/api/v1/connections/$CONN_ID/start" \
		-H "X-Tenant-ID: test-tenant")

	local status=$(echo "$response" | jq -r '.status // empty' 2>/dev/null)

	if [ "$status" = "running" ]; then
		test_result 0 "Connection started successfully"
		return 0
	else
		log_error "Response: $response"
		test_result 1 "Failed to start connection"
		return 1
	fi
}

# Test 3: Send test message via REST API
test_send_test_message() {
	log_test "Test 3: Send test message via REST API"

	if [ -z "$CONN_ID" ]; then
		test_result 1 "Skipping - no connection ID"
		return 1
	fi

	local response=$(curl -s -X POST "$API_ENDPOINT/api/v1/connections/$CONN_ID/test-message" \
		-H "Content-Type: application/json" \
		-H "X-Tenant-ID: test-tenant" \
		-d '{
			"payload": {
				"message": "test data"
			}
		}')

	local success=$(echo "$response" | jq -r '.success // false' 2>/dev/null)

	if [ "$success" = "true" ]; then
		test_result 0 "Test message sent successfully"
		return 0
	else
		log_error "Response: $response"
		test_result 1 "Failed to send test message"
		return 1
	fi
}

# Test 4: WebSocket connection established
test_websocket_connection() {
	log_test "Test 4: WebSocket connection established"

	if [ -z "$CONN_ID" ]; then
		test_result 1 "Skipping - no connection ID"
		return 1
	fi

	# Check if websocat is available
	if ! command -v websocat &>/dev/null; then
		log_error "websocat not found. Install with: cargo install websocat or apt-get install websocat"
		test_result 1 "websocat not available for WebSocket testing"
		return 1
	fi

	# Attempt WebSocket connection with timeout
	# Derive WebSocket URL from API_ENDPOINT (http:// -> ws://, https:// -> wss://)
	local ws_base=$(echo "$API_ENDPOINT" | sed 's|^http://|ws://|; s|^https://|wss://|')
	local ws_url="$ws_base/api/v1/connections/$CONN_ID/metrics/stream"
	timeout 5 websocat "$ws_url" 2>/dev/null | head -1 | grep -q "metrics" 2>/dev/null
	local result=$?

	if [ $result -eq 0 ]; then
		test_result 0 "WebSocket connection established"
		return 0
	elif [ $result -eq 124 ]; then
		test_result 1 "WebSocket connection timed out"
		return 1
	else
		test_result 1 "Failed to establish WebSocket connection"
		return 1
	fi
}

# Test 5: Metrics broadcast to WebSocket client
test_metrics_broadcast() {
	log_test "Test 5: Metrics broadcast to WebSocket client"

	if [ -z "$CONN_ID" ]; then
		test_result 1 "Skipping - no connection ID"
		return 1
	fi

	# Check if websocat is available
	if ! command -v websocat &>/dev/null; then
		test_result 1 "websocat not available for WebSocket testing"
		return 1
	fi

	# Listen for metrics on WebSocket (5 second timeout)
	# Derive WebSocket URL from API_ENDPOINT (http:// -> ws://, https:// -> wss://)
	local ws_base=$(echo "$API_ENDPOINT" | sed 's|^http://|ws://|; s|^https://|wss://|')
	local metrics_received=$(timeout 5 websocat "$ws_base/api/v1/connections/$CONN_ID/metrics/stream" 2>/dev/null | grep -c "messagesProcessed" 2>/dev/null || echo 0)

	if [ "$metrics_received" -gt 0 ]; then
		test_result 0 "Metrics received via WebSocket"
		return 0
	else
		test_result 1 "No metrics received via WebSocket"
		return 1
	fi
}

# Test 6: Database persistence
test_database_persistence() {
	log_test "Test 6: Database persistence (connection in database)"

	if [ -z "$CONN_ID" ]; then
		test_result 1 "Skipping - no connection ID"
		return 1
	fi

	# Ensure kubectl is available before querying the database
	if ! command -v kubectl >/dev/null 2>&1; then
		log_error "kubectl command not found in PATH; cannot query PostgreSQL pod in cluster"
		test_result 1 "Cannot access database"
		return 1
	fi

	# Query database for the connection
	local db_pod=$(kubectl get pods -n vrsky-database -l app=postgresql -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

	if [ -z "$db_pod" ]; then
		log_error "PostgreSQL pod not found in namespace 'vrsky-database' with label 'app=postgresql'. Ensure the database is deployed and the namespace/labels match this script's expectations."
		test_result 1 "Cannot access database"
		return 1
	fi

	local conn_count=$(kubectl exec -n vrsky-database "$db_pod" -- \
		psql -U vrsky -d vrsky -t -v conn_id="$CONN_ID" -c "SELECT COUNT(*) FROM connections WHERE id = :'conn_id';" 2>/dev/null || echo "0")

	if [ "$conn_count" -gt 0 ]; then
		test_result 0 "Connection found in database"
		return 0
	else
		test_result 1 "Connection not found in database"
		return 1
	fi
}

# Check pod health
check_pod_health() {
	log_test "Checking pod health (no restart loops)"

	local api_pod=$(kubectl get pods -n $NAMESPACE -l app=vrsky-management-api -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

	if [ -z "$api_pod" ]; then
		test_result 1 "Management API pod not found"
		return 1
	fi

	local restart_count=$(kubectl get pods -n $NAMESPACE -l app=vrsky-management-api -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}' 2>/dev/null || echo "0")

	if [ "$restart_count" -eq 0 ]; then
		test_result 0 "No pod restarts detected"
		return 0
	else
		test_result 1 "Pod has restarted $restart_count times"
		return 1
	fi
}

# Check pod logs for errors
check_pod_logs() {
	log_test "Checking pod logs for errors"

	local api_pod=$(kubectl get pods -n $NAMESPACE -l app=vrsky-management-api -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

	if [ -z "$api_pod" ]; then
		test_result 1 "Management API pod not found"
		return 1
	fi

	local error_count=$(kubectl logs -n $NAMESPACE "$api_pod" 2>/dev/null | grep -c "ERROR" || echo "0")

	if [ "$error_count" -eq 0 ]; then
		test_result 0 "No ERROR logs detected"
		return 0
	else
		test_result 1 "Found $error_count ERROR log entries"
		kubectl logs -n $NAMESPACE "$api_pod" | grep "ERROR" | head -3
		return 1
	fi
}

# Main execution
main() {
	echo "================================================"
	echo "VRSky Phase 3 E2E Integration Test"
	echo "================================================"
	echo ""

	# Check if running in Kubernetes or localhost
	if kubectl cluster-info &>/dev/null; then
		log_info "Connected to Kubernetes cluster"

		if ! wait_for_services; then
			log_error "Services not ready"
			exit 1
		fi
	else
		log_info "Running local tests (no Kubernetes cluster detected)"
	fi

	# Verify prerequisites
	if ! verify_prerequisites; then
		exit 1
	fi

	echo ""

	# Run tests
	test_create_connection
	test_start_connection
	test_send_test_message

	if command -v websocat &>/dev/null; then
		test_websocket_connection
		test_metrics_broadcast
	else
		log_error "websocat not found - skipping WebSocket tests"
	fi

	test_database_persistence
	check_pod_health
	check_pod_logs

	# Print results
	echo ""
	echo "================================================"
	echo "Test Results"
	echo "================================================"
	echo -e "Total tests: ${YELLOW}$TESTS_RUN${NC}"
	echo -e "Passed: ${GREEN}$TESTS_PASSED${NC}"
	echo -e "Failed: ${RED}$TESTS_FAILED${NC}"
	echo ""

	if [ $TESTS_FAILED -eq 0 ]; then
		log_info "All tests passed! ✓"
		exit 0
	else
		log_error "Some tests failed"
		exit 1
	fi
}

# Run main
main "$@"
