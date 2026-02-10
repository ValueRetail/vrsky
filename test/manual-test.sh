#!/bin/bash
# VRSky Manual Testing Master Script
# Run individual component tests or full end-to-end pipeline

set -e

export PATH="/home/ludvik/go/bin:$PATH"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_header() {
    echo ""
    echo -e "${BLUE}=== $1 ===${NC}"
    echo ""
}

print_menu() {
    echo -e "${YELLOW}VRSky Manual Testing Menu${NC}"
    echo ""
    echo "1. Test File Producer (NATS → Disk)"
    echo "2. Test PostgreSQL Consumer (Source DB → NATS)"
    echo "3. Test PostgreSQL Producer (NATS → Target DB)"
    echo "4. Full End-to-End Test (Source DB → NATS → Target DB)"
    echo "5. Run All Tests"
    echo "6. View Logs"
    echo "7. Check Service Status"
    echo "8. Kill All Services"
    echo "9. Exit"
    echo ""
}

check_prerequisites() {
    print_header "Checking Prerequisites"
    
    local all_ok=true
    
    # Check PostgreSQL source
    if nc -zv localhost 5432 2>&1 | grep -q "succeeded"; then
        echo -e "${GREEN}✓${NC} PostgreSQL source (localhost:5432)"
    else
        echo -e "${YELLOW}⚠${NC} PostgreSQL source (localhost:5432) - NOT RESPONDING"
        all_ok=false
    fi
    
    # Check PostgreSQL target
    if nc -zv localhost 5433 2>&1 | grep -q "succeeded"; then
        echo -e "${GREEN}✓${NC} PostgreSQL target (localhost:5433)"
    else
        echo -e "${YELLOW}⚠${NC} PostgreSQL target (localhost:5433) - NOT RESPONDING"
        all_ok=false
    fi
    
    # Check NATS
    if nc -zv localhost 4222 2>&1 | grep -q "succeeded"; then
        echo -e "${GREEN}✓${NC} NATS (localhost:4222)"
    else
        echo -e "${YELLOW}⚠${NC} NATS (localhost:4222) - NOT RESPONDING"
        all_ok=false
    fi
    
    # Check Go
    if command -v /home/ludvik/go/bin/go &> /dev/null; then
        GO_VERSION=$(/home/ludvik/go/bin/go version | awk '{print $3}')
        echo -e "${GREEN}✓${NC} Go ($GO_VERSION)"
    else
        echo -e "${YELLOW}⚠${NC} Go - NOT FOUND"
        all_ok=false
    fi
    
    echo ""
    if [ "$all_ok" = true ]; then
        echo -e "${GREEN}All prerequisites met!${NC}"
        return 0
    else
        echo -e "${YELLOW}Some services may not be available${NC}"
        return 1
    fi
}

run_test() {
    local test_script=$1
    local test_name=$2
    
    if [ ! -f "$test_script" ]; then
        echo -e "${YELLOW}Error: Test script not found: $test_script${NC}"
        return 1
    fi
    
    print_header "Running: $test_name"
    
    # Run the test
    if bash "$test_script"; then
        echo ""
        echo -e "${GREEN}✓ Test passed${NC}"
        return 0
    else
        echo ""
        echo -e "${YELLOW}⚠ Test failed or incomplete${NC}"
        return 1
    fi
}

