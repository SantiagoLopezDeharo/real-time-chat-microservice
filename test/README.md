# Integration Test Suite

This directory contains comprehensive integration tests for the real-time chat microservice. The tests validate the entire system under realistic, high-concurrency scenarios.

## Overview

The test suite simulates real users connecting to the service via WebSockets, sending messages concurrently, and verifying that all messages are correctly delivered and persisted. It validates:

- **High Concurrency**: The worker pool optimizations handle many concurrent users
- **Message Delivery**: WebSocket broadcasts work correctly and exclude senders
- **Data Persistence**: All messages are correctly saved to MongoDB
- **Authorization**: Access control is enforced for reading messages
- **Pagination**: The pagination system works correctly
- **Group Chats**: Multi-participant channels function properly

## Test Cases

### 1. TestHighConcurrency

**Purpose**: Validates the system under high load with many concurrent users.

**Scenario**:
- Creates 10 simulated users
- Each user connects via WebSocket
- Each user sends a message to every other user (90 total messages)
- Verifies all messages are delivered in real-time via WebSocket
- Confirms sender exclusion (users don't receive their own messages)
- Validates database persistence through REST API queries

**Key Validations**:
- Worker pools prevent resource exhaustion
- Message routing is correct
- No message loss occurs
- Database writes complete successfully

### 2. TestGroupChat

**Purpose**: Tests multi-participant group conversations.

**Scenario**:
- Creates 5 users in a single group chat
- Each user sends one message to the group
- Verifies each user receives messages from all other participants
- Confirms sender exclusion in group context
- Validates all messages are persisted with correct participant lists

**Key Validations**:
- Group message broadcasts work correctly
- Participant lists are maintained
- Each user receives exactly (n-1) messages in an n-person group

### 3. TestPagination

**Purpose**: Validates the pagination system for message history.

**Scenario**:
- Two users exchange 25 messages
- Retrieves messages in pages of 10
- Verifies correct page sizes (10, 10, 5)
- Confirms no duplicate or missing messages across pages

**Key Validations**:
- Pagination parameters (page, size) work correctly
- Messages are ordered chronologically (latest first)
- No data loss or duplication occurs

### 4. TestUnauthorizedAccess

**Purpose**: Tests access control for channel messages.

**Scenario**:
- User1 and User2 have a private conversation
- User3 attempts to read their messages
- Verifies User3 receives an empty result

**Key Validations**:
- Non-participants cannot access channel messages
- Authorization logic is enforced at the service layer

### 5. TestSenderMustBeParticipant

**Purpose**: Validates that only channel participants can send messages.

**Scenario**:
- User attempts to send a message to a channel they're not part of
- Verifies the request is rejected with HTTP 403 Forbidden

**Key Validations**:
- Sender must be in the participants list
- Proper HTTP status codes are returned

## Running the Tests

### Prerequisites

1. **MongoDB**: A running MongoDB instance on `localhost:27017`
2. **Go 1.22+**: The project requires Go 1.22 or later
3. **Dependencies**: Install with `go mod tidy`

### Run All Tests

```bash
go test -v ./test
```

### Run a Specific Test

```bash
go test -v ./test -run TestHighConcurrency
```

### Run with Race Detection

```bash
go test -v -race ./test
```

This is especially useful for validating the worker pool implementation and concurrent access patterns.

## Test Configuration

Key constants in `integration_test.go`:

```go
const (
    mongoURITest    = "mongodb://localhost:27017"  // MongoDB connection
    dbNameTest      = "chat_test"                  // Test database
    collectionTest  = "messages_test"              // Test collection
    jwtSecretTest   = "test-secret"                // JWT signing key
    numUsers        = 10                           // Users in concurrency test
    messagesPerUser = 5                            // Messages per user
)
```

You can modify these values to adjust test intensity.

## Test Architecture

### SimulatedUser

Each test uses `SimulatedUser` instances that:
- Generate their own JWT tokens for authentication
- Establish persistent WebSocket connections
- Listen for incoming messages in a dedicated goroutine
- Send messages via the REST API
- Track sent and received messages for verification

### Test Server

The test suite uses Go's `httptest.Server` to create a real HTTP server with:
- Full WebSocket support
- JWT authentication middleware
- The actual chat service with MongoDB backend
- The worker pool optimizations

This ensures tests validate the complete, production-like system.

## Performance Metrics

Example output from a successful test run:

```
TestHighConcurrency: 10 users, 90 messages, ~650ms
TestGroupChat: 5 users, 5 messages, ~600ms  
TestPagination: 2 users, 25 messages, ~930ms
TestUnauthorizedAccess: ~1ms
TestSenderMustBeParticipant: ~1ms
```

Total suite execution: **~2.3 seconds**

## Interpreting Results

### Success Indicators

- All tests pass with `PASS` status
- Log shows "Test completed successfully!" for each test
- All users register and unregister cleanly
- DB workers start without errors

### Failure Scenarios

If tests fail, check:

1. **MongoDB Connection**: Ensure MongoDB is running and accessible
2. **Port Conflicts**: The test server uses a random available port
3. **Timing Issues**: Tests have generous timeouts (10s), but very slow systems may need adjustments
4. **Database State**: The test suite cleans the database before running

## CI/CD Integration

To run tests in a CI/CD pipeline:

```bash
# Start MongoDB (if not already running)
docker run -d -p 27017:27017 mongo:7.0

# Run tests
go test -v ./test -timeout 30s

# With coverage
go test -v ./test -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## Extending the Tests

To add new test cases:

1. Follow the existing pattern in `integration_test.go`
2. Use `SimulatedUser` for client simulation
3. Use `waitTimeout` for coordinating async operations
4. Add a 500ms sleep after sending messages to allow DB writes
5. Clean up connections with `defer user.Close()`

Example:

```go
func TestMyNewScenario(t *testing.T) {
    var wg sync.WaitGroup
    user := NewSimulatedUser(t, 1000, &wg)
    user.Connect(testServer.URL)
    defer user.Close()
    
    // Your test logic here
}
```

## Notes

- **Test Isolation**: Each test uses the same database but different user IDs to prevent conflicts
- **Async Operations**: The 500ms sleep after message sending accounts for async DB writes via the worker pool
- **Real WebSockets**: Tests use actual WebSocket connections, not mocks
- **Production Parity**: The test environment mirrors the production setup as closely as possible
