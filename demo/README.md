# Chat Demo Client

An interactive Node.js CLI for trying the chat microservice by hand. It mints a JWT for any user ID you type and wraps every endpoint of the service in a menu.

## Requirements

- Node.js 18+
- The chat microservice running locally (see the [main README](../README.md#quick-start))

The demo signs its tokens with the hard-coded secret `your-jwt-secret` ([`jwt.js`](jwt.js)), so the server must run with that secret or every authenticated call fails with `401`:

```bash
export JWT_SECRET=your-jwt-secret
docker compose up -d          # or: go run ./cmd/server
```

## Install and run

```bash
cd demo
npm install
npm start                     # same as: node index.js
```

By default the client talks to `http://localhost:8080` and `ws://localhost:8080`. Override them with:

```bash
export SERVER_HTTP=http://localhost:8080
export SERVER_WS=ws://localhost:8080
```

## Usage

At start the client asks for your user ID and generates an HS256 token with the claims `id`, `iat` and `exp` (valid for 24 hours). It then shows this menu:

| Option | What it does |
|--------|--------------|
| 1. Connect to WebSocket | Opens `GET /ws` with the token and prints every message delivered to you, from any conversation. The terminal stays in listening mode; `Ctrl+C` closes the socket and exits the client |
| 2. Send message | Asks for the participants (comma-separated, including yourself) and the content, then calls `POST /api/messages`. The client refuses locally if your ID is not in the list |
| 3. Get messages | Asks for the participants, a page (default `0`) and a size (default `50`, the server caps it at `100`), then calls `GET /api/messages/get` and prints the page, newest first |
| 4. Get user connection counts | Asks for user IDs and calls `POST /api/connections` (no token sent) |
| 5. Health check | Calls `GET /health` and prints the status and server time |
| 6. Exit | Quits |

Server errors are printed with the server's plain-text body, for example `✗ forbidden: user must be part of participants`.

## Example: two users

Since option 1 keeps the terminal listening, use one terminal per listener and another to send.

Terminal 1, listen as Alice:

```
Enter your user ID: alice
Select an option: 1
✓ Connected to WebSocket
```

Terminal 2, send as Bob:

```
Enter your user ID: bob
Select an option: 2
Enter participant user IDs (comma-separated, including yourself): alice, bob
Enter message: Hi Alice!
✓ Message sent successfully
```

Terminal 1 prints:

```
[10:30:45] Channel [alice, bob]
  bob: Hi Alice!
```

For a group, list more participants (`alice, bob, carol`): every listening recipient gets the message. The sender never receives their own message over the WebSocket, so a listener running as `bob` would print nothing for it.

## Troubleshooting

| Symptom | Cause |
|---------|-------|
| `ECONNREFUSED` | The server is not running, or `SERVER_HTTP` / `SERVER_WS` point elsewhere |
| `invalid token`, or `Failed to connect: Unexpected server response: 401` | The server's `JWT_SECRET` is not `your-jwt-secret`, or the user ID was left empty |
| `forbidden: user must be part of participants` | You asked for the history of a conversation you are not part of |
| `Too Many Requests` | Per-user rate limit (default 5 requests/s, burst 10) |

The demo's secret and self-issued tokens are for local testing only; in production, tokens come from your authentication service.
