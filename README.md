# Real-Time Chat Microservice

A production-ready, scalable Go microservice for real-time chat with support for one-on-one conversations and group chats. Built with WebSocket for instant messaging and MongoDB for persistent storage.

## 🛠️ Tech Stack

| Golang | MongoDB | Docker | WebSocket |
| ------ | ------ | ---------- | --------- |
| <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/go.png"> | <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/mongodb.png"> | <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/docker.png"> | 🔌 |


## ✨ Key Features

- 🚀 **Participant-Based Channels**: Channels are defined by participant user IDs - no arbitrary channel IDs needed
- 🔌 **Efficient WebSocket**: Single connection per user receives messages from all channels
- 🎯 **Smart Broadcasting**: Sender doesn't receive their own messages (prevents duplicates)
- 💾 **Async Persistence**: Messages broadcast immediately, saved to MongoDB with retry logic
- 🔐 **JWT Authentication**: Secure user identification with clean authorization model
- 📊 **Scalable Architecture**: Modular design ready for horizontal scaling
- 🐳 **Docker Ready**: Complete Docker Compose setup with MongoDB volumes

## 🏗️ Architecture

### Channel Model
Channels are identified by the set of participants (user IDs):
- **One-on-One**: `["alice", "bob"]`
- **Group Chat**: `["alice", "bob", "charlie"]`
- **Order Independent**: `["alice", "bob"]` = `["bob", "alice"]` (automatically sorted)

### WebSocket Model
- User connects once: `GET /ws`
- Receives messages from ALL channels they're part of
- Much more efficient than one connection per channel

### Broadcasting Logic
- Message sent via REST API → Broadcast to all participants except sender → Async persist to MongoDB
- Sender sees message immediately in UI (optimistic update)
- Other participants receive via WebSocket in real-time

## 📁 Project Structure

```
.
├── cmd/server/          # Application entrypoint
├── internal/
│   ├── httpapi/        # HTTP & WebSocket handlers
│   ├── ws/             # WebSocket hub (user-based connection management)
│   ├── service/        # Business logic layer
│   ├── repository/     # MongoDB persistence with retry
│   └── middleware/     # JWT authentication
├── pkg/models/         # Domain models (Message structure)
├── demo/               # Node.js CLI demo client
├── docker-compose.yml  # Docker services configuration
└── Dockerfile          # Multi-stage Go build

```

## 🚀 Quick Start

### Option 1: Docker Compose (Recommended)

```bash
# Clone and start
docker-compose up --build

# The service is now running at:
# HTTP: http://localhost:8080
# WebSocket: ws://localhost:8080/ws
```

### Option 2: Local Development

```bash
# Start MongoDB
docker run -d -p 27017:27017 --name mongodb mongo:7.0

# Configure environment
export MONGO_URI="mongodb://localhost:27017"
export MONGO_DB="chatdb"
export MONGO_COLLECTION="messages"

# Run the server
go run cmd/server/main.go
```

### Try the Demo Client

```bash
cd demo
npm install
node index.js
```

See [QUICKSTART.md](./QUICKSTART.md) for detailed examples!

## 📡 API Endpoints

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/health` | No | Service health check |
| GET | `/ws` | JWT | WebSocket connection (all channels) |
| POST | `/api/messages` | JWT | Send message to channel |
| GET | `/api/messages/get` | JWT | Get channel messages (with pagination) |
| POST | `/api/connections` | No | Check user connection counts |

### Pagination Support

The GET messages endpoint supports pagination for efficient message retrieval:

**Query Parameters:**
- `participants` (required): Comma-separated user IDs
- `page` (optional): Page number, 0-indexed (default: 0)
- `size` (optional): Messages per page (default: 50, max: 100)

**Formula:** `offset = page × size`

Messages are always sorted by **newest first** (descending `created_at`).

## 🔑 Authentication

### JWT Token Structure

```json
{
  "id": "alice",
  "iat": 1730380800,
  "exp": 1730467200
}
```

**Note**: No `groups` field needed! Authorization is based on participant lists.

### Authorization Rules

- **Send Message**: User must be in the `participants` array
- **Read Messages**: User must be in the `participants` array  
- **WebSocket**: Connects with user ID, receives from all channels they're in

## 💬 Usage Examples

### Send a Message

```bash
curl -X POST http://localhost:8080/api/messages \
  -H "Authorization: Bearer YOUR_JWT" \
  -H "Content-Type: application/json" \
  -d '{
    "participants": ["alice", "bob"],
    "content": "Hello Bob!"
  }'
```

### Get Messages

```bash
# Get latest 50 messages (default)
curl -X GET "http://localhost:8080/api/messages/get?participants=alice,bob" \
  -H "Authorization: Bearer YOUR_JWT"

# Get first 20 messages (latest)
curl -X GET "http://localhost:8080/api/messages/get?participants=alice,bob&page=0&size=20" \
  -H "Authorization: Bearer YOUR_JWT"

# Get next 20 messages (older)
curl -X GET "http://localhost:8080/api/messages/get?participants=alice,bob&page=1&size=20" \
  -H "Authorization: Bearer YOUR_JWT"
```

### Check Who's Online

```bash
curl -X POST http://localhost:8080/api/connections \
  -H "Content-Type: application/json" \
  -d '{
    "users": ["alice", "bob", "charlie"]
  }'
