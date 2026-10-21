# Real-Time Chat Microservice

A production-ready, scalable Go microservice for real-time chat with support for one-on-one conversations and group chats. Built with WebSocket for instant messaging and MongoDB for persistent storage.

## 🛠️ Tech Stack

| Golang | MongoDB | Docker | 
| ------ | ------ | ---------- | 
| <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/go.png"> | <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/mongodb.png"> | <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/docker.png"> |


## ✨ Key Features

- 🚀 **Participant-Based Channels**: Channels are defined by participant user IDs - no arbitrary channel IDs needed
- 🔌 **Efficient WebSocket**: Single connection per user receives messages from all channels
- 🎯 **Smart Broadcasting**: Sender doesn't receive their own messages (prevents duplicates)
- 💾 **Async Persistence**: Messages broadcast immediately, saved to MongoDB in batches with idempotent retries and drained on shutdown
- 🔐 **JWT Authentication**: Secure user identification with clean authorization model
- 📊 **Scalable Architecture**: Modular design ready for horizontal scaling
- 🐳 **Docker Ready**: 15 MB multi-arch image on Docker Hub plus a Docker Compose setup with MongoDB volumes

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
- Message sent via REST API → Queued for persistence → Broadcast to all participants except sender → Batched write to MongoDB
- Sender sees message immediately in UI (optimistic update)
- Other participants receive via WebSocket in real-time

## 📁 Project Structure

```
.
├── cmd/server/          # Application entrypoint and graceful shutdown
├── internal/
│   ├── config/         # Environment configuration and validation
│   ├── server/         # Routes, HTTP server hardening, pprof listener
│   ├── httpapi/        # HTTP & WebSocket handlers
│   ├── ws/             # WebSocket hub (user-based connection management)
│   ├── service/        # Business logic and batched persistence
│   ├── repository/     # MongoDB persistence
│   └── middleware/     # JWT authentication and rate limiting
├── pkg/models/         # Domain models (Message structure)
├── test/e2e/           # End-to-end, security, concurrency and performance suites
├── demo/               # Node.js CLI demo client
├── docker-compose.yml  # Docker services configuration
└── Dockerfile          # Multi-stage build on scratch

```

## 🚀 Quick Start

### Option 1: Docker image

```bash
docker run -d -p 27017:27017 --name mongodb mongo:7.0
docker run --rm -p 8080:8080 \
  -e JWT_SECRET=your-jwt-secret \
  -e MONGO_URI=mongodb://host.docker.internal:27017 \
  slincsilver/real-time-chat-microservice:latest
```

The image (`linux/amd64`, `linux/arm64`) is built from `scratch`, runs as a non-root user and ships the CA bundle for TLS connections such as `mongodb+srv://` URIs.

### Option 2: Docker Compose

```bash
# Clone and start
docker-compose up --build

# The service is now running at:
# HTTP: http://localhost:8080
# WebSocket: ws://localhost:8080/ws
```

### Option 3: Local Development

```bash
# Start MongoDB
docker run -d -p 27017:27017 --name mongodb mongo:7.0

# Configure environment
export MONGO_URI="mongodb://localhost:27017"
export MONGO_DB="chatdb"
export MONGO_COLLECTION="messages"
export JWT_SECRET="your-jwt-secret"

# Run the server
go run cmd/server/main.go
```

### Try the Demo Client

```bash
cd demo
npm install
node index.js
```

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

Messages are always sorted by **newest first** (descending `created_at`, ties broken by `id`). `page` is limited to 10000; an empty conversation returns `[]`.

### Responses

| Status | When |
|--------|------|
| `202` | Message accepted: `{"status":"message queued","id":"<message id>"}` |
| `400` | Malformed JSON, no or empty participant IDs, more than 1000 participants, empty content |
| `401` | Missing, malformed, forged or expired JWT, or a JWT without an `id` claim |
| `403` | The authenticated user is not one of the participants (sending or reading) |
| `413` | Request body larger than `MAX_BODY_BYTES` |
| `429` | Per-user rate limit exceeded |
| `503` | Persistence is saturated; retry after the `Retry-After` header. Nothing was delivered |

## 🔑 Authentication

### JWT Token Structure

```json
{
  "id": "alice",
  "iat": 1730380800,
  "exp": 1730467200
}
```

**Note**: No `groups` field needed! Authorization is based on participant lists. Only HMAC-signed tokens (`HS256`/`HS384`/`HS512`) are accepted and the `id` claim is required.

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

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | – (required) | HMAC secret used to verify user JWTs |
| `MONGO_URI` | `mongodb://localhost:27017` | MongoDB connection string |
| `MONGO_DB` | `chatdb` | Database name |
| `MONGO_COLLECTION` | `messages` | Collection name |
| `PORT` | `8080` | HTTP listen port |
| `RETRY_ATTEMPTS` | `5` | Attempts per persistence batch, must be ≥ 1 |
| `RATE_LIMIT_RPS` | `5` | Sustained requests per second per user on `/api/*` |
| `RATE_LIMIT_BURST` | `10` | Burst size per user, must be ≥ 1 |
| `MAX_BODY_BYTES` | `65536` | Maximum request body; larger requests get `413` |
| `PPROF_ADDR` | – (off) | Address for an optional `net/http/pprof` listener, e.g. `127.0.0.1:6060`. Never expose it publicly |

