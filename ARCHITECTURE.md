# Real-Time Chat Microservice - Architecture

## 🎯 Core Concept

The chat system is organized around **participant-based channels** where each channel is identified by the set of user IDs that are part of that conversation.

### Channel Structure

- **One-on-One Chat**: `["alice", "bob"]`
- **Group Chat**: `["alice", "bob", "charlie", "david"]`
- **Channel ID**: Comma-separated sorted user IDs (e.g., `"alice,bob,charlie"`)

**Key Principle**: Order of participant IDs doesn't matter. The system automatically sorts them for consistency.

## 🔑 Authentication & Authorization

### JWT Token Structure

```json
{
  "id": "alice",
  "iat": 1234567890,
  "exp": 1234654290
}
```

**Note**: No `groups` field needed anymore! Channel access is determined by participant lists.

### Access Control Rules

1. **Sending Messages**: User must be in the `participants` array
2. **Reading Messages**: User must be in the `participants` array
3. **WebSocket Connection**: User connects once and receives messages from ALL channels they're part of

## 🏗️ System Architecture

### WebSocket Connection Model

**Previous (channel-based)**:
- User connects to `/ws?channel=channelID`
- One connection per channel
- User needs multiple connections for multiple chats

**Current (user-based)**:
- User connects to `/ws` (single connection)
- Receives messages from ALL channels they're part of
- Much more efficient for users in multiple chats

### Message Broadcasting

**Key Feature**: Sender does NOT receive their own message via WebSocket

**Flow**:
1. User sends message via REST API
2. System broadcasts to all participants EXCEPT sender
3. Message is persisted to MongoDB asynchronously
4. Each participant's WebSocket connections receive the message

### Data Structure

**Message Model**:
```go
type Message struct {
    ID           string    `json:"id" bson:"_id"`
    Sender       string    `json:"sender" bson:"sender"`
    Content      string    `json:"content" bson:"content"`
    CreatedAt    time.Time `json:"created_at" bson:"created_at"`
    Participants []string  `json:"participants" bson:"participants"` // Sorted array
}
```

**MongoDB Storage**:
- Participants array is always sorted before storage
- Index on `participants` field for efficient querying
- Order-independent channel identification

## 📡 API Endpoints

### 1. WebSocket Connection
```
GET /ws
Headers: Authorization: Bearer <jwt-token>
```

Establishes a persistent connection for the authenticated user. Receives messages from all channels the user participates in.

### 2. Send Message
```
POST /api/messages
Headers: Authorization: Bearer <jwt-token>
Body: {
  "participants": ["alice", "bob", "charlie"],
  "content": "Hello everyone!"
}
```

Sends a message to a channel. The sender must be in the participants array.

### 3. Get Messages
```
GET /api/messages/get?participants=alice,bob,charlie
Headers: Authorization: Bearer <jwt-token>
```

Retrieves all messages from a channel. User must be a participant.

### 4. Get User Connections
```
POST /api/connections
Body: {
  "users": ["alice", "bob", "charlie"]
}
```

Returns how many active WebSocket connections each user has. No authentication required.

### 5. Health Check
```
GET /health
```

Service health status.

## 🔄 Message Flow

### Sending a Message

```
1. Client POST /api/messages
   {
     "participants": ["alice", "bob", "charlie"],
     "content": "Hello!"
   }

2. Server validates:
   - JWT token is valid
   - Sender (from JWT) is in participants array

3. Server creates message:
   - Sender: "alice" (from JWT)
   - Participants: ["alice", "bob", "charlie"] (sorted)
   - Content: "Hello!"
   - CreatedAt: current timestamp

4. Server broadcasts to participants:
   - Sends to Bob's WebSocket connections ✅
   - Sends to Charlie's WebSocket connections ✅
   - Does NOT send to Alice (sender) ❌

5. Server persists to MongoDB (async):
   - Retries on failure
   - Exponential backoff
```

### Receiving Messages

```
1. User connects: GET /ws
   - Server registers WebSocket by user ID

2. When message arrives for any channel containing this user:
   - Server looks up user's WebSocket connections
   - Broadcasts to all their connections
   - Message includes full context (sender, participants, content)

3. Client receives:
   {
     "id": "...",
     "sender": "alice",
     "content": "Hello!",
     "created_at": "2025-10-31T...",
     "participants": ["alice", "bob", "charlie"]
   }
```

## 🏛️ Internal Architecture

### Hub (Connection Manager)

**Structure**:
```go
type Hub struct {
    clients map[string]map[*Client]bool  // userID -> set of connections
}
```

**Key Features**:
- Organizes connections by user ID, not channel
- One user can have multiple WebSocket connections
- Broadcasts go to all connections of each participant

### Broadcasting Logic

```go
type BroadcastMessage struct {
    Participants []string  // Who should receive
    Message      []byte    // JSON payload
    SenderID     string    // Who sent it (excluded from broadcast)
}
```

**Process**:
1. Iterate through participants
2. Skip sender
3. For each participant, send to all their WebSocket connections
4. Non-blocking: uses goroutines per connection

### MongoDB Repository

**Key Methods**:
- `GetMessagesByParticipants(participants []string)`: Retrieves channel messages
- `Save(msg *Message)`: Persists message (sorts participants first)
- `SaveAsync(msg *Message, maxRetries int)`: Async save with retry

**Query Strategy**:
```javascript
// MongoDB query
{
  "participants": ["alice", "bob", "charlie"]  // Exact match (sorted)
}
```

## 🎨 Design Decisions

### Why Participant Arrays?

**Advantages**:
1. ✅ No need for separate channel ID generation
2. ✅ Self-documenting: channel ID tells you who's in it
3. ✅ No "groups" field in JWT (cleaner auth)
4. ✅ Easy to query: "give me all messages where I'm a participant"
5. ✅ Order-independent through sorting