view_logs() {
    print_header "Available Logs"
    
    echo "Choose a log to view:"
    echo "1. Consumer log"
    echo "2. Producer log"
    echo "3. File Producer log"
    echo "4. E2E Consumer log"
    echo "5. E2E Producer log"
    echo "6. All logs (last 20 lines)"
    echo "7. Back to menu"
    echo ""
    read -p "Select (1-7): " log_choice
    
    case $log_choice in
        1)
            echo ""
            tail -50 /tmp/consumer.log 2>/dev/null || echo "No log found"
            ;;
        2)
            echo ""
            tail -50 /tmp/producer.log 2>/dev/null || echo "No log found"
            ;;
        3)
            echo ""
            tail -50 /tmp/file-producer.log 2>/dev/null || echo "No log found"
            ;;
        4)
            echo ""
            tail -50 /tmp/e2e-consumer.log 2>/dev/null || echo "No log found"
            ;;
        5)
            echo ""
            tail -50 /tmp/e2e-producer.log 2>/dev/null || echo "No log found"
            ;;
        6)
            echo ""
            for log in /tmp/*-test.log /tmp/*.log; do
                if [ -f "$log" ]; then
                    echo "=== $(basename $log) ==="
                    tail -10 "$log"
                    echo ""
                fi
            done
            ;;
        7)
            return 0
            ;;
        *)
            echo "Invalid choice"
            ;;
    esac
    
    read -p "Press Enter to continue..."
}

check_service_status() {
    print_header "Service Status"
    
    echo "Running Processes:"
    ps aux | grep -E "postgres-consumer|postgres-producer|file-producer" | grep -v grep || echo "No services running"
    
    echo ""
    echo "Open Ports:"
    netstat -tlnp 2>/dev/null | grep -E "5432|5433|4222" || echo "Checking with lsof..."
    lsof -i :5432 -i :5433 -i :4222 2>/dev/null | grep -v COMMAND || echo "No services found on expected ports"
    
    echo ""
    read -p "Press Enter to continue..."
}

kill_services() {
    print_header "Stopping All Services"
    
    echo "Killing running services..."
    pkill -f postgres-consumer 2>/dev/null && echo "✓ Consumer killed" || echo "✓ Consumer not running"
    pkill -f postgres-producer 2>/dev/null && echo "✓ Producer killed" || echo "✓ Producer not running"
    pkill -f file-producer 2>/dev/null && echo "✓ File Producer killed" || echo "✓ File Producer not running"
    
    sleep 1
    
    echo ""
    echo "Remaining processes:"
    ps aux | grep -E "postgres-consumer|postgres-producer|file-producer" | grep -v grep || echo "✓ All services stopped"
    
    echo ""
    read -p "Press Enter to continue..."
}

main_menu() {
    local test_dir="/home/ludvik/vrsky/test"
    
    while true; do
        clear
        print_menu
        read -p "Select option (1-9): " choice
        
        case $choice in
            1)
                run_test "$test_dir/test-file-producer.sh" "File Producer Test"
                read -p "Press Enter to continue..."
                ;;
            2)
                run_test "$test_dir/test-postgres-consumer.sh" "PostgreSQL Consumer Test"
                read -p "Press Enter to continue..."
                ;;
            3)
                run_test "$test_dir/test-postgres-producer.sh" "PostgreSQL Producer Test"
                read -p "Press Enter to continue..."
                ;;
            4)
                run_test "$test_dir/test-e2e-pipeline.sh" "End-to-End Pipeline Test"
                read -p "Press Enter to continue..."
                ;;
            5)
                clear
                print_header "Running All Tests"
                
                echo "This will run all 4 component tests sequentially..."
                echo ""
                
                run_test "$test_dir/test-file-producer.sh" "File Producer Test" || true
                sleep 2
                
                run_test "$test_dir/test-postgres-consumer.sh" "PostgreSQL Consumer Test" || true
                sleep 2
                
                run_test "$test_dir/test-postgres-producer.sh" "PostgreSQL Producer Test" || true
                sleep 2
                
                run_test "$test_dir/test-e2e-pipeline.sh" "End-to-End Pipeline Test" || true
                
                echo ""
                read -p "Press Enter to continue..."
                ;;
            6)
                view_logs
                ;;
            7)
                check_service_status
                ;;
            8)
                kill_services
                ;;
            9)
                echo ""
                echo "Exiting..."
                exit 0
                ;;
            *)
                echo "Invalid option"
                sleep 1
                ;;
        esac
    done
}

# Main execution
if [ "$1" == "check" ]; then
    check_prerequisites
elif [ "$1" == "logs" ]; then
    view_logs
elif [ "$1" == "status" ]; then
    check_service_status
elif [ "$1" == "kill" ]; then
    kill_services
elif [ "$1" == "file" ]; then
    bash "/home/ludvik/vrsky/test/test-file-producer.sh"
elif [ "$1" == "consumer" ]; then
    bash "/home/ludvik/vrsky/test/test-postgres-consumer.sh"
elif [ "$1" == "producer" ]; then
    bash "/home/ludvik/vrsky/test/test-postgres-producer.sh"
elif [ "$1" == "e2e" ]; then
    bash "/home/ludvik/vrsky/test/test-e2e-pipeline.sh"
elif [ "$1" == "all" ]; then
    bash "/home/ludvik/vrsky/test/test-file-producer.sh"
    bash "/home/ludvik/vrsky/test/test-postgres-consumer.sh"
    bash "/home/ludvik/vrsky/test/test-postgres-producer.sh"
    bash "/home/ludvik/vrsky/test/test-e2e-pipeline.sh"
else
    # Interactive menu
    main_menu
fi
