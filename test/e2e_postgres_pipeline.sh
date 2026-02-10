#!/bin/bash

# VRSky PostgreSQL CDC E2E Test Script
# Tests the complete PostgreSQL consumer → NATS → producer pipeline

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration - Load from environment variables with defaults for local development
NATS_URL="${NATS_URL:-nats://localhost:4222}"
POSTGRES_SOURCE_HOST="${POSTGRES_SOURCE_HOST:-localhost}"
POSTGRES_SOURCE_PORT="${POSTGRES_SOURCE_PORT:-5432}"
POSTGRES_SOURCE_USER="${POSTGRES_SOURCE_USER:-postgres}"
POSTGRES_SOURCE_PASSWORD="${POSTGRES_SOURCE_PASSWORD:-}"  # Intentionally empty - use env var
POSTGRES_SOURCE_DB="${POSTGRES_SOURCE_DB:-source_db}"

POSTGRES_TARGET_HOST="${POSTGRES_TARGET_HOST:-localhost}"
POSTGRES_TARGET_PORT="${POSTGRES_TARGET_PORT:-5433}"
POSTGRES_TARGET_USER="${POSTGRES_TARGET_USER:-postgres}"
POSTGRES_TARGET_PASSWORD="${POSTGRES_TARGET_PASSWORD:-}"  # Intentionally empty - use env var
POSTGRES_TARGET_DB="${POSTGRES_TARGET_DB:-target_db}"

# Validate that passwords are provided
if [ -z "$POSTGRES_SOURCE_PASSWORD" ] || [ -z "$POSTGRES_TARGET_PASSWORD" ]; then
    echo -e "${RED}Error: Database passwords must be provided via environment variables:${NC}"
    echo "  export POSTGRES_SOURCE_PASSWORD=<password>"
    echo "  export POSTGRES_TARGET_PASSWORD=<password>"
    exit 1
fi

TEST_TABLE="test_cdc_table"
NATS_SUBJECT="postgres.changes"

# Test counters
TESTS_PASSED=0
TESTS_FAILED=0

# Helper functions
print_header() {
    echo -e "${BLUE}=== $1 ===${NC}"
}

print_test() {
    echo -e "${YELLOW}Test: $1${NC}"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
    ((TESTS_PASSED++))
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
    ((TESTS_FAILED++))
}

wait_for_postgres() {
    local host=$1
    local port=$2
    local user=$3
    local password=$4
    local db=$5
    local timeout=30
    local elapsed=0

    print_test "Waiting for PostgreSQL at $host:$port"
    
    # Use nc (netcat) to check if port is open
    while [ $elapsed -lt $timeout ]; do
        if nc -z $host $port > /dev/null 2>&1; then
            print_success "PostgreSQL at $host:$port is ready"
            return 0
        fi
        sleep 1
        ((elapsed++))
    done
    
    print_error "PostgreSQL at $host:$port did not become ready after ${timeout}s"
    return 1
}

wait_for_nats() {
    local timeout=30
    local elapsed=0

    # Parse NATS_URL to extract host and port (format: nats://host:port)
    local nats_host=$(echo "$NATS_URL" | sed -E 's|nats://([^:]+).*|\1|')
    local nats_port=$(echo "$NATS_URL" | sed -E 's|.*:([0-9]+).*|\1|')
    
    # Fallback to defaults if parsing fails
    nats_host="${nats_host:-localhost}"
    nats_port="${nats_port:-4222}"

    print_test "Waiting for NATS at $NATS_URL ($nats_host:$nats_port)"
    
    while [ $elapsed -lt $timeout ]; do
        if nc -z "$nats_host" "$nats_port" > /dev/null 2>&1; then
            print_success "NATS is ready"
            return 0
        fi
        sleep 1
        ((elapsed++))
    done
    
    print_error "NATS did not become ready after ${timeout}s at $nats_host:$nats_port"
    return 1
}

