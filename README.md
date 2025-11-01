# Real-time Chat Microservice (minimal scaffold)

This repository is a Golang Plug And Play micro-service to managing real time chat system with one on one clients and groups of them.

## Tech Stack

| Golang | MongoDB | Docker |
| ------ | ------ | ---------- |
| <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/go.png"> | <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/mongodb.png"> | <img height="60" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/docker.png"> |


Structure highlights:
- `cmd/server` - application entrypoint
- `internal/httpapi` - HTTP handlers (REST & WebSocket upgrade)
- `internal/ws` - websocket hub and client management with channel support
- `internal/service` - business logic that wires hub and repository
- `internal/repository` - MongoDB repository implementation
- `internal/middleware` - JWT authentication and authorization
- `pkg/models` - shared domain models

Prerequisites:

- Go 1.22+ (for local development)
- Docker and Docker Compose (for containerized deployment)
- MongoDB instance running (local or remote, or use Docker Compose)

Environment Variables:

```bash
MONGO_URI=mongodb://localhost:27017
MONGO_DB=chatdb
MONGO_COLLECTION=messages
RETRY_ATTEMPTS=5
PORT=8080
JWT_SECRET=your-jwt-secret
```

- `RETRY_ATTEMPTS`: Maximum number of retry attempts for message persistence (default: 5)

## Running with Docker Compose (Recommended)

1. Make sure Docker and Docker Compose are installed

2. Configure your `.env` file with desired values

3. Start the services:

```bash
docker-compose up -d
```

4. View logs:

```bash
docker-compose logs -f chat-api
```

5. Stop the services:

```bash
docker-compose down
```

6. Stop and remove volumes (clears database):

```bash
docker-compose down -v
```

## Running Locally

1. Start MongoDB:

```bash
docker run -d -p 27017:27017 --name mongodb mongo:latest
```

2. Build and run:

```bash
export $(cat .env | xargs)
go run ./cmd/server
```

## Endpoints:
- `GET /health` - health check (no auth required)
- `GET /ws?channel=<channel_id>` - WebSocket endpoint (requires JWT, user must have access to channel)
- `POST /api/messages/<channel_id>` - send message via REST (requires JWT, user must have access to channel)
- `GET /api/messages/<channel_id>` - get messages from channel (requires JWT, filtered by access rules)
- `GET /api/messages/counts` - get connected client counts for multiple channels (no auth required)

JWT Authentication:

All protected endpoints require a JWT token in the Authorization header:
```
Authorization: Bearer <jwt_token>
```

The JWT payload must contain:
- `id` (string): user identifier, used as message sender
- `groups` (array of strings): group identifiers the user belongs to

Channel Access Rules:
- A user can access a channel if their `id` matches the channel ID
- OR if any of their `groups` contains the channel ID

Message Retrieval Rules (GET /api/messages/<channel_id>):
- If user's `id` or any `groups` match the `channel_id`: returns ALL messages from that channel
- Otherwise: returns only messages where the user is the sender OR where the channel matches their ID/groups

Example JWT payload:
```json
{
  "id": "user123",
  "groups": ["group1", "group2"],
  "other": "fields are ignored"
}
```

Message Format:
```json
{
  "content": "message text"
}
```

The `sender` field is automatically set from the JWT `id` claim.

Client Count Request:
```json
{
  "channels": ["channel1", "channel2", "channel3"]
}
```

Client Count Response:
```json
{
  "channel1": 5,
  "channel2": 0,
  "channel3": 12
}
```

Notes:
- MongoDB is used for persistent message storage
- MongoDB data is persisted in Docker volumes (`mongodb_data` and `mongodb_config`)
- Messages are broadcast immediately via WebSocket for minimal latency
- Message persistence happens asynchronously in a separate goroutine to avoid blocking broadcasts
- Failed persistence operations are retried up to the configured number of attempts with exponential backoff
- Messages are stored with timestamps and indexed by channel
- WebSocket messages are JSON matching the `models.Message` structure
- JWT verification is limited to parsing; signature validation should be handled by an upstream service
- The service supports horizontal scaling as long as all instances connect to the same MongoDB instance

## Docker Services

The Docker Compose setup includes:
- **chat-api**: The Go microservice running on port 8080 (configurable)
- **mongodb**: MongoDB 7.0 instance with persistent volumes
- **chat-network**: Bridge network for service communication
- **mongodb_data**: Volume for MongoDB data persistence
- **mongodb_config**: Volume for MongoDB configuration