```

Response:
```json
{
  "alice": 2,
  "bob": 1,
  "charlie": 0
}
```

### WebSocket (JavaScript Example)

```javascript
const ws = new WebSocket('ws://localhost:8080/ws', {
  headers: { 'Authorization': 'Bearer YOUR_JWT' }
});

ws.on('message', (data) => {
  const msg = JSON.parse(data);
  console.log(`Channel [${msg.participants.join(', ')}]`);
  console.log(`${msg.sender}: ${msg.content}`);
});
```

## ⚙️ Configuration

### Environment Variables

```bash
# MongoDB Configuration
MONGO_URI=mongodb://localhost:27017
MONGO_DB=chatdb
MONGO_COLLECTION=messages

# Server Configuration
PORT=8080
RETRY_ATTEMPTS=5  # Message persistence retry count

# JWT (for demo only)
JWT_SECRET=your-jwt-secret
```

### Docker Volumes

- `mongodb_data`: MongoDB database files
- `mongodb_config`: MongoDB configuration

## 📊 Message Format

### Sent via API

```json
{
  "participants": ["alice", "bob", "charlie"],
  "content": "Hello everyone!"
}
```

### Received via WebSocket

```json
{
  "id": "507f1f77bcf86cd799439011",
  "sender": "alice",
  "content": "Hello everyone!",
  "participants": ["alice", "bob", "charlie"],
  "created_at": "2025-10-31T10:30:45Z"
}
```

## 🎯 Key Behaviors

1. **Order Independence**: `["alice", "bob"]` and `["bob", "alice"]` are the same channel
2. **No Echo**: Sender doesn't receive their own message via WebSocket
3. **Single Connection**: One WebSocket per user handles all channels
4. **Broadcast First**: Messages sent to WebSocket immediately, then persisted async
5. **Retry Logic**: Failed MongoDB saves retry with exponential backoff

## 📚 Documentation

- [**QUICKSTART.md**](./QUICKSTART.md) - Get up and running in 5 minutes
- [**ARCHITECTURE.md**](./ARCHITECTURE.md) - Deep dive into system design
- [**PAGINATION.md**](./PAGINATION.md) - Complete pagination guide with examples
- [**MIGRATION.md**](./MIGRATION.md) - Upgrading from channel-ID-based systems
- [**CONCURRENCY.md**](./CONCURRENCY.md) - Concurrency optimizations and worker pools
- [**test/README.md**](./test/README.md) - Integration test suite documentation
- [**demo/README.md**](./demo/README.md) - Demo client usage guide

## 🧪 Testing

### Integration Test Suite

The project includes a comprehensive integration test suite that validates the entire system under high-concurrency scenarios:

```bash
# Run all tests
go test -v ./test

# Or use the test runner script
./test/run_tests.sh all

# Run with race detection
./test/run_tests.sh race

# Run with coverage
./test/run_tests.sh coverage
```

**Test Coverage**:
- ✅ High concurrency (10 users, 90 concurrent messages)
- ✅ Group chat functionality (multi-participant channels)
- ✅ Pagination system validation
- ✅ Authorization and access control
- ✅ Sender exclusion from broadcasts
- ✅ Database persistence verification

See [test/README.md](./test/README.md) for detailed test documentation.

### Manual Multi-User Chat Test

**Terminal 1 (Alice)**:
```bash
cd demo && node index.js
# User: alice
# Option 1: Connect WebSocket
```

**Terminal 2 (Bob)**:
```bash
cd demo && node index.js
# User: bob
# Option 2: Send message
# Participants: alice, bob
# Message: Hi Alice!
```

Alice's terminal will show:
```
[10:30:45] Channel [alice, bob]
  bob: Hi Alice!
```

## 🔧 Development

### Build

```bash
go build ./...
```

### Run Tests (when implemented)

```bash
go test ./...
```

### View Logs

```bash
# Docker
docker-compose logs -f chat-api

# Local
# Logs output to stdout
```

### MongoDB Shell

```bash
docker exec -it real-time-chat-microservice-mongodb-1 mongosh

use chatdb
db.messages.find().pretty()
```

## 🚀 Production Considerations

### Current Implementation
- Single instance
- In-memory connection management
- MongoDB for persistence

### For Multi-Instance Deployment
1. Add **Redis Pub/Sub** for cross-instance messaging
2. Implement **sticky sessions** or connection affinity
3. Use **Redis** for shared connection registry
4. Consider **message queue** for reliable delivery

See [ARCHITECTURE.md](./ARCHITECTURE.md) for scaling strategies.

## 🔒 Security Notes

⚠️ **Current JWT implementation is for demo purposes only!**

For production:
- [ ] Verify JWT signatures (use proper JWT library)
- [ ] Add rate limiting per user
- [ ] Sanitize message content
- [ ] Implement proper CORS policy
- [ ] Add message size limits
- [ ] Use TLS/WSS for WebSocket
- [ ] Add authentication service integration
- [ ] Implement user blocking/reporting

## 🤝 Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests (when test suite exists)
5. Submit a pull request

## 📄 License

[MIT License](./LICENSE)

## 🙏 Acknowledgments

Built with:
- [gorilla/websocket](https://github.com/gorilla/websocket) - WebSocket implementation
- [MongoDB Go Driver](https://github.com/mongodb/mongo-go-driver) - MongoDB client
- [Docker](https://www.docker.com/) - Containerization

---

**Need help?** Check out the documentation or open an issue!