# Test 1: Infrastructure Health Check
test_infrastructure() {
    print_header "Test 1: Infrastructure Health Check"
    
    wait_for_postgres "$POSTGRES_SOURCE_HOST" "$POSTGRES_SOURCE_PORT" "$POSTGRES_SOURCE_USER" "$POSTGRES_SOURCE_PASSWORD" "$POSTGRES_SOURCE_DB" || return 1
    wait_for_postgres "$POSTGRES_TARGET_HOST" "$POSTGRES_TARGET_PORT" "$POSTGRES_TARGET_USER" "$POSTGRES_TARGET_PASSWORD" "$POSTGRES_TARGET_DB" || return 1
    wait_for_nats || return 1
    
    print_success "All infrastructure services are healthy"
}

# Test 2: Create Test Table in Source Database
test_create_source_table() {
    print_header "Test 2: Create Test Table in Source Database"
    
    print_test "Creating test table in source database"
    
    PGPASSWORD="$POSTGRES_SOURCE_PASSWORD" psql \
        -h "$POSTGRES_SOURCE_HOST" \
        -p "$POSTGRES_SOURCE_PORT" \
        -U "$POSTGRES_SOURCE_USER" \
        -d "$POSTGRES_SOURCE_DB" \
        -c "DROP TABLE IF EXISTS \"$TEST_TABLE\" CASCADE;" \
        -c "CREATE TABLE \"$TEST_TABLE\" (
            id SERIAL PRIMARY KEY,
            name VARCHAR(100) NOT NULL,
            email VARCHAR(100),
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
        );" \
        > /dev/null 2>&1
    
    if [ $? -eq 0 ]; then
        print_success "Test table created in source database"
    else
        print_error "Failed to create test table in source database"
        return 1
    fi
}

# Test 3: Setup Replication
test_setup_replication() {
    print_header "Test 3: Setup Replication (Logical Replication Slot & Publication)"
    
    print_test "Creating replication slot"
    
    PGPASSWORD="$POSTGRES_SOURCE_PASSWORD" psql \
        -h "$POSTGRES_SOURCE_HOST" \
        -p "$POSTGRES_SOURCE_PORT" \
        -U "$POSTGRES_SOURCE_USER" \
        -d "$POSTGRES_SOURCE_DB" \
        -c "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = 'vrsky_slot' AND NOT active;" \
        > /dev/null 2>&1
    
    # Note: Replication slots are typically created by the consumer application
    print_success "Replication slot cleanup completed"
    
    print_test "Creating publication for all tables"
    
    PGPASSWORD="$POSTGRES_SOURCE_PASSWORD" psql \
        -h "$POSTGRES_SOURCE_HOST" \
        -p "$POSTGRES_SOURCE_PORT" \
        -U "$POSTGRES_SOURCE_USER" \
        -d "$POSTGRES_SOURCE_DB" \
        -c "DROP PUBLICATION IF EXISTS vrsky_publication;" \
        -c "CREATE PUBLICATION vrsky_publication FOR ALL TABLES;" \
        > /dev/null 2>&1
    
    if [ $? -eq 0 ]; then
        print_success "Publication created successfully"
    else
        print_error "Failed to create publication"
        return 1
    fi
}

# Test 4: Insert Initial Data
test_insert_data() {
    print_header "Test 4: Insert Initial Data"
    
    print_test "Inserting test records into source database"
    
    PGPASSWORD=$POSTGRES_SOURCE_PASSWORD psql \
        -h $POSTGRES_SOURCE_HOST \
        -p $POSTGRES_SOURCE_PORT \
        -U $POSTGRES_SOURCE_USER \
        -d $POSTGRES_SOURCE_DB \
        -c "INSERT INTO $TEST_TABLE (name, email) VALUES
            ('Alice Johnson', 'alice@example.com'),
            ('Bob Smith', 'bob@example.com'),
            ('Carol White', 'carol@example.com'),
            ('David Brown', 'david@example.com');" \
        > /dev/null 2>&1
    
    if [ $? -eq 0 ]; then
        print_success "Inserted 4 test records"
    else
        print_error "Failed to insert test records"
        return 1
    fi
}