Invalid values stop the service at startup with every problem reported at once. Note: MongoDB 8.x and 9.x refuse to start on Linux kernels 6.19+ ([SERVER-121912](https://jira.mongodb.org/browse/SERVER-121912)), which includes current Docker Desktop VMs, so the examples use `mongo:7.0`.

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

1. **Order Independence**: `["alice", "bob"]`, `["bob", "alice"]` and `["bob", " alice", "bob"]` are the same channel; IDs are trimmed and de-duplicated
2. **No Echo**: Sender doesn't receive their own message via WebSocket
3. **Single Connection**: One WebSocket per user handles all channels; every open tab of a recipient receives the message
4. **Deliver Immediately, Persist in Batches**: Messages are queued for persistence, delivered over WebSocket at once and written to MongoDB by workers that batch whatever has queued up
5. **Backpressure**: When persistence cannot keep up the API answers `503` instead of blocking, and nothing is delivered for rejected messages
6. **Retry Logic**: Failed batches retry with linear backoff; message IDs are generated up front, so retries never duplicate messages
7. **Durable Shutdown**: On `SIGTERM` the service stops accepting requests, closes WebSockets with `1001`, persists every acknowledged message and disconnects from MongoDB
8. **Slow Consumers**: A connection with more than 4 MiB of undelivered messages is closed with `1008` so it never slows down others; clients should reconnect and reload history

## 📚 Documentation

- [**ARCHITECTURE.md**](./ARCHITECTURE.md) - Deep dive into system design
- [**demo/README.md**](./demo/README.md) - Demo client usage guide

## 🧪 Testing

```bash
make test        # unit tests with the race detector (starts a throwaway mongo:7.0, or set MONGO_TEST_URI)
make e2e         # end-to-end suites against a race-detector build
make e2e-image   # functional, security and concurrency suites against the Docker image
make perf        # performance scenarios and optimization report
make bench       # micro benchmarks
```

- **Unit tests** cover every package: configuration validation, participant normalization (fuzzed), JWT forgery and algorithm confusion, per-user rate limiting, request decoding and error mapping, persistence backpressure, batching, retries and drain on shutdown, websocket fan-out, framing, ordering, ping/pong liveness and slow-consumer eviction, and the MongoDB repository against a real database.
- **End-to-end tests** (`test/e2e`, build tag `e2e`) start MongoDB in Docker, build the real binary with the race detector and coverage, boot it with throwaway secrets and its own database, and drive it with simulated users and browser tabs:
  - *functional*: direct and group messages, multi-tab delivery, participant set semantics, history order and pagination, verbatim content, reconnects, graceful shutdown, every acknowledged message surviving a restart, startup validation;
  - *security*: forged/expired/algorithm-confused tokens and tokens without a user, sender spoofing, outsiders reading or writing conversations, Mongo operator injection, body/participant/page limits, rate limiting, websocket protocol violations, slowloris, connection floods with goroutine-leak detection, no secrets or internals in error responses;
  - *concurrency*: concurrent group senders with exactly-once and per-sender ordering checks, isolated parallel conversations, tab churn, slow consumers, `SIGTERM` under load with exact durability checks, `503` backpressure while MongoDB is frozen with `docker pause`. Any data race, panic or unclean exit reported by the server fails the run.
- **Performance suite** profiles the live server through pprof and writes `test/e2e/reports/perf-<label>.md` with latency percentiles, throughput, memory and goroutines per connection, CPU/allocation/heap hotspots and the optimization opportunities they point to. `E2E_BINARY` profiles another build and `E2E_PERF_BASELINE` adds a comparison column.

## ⚡ Performance

Driven by the performance suite's findings, measured on an Apple M3 Pro (11 cores) with the load generator and MongoDB on the same host:

| Metric | Before | After |
|--------|-------:|------:|
| Memory (RSS) per idle connection | 24.8 KiB | 13.0 KiB |
| Goroutines per idle connection | 2 | 1 |
| One-to-one messages per second (32 senders) | 7.0k | 45–50k |
| Send request latency p50 | 4.5 ms | 0.4 ms |
| Messages persisted per second | 6.4k | 44–52k |
| History reads per second (newest 50 of 6000) | 0.8k | 5.8–6.0k |
| History read latency p99 | 104 ms | 16–17 ms |
| Group fan-out to 999 members, delivery latency p50 / p99 | 3.8 / 9.6 ms | 3.9–4.6 / 9.8–10.9 ms |

The previous implementation inserted one document per round trip and made senders wait on it, which capped intake at the database's single-insert rate; persistence now writes batches and the history index covers the sort. At 7× the message rate, one-to-one delivery latency p99 rises from 1 ms to 4–5 ms. Remaining hotspots are kernel socket IO and the MongoDB driver allocating a decoder per inserted document to read back client-generated IDs.

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

### Run Tests

```bash
make test e2e
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

Built in:
- JWT verification pinned to HMAC algorithms with a required user ID
- Per-user rate limiting, request body limits and participant limits
- HTTP header/read/write timeouts against slowloris-style attacks
- Participation checks on every read and write
- Bounded per-connection queues and slow-consumer eviction

For production:
- [ ] Terminate TLS/WSS in front of the service
- [ ] Add authentication service integration
- [ ] `/api/connections` is unauthenticated by design; keep it on an internal network if presence is sensitive

## 🤝 Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
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
