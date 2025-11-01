#!/bin/bash

# Test runner script for the chat microservice
# This script provides convenient commands for running the test suite

set -e

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${BLUE}Chat Microservice Test Runner${NC}"
echo "================================"
echo ""

# Function to check if MongoDB is running
check_mongo() {
    echo -e "${BLUE}Checking MongoDB connection...${NC}"
    if timeout 2 bash -c "echo > /dev/tcp/localhost/27017" 2>/dev/null; then
        echo -e "${GREEN}✓ MongoDB is running${NC}"
        return 0
    else
        echo -e "${RED}✗ MongoDB is not accessible on localhost:27017${NC}"
        echo "Please start MongoDB first:"
        echo "  docker run -d -p 27017:27017 mongo:7.0"
        return 1
    fi
}

# Function to run basic tests
run_tests() {
    echo -e "\n${BLUE}Running integration tests...${NC}"
    go test -v ./test
}

# Function to run tests with race detection
run_race_tests() {
    echo -e "\n${BLUE}Running tests with race detection...${NC}"
    go test -v -race ./test
}

# Function to run tests with coverage
run_coverage() {
    echo -e "\n${BLUE}Running tests with coverage...${NC}"
    go test -v -coverprofile=test/coverage.out ./test
    if [ -f "test/coverage.out" ]; then
        go tool cover -func=test/coverage.out
        echo -e "\n${GREEN}Coverage report generated: test/coverage.out${NC}"
        echo "To view HTML report: go tool cover -html=test/coverage.out"
    fi
}

# Function to run a specific test
run_specific() {
    local test_name=$1
    echo -e "\n${BLUE}Running specific test: ${test_name}${NC}"
    go test -v ./test -run "${test_name}"
}

# Function to show test statistics
show_stats() {
    echo -e "\n${BLUE}Test Statistics:${NC}"
    echo "Number of test functions: $(grep -c "^func Test" test/integration_test.go)"
    echo "Lines of test code: $(wc -l < test/integration_test.go)"
    echo ""
}

# Main menu
if [ $# -eq 0 ]; then
    check_mongo || exit 1
    echo ""
    echo "Usage: $0 [command]"
    echo ""
    echo "Commands:"
    echo "  all         - Run all tests"
    echo "  race        - Run tests with race detection"
    echo "  coverage    - Run tests with coverage report"
    echo "  <name>      - Run a specific test (e.g., TestHighConcurrency)"
    echo "  stats       - Show test statistics"
    echo ""
    echo "Running all tests by default..."
    run_tests
else
    case "$1" in
        all)
            check_mongo || exit 1
            run_tests
            ;;
        race)
            check_mongo || exit 1
            run_race_tests
            ;;
        coverage)
            check_mongo || exit 1
            run_coverage
            ;;
        stats)
            show_stats
            ;;
        *)
            check_mongo || exit 1
            run_specific "$1"
            ;;
    esac
fi

echo -e "\n${GREEN}Done!${NC}"