# Test 5: Verify Source Data
test_verify_source_data() {
    print_header "Test 5: Verify Source Data"
    
    print_test "Verifying data in source database"
    
    COUNT=$(PGPASSWORD=$POSTGRES_SOURCE_PASSWORD psql \
        -h $POSTGRES_SOURCE_HOST \
        -p $POSTGRES_SOURCE_PORT \
        -U $POSTGRES_SOURCE_USER \
        -d $POSTGRES_SOURCE_DB \
        -t -c "SELECT COUNT(*) FROM $TEST_TABLE;")
    
    if [ "$COUNT" -eq 4 ]; then
        print_success "Source database contains 4 records"
    else
        print_error "Expected 4 records in source, found $COUNT"
        return 1
    fi
}

# Test 6: Create Target Table
test_create_target_table() {
    print_header "Test 6: Create Target Table in Target Database"
    
    print_test "Creating matching table in target database"
    
    PGPASSWORD=$POSTGRES_TARGET_PASSWORD psql \
        -h $POSTGRES_TARGET_HOST \
        -p $POSTGRES_TARGET_PORT \
        -U $POSTGRES_TARGET_USER \
        -d $POSTGRES_TARGET_DB \
        -c "DROP TABLE IF EXISTS $TEST_TABLE CASCADE;" \
        -c "CREATE TABLE $TEST_TABLE (
            id SERIAL PRIMARY KEY,
            name VARCHAR(100) NOT NULL,
            email VARCHAR(100),
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
        );" \
        > /dev/null 2>&1
    
    if [ $? -eq 0 ]; then
        print_success "Target table created successfully"
    else
        print_error "Failed to create target table"
        return 1
    fi
}

# Test 7: Consumer Data Capture
test_consumer_capture() {
    print_header "Test 7: Consumer Data Capture (CDC)"
    
    print_test "Consumer should capture INSERT operations"
    
    # Insert update data
    PGPASSWORD=$POSTGRES_SOURCE_PASSWORD psql \
        -h $POSTGRES_SOURCE_HOST \
        -p $POSTGRES_SOURCE_PORT \
        -U $POSTGRES_SOURCE_USER \
        -d $POSTGRES_SOURCE_DB \
        -c "INSERT INTO $TEST_TABLE (name, email) VALUES ('Eve Davis', 'eve@example.com');" \
        > /dev/null 2>&1
    
    if [ $? -eq 0 ]; then
        print_success "New record inserted successfully"
    else
        print_error "Failed to insert new record"
        return 1
    fi
}

# Test 8: Update Operations
test_update_operations() {
    print_header "Test 8: Update Operations"
    
    print_test "Testing UPDATE operation"
    
    PGPASSWORD=$POSTGRES_SOURCE_PASSWORD psql \
        -h $POSTGRES_SOURCE_HOST \
        -p $POSTGRES_SOURCE_PORT \
        -U $POSTGRES_SOURCE_USER \
        -d $POSTGRES_SOURCE_DB \
        -c "UPDATE $TEST_TABLE SET email = 'alice.new@example.com' WHERE name = 'Alice Johnson';" \
        > /dev/null 2>&1
    
    if [ $? -eq 0 ]; then
        print_success "Record updated successfully"
    else
        print_error "Failed to update record"
        return 1
    fi
}

# Test 9: Delete Operations
test_delete_operations() {
    print_header "Test 9: Delete Operations"
    
    print_test "Testing DELETE operation"
    
    PGPASSWORD=$POSTGRES_SOURCE_PASSWORD psql \
        -h $POSTGRES_SOURCE_HOST \
        -p $POSTGRES_SOURCE_PORT \
        -U $POSTGRES_SOURCE_USER \
        -d $POSTGRES_SOURCE_DB \
        -c "DELETE FROM $TEST_TABLE WHERE name = 'Bob Smith';" \
        > /dev/null 2>&1
    
    if [ $? -eq 0 ]; then
        print_success "Record deleted successfully"
    else
        print_error "Failed to delete record"
        return 1
    fi
}