**Implementation**:
- Always sort before storage/comparison
- Use comma-joined string as channel identifier
- MongoDB index on participants array

### Why Single WebSocket per User?

**Advantages**:
1. ✅ Scalability: One connection instead of N (N = number of channels)
2. ✅ Simplicity: Client code is simpler
3. ✅ Efficiency: Less network overhead
4. ✅ Mobile-friendly: Fewer connections = better battery life

**Trade-off**:
- Server must track which channels user is in
- Solved by including participants array in each message

### Why Exclude Sender from Broadcast?

**Advantages**:
1. ✅ Client sees message immediately after sending (optimistic UI)
2. ✅ Prevents duplicate message on sender's screen
3. ✅ Reduces bandwidth
4. ✅ Standard chat pattern

## 🚀 Scalability Considerations

### Current Design
- Single Go process
- In-memory connection management
- MongoDB for persistence

### Horizontal Scaling Strategy
For multiple instances, you'll need:

1. **Redis Pub/Sub** for cross-instance message broadcasting
2. **Sticky sessions** or connection affinity
3. **Shared storage** for connection registry (Redis)

**Example Multi-Instance Flow**:
```
Instance A                Instance B
   |                         |
   |-- User Alice           |-- User Bob
   |                         |
   |<------- Redis Pub/Sub ------>|
   |                         |
   |-- Broadcast to Alice   |-- Broadcast to Bob
```

## 📊 Performance Characteristics

### Concurrency
- Each WebSocket write happens in its own goroutine
- Non-blocking broadcasts
- Async persistence with retry

### Database
- Index on `participants` for O(log n) lookups
- Sorted arrays enable exact match queries
- No need for complex $elemMatch queries

### Memory
- O(U) where U = number of connected users
- Each user can have multiple connections
- Efficient for large numbers of users in few channels each

## 🔧 Configuration

### Environment Variables
- `MONGO_URI`: MongoDB connection string (default: `mongodb://localhost:27017`)
- `MONGO_DB`: Database name (default: `chatdb`)
- `MONGO_COLLECTION`: Collection name (default: `messages`)
- `RETRY_ATTEMPTS`: Max retry attempts for async save (default: `5`)
- `PORT`: Server port (default: `8080`)

### Docker Setup
```bash
docker-compose up --build
```

Includes:
- MongoDB with persistent volumes
- Chat microservice with environment configuration
- Network for inter-service communication

## 🎯 Use Cases

### One-on-One Chat
```javascript
// Alice sends to Bob
POST /api/messages
{
  "participants": ["alice", "bob"],
  "content": "Hi Bob!"
}

// Channel ID internally: "alice,bob"
// Both can retrieve with: GET /api/messages/get?participants=alice,bob
```

### Group Chat
```javascript
// Charlie creates group with Alice and Bob
POST /api/messages
{
  "participants": ["alice", "bob", "charlie"],
  "content": "Welcome to the group!"
}

// Channel ID internally: "alice,bob,charlie"
// All three receive via their WebSocket connections
// Sender (Charlie) doesn't receive duplicate via WebSocket
```

### Multiple Concurrent Chats
```javascript
// User Alice:
// - Connected to WebSocket once
// - Receives from channel ["alice", "bob"]
// - Receives from channel ["alice", "charlie"]
// - Receives from channel ["alice", "bob", "charlie", "david"]
// All through same WebSocket connection
```

## 🧪 Testing with Demo Client

### Start Demo
```bash
cd demo
npm install
node index.js
```

### Scenario: Two-User Chat
**Terminal 1 (Alice)**:
```
Enter your user ID: alice
[Select: 1] Connect to WebSocket
[Select: 2] Send message
  Participants: alice, bob
  Message: Hi Bob!
```

**Terminal 2 (Bob)**:
```
Enter your user ID: bob
[Select: 1] Connect to WebSocket
[Receives] alice: Hi Bob!
[Select: 2] Send message
  Participants: alice, bob
  Message: Hi Alice!
```

### Scenario: Group Chat
**Terminal 1 (Alice)**:
```
[Select: 2] Send message
  Participants: alice, bob, charlie
  Message: Hello team!
```

**Terminal 2 (Bob) & Terminal 3 (Charlie)**:
Both receive:
```
[Channel: alice, bob, charlie]
alice: Hello team!
```

Note: Alice doesn't receive her own message via WebSocket!

## 🔐 Security Considerations

### Current Implementation
- JWT parsing without signature verification (demo only!)
- No rate limiting
- No input sanitization
- CORS allows all origins

### Production Recommendations
1. ✅ Verify JWT signatures (use proper JWT library)
2. ✅ Add rate limiting per user
3. ✅ Sanitize message content
4. ✅ Implement proper CORS policy
5. ✅ Add message size limits
6. ✅ Implement user blocking/reporting
7. ✅ Add audit logging
8. ✅ Use TLS/WSS in production

## 📈 Future Enhancements

### Potential Features
- [ ] Message editing/deletion
- [ ] Read receipts (track who read each message)
- [ ] Typing indicators
- [ ] File attachments
- [ ] Message reactions
- [ ] Push notifications
- [ ] Message search
- [ ] Channel metadata (name, avatar)
- [ ] Admin/moderator roles
- [ ] Message pagination
- [ ] Offline message queue

### Technical Improvements
- [ ] Redis for distributed broadcasting
- [ ] Message compression
- [ ] Connection pooling optimization
- [ ] Prometheus metrics
- [ ] Distributed tracing
- [ ] Load testing suite
- [ ] Circuit breakers for MongoDB
- [ ] Graceful shutdown handling