# Test 10: Data Consistency Check
test_data_consistency() {
    print_header "Test 10: Data Consistency Check (Target Database)"
    
    print_test "Verifying data reached target database (may take a moment)"
    
    # Get source database count
    SOURCE_COUNT=$(PGPASSWORD=$POSTGRES_SOURCE_PASSWORD psql \
        -h $POSTGRES_SOURCE_HOST \
        -p $POSTGRES_SOURCE_PORT \
        -U $POSTGRES_SOURCE_USER \
        -d $POSTGRES_SOURCE_DB \
        -t -c "SELECT COUNT(*) FROM $TEST_TABLE;" 2>/dev/null | tr -d ' ')
    
    if [ -z "$SOURCE_COUNT" ] || ! [ "$SOURCE_COUNT" -gt 0 ]; then
        print_error "Could not determine source record count"
        return 1
    fi
    
    # Retry logic: wait for target to catch up with source
    local max_retries=30  # 30 seconds with 1-second intervals
    local retry=0
    local target_count=0
    
    while [ $retry -lt $max_retries ]; do
        TARGET_COUNT=$(PGPASSWORD=$POSTGRES_TARGET_PASSWORD psql \
            -h $POSTGRES_TARGET_HOST \
            -p $POSTGRES_TARGET_PORT \
            -U $POSTGRES_TARGET_USER \
            -d $POSTGRES_TARGET_DB \
            -t -c "SELECT COUNT(*) FROM $TEST_TABLE;" 2>/dev/null | tr -d ' ')
        
        if [ -z "$TARGET_COUNT" ]; then
            TARGET_COUNT=0
        fi
        
        if [ "$TARGET_COUNT" -eq "$SOURCE_COUNT" ]; then
            echo "Source count: $SOURCE_COUNT, Target count: $TARGET_COUNT (matched after ${retry}s)"
            print_success "Target database has received all data"
            return 0
        fi
        
        echo "  Waiting for replication... Source: $SOURCE_COUNT, Target: $TARGET_COUNT (attempt $((retry + 1))/$max_retries)"
        sleep 1
        ((retry++))
    done
    
    # Timeout - log what we got
    echo "Source count: $SOURCE_COUNT, Target count: $TARGET_COUNT"
    print_error "Target database did not catch up after ${max_retries}s (expected $SOURCE_COUNT records, got $TARGET_COUNT)"
    return 1
}

# Main execution
main() {
    print_header "VRSky PostgreSQL CDC E2E Test Suite"
    echo "Testing consumer → NATS → producer pipeline"
    echo ""
    
    # Run all tests
    test_infrastructure || exit 1
    echo ""
    
    test_create_source_table || exit 1
    echo ""
    
    test_setup_replication || exit 1
    echo ""
    
    test_insert_data || exit 1
    echo ""
    
    test_verify_source_data || exit 1
    echo ""
    
    test_create_target_table || exit 1
    echo ""
    
    test_consumer_capture || exit 1
    echo ""
    
    test_update_operations || exit 1
    echo ""
    
    test_delete_operations || exit 1
    echo ""
    
    test_data_consistency || exit 1
    echo ""
    
    # Print summary
    print_header "Test Summary"
    echo -e "Tests Passed: ${GREEN}${TESTS_PASSED}${NC}"
    echo -e "Tests Failed: ${RED}${TESTS_FAILED}${NC}"
    echo ""
    
    if [ $TESTS_FAILED -eq 0 ]; then
        print_success "All E2E tests passed!"
        return 0
    else
        print_error "Some tests failed"
        return 1
    fi
}

main
