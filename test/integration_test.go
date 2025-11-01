package test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"chat-microservice/internal/httpapi"
	"chat-microservice/internal/middleware"
	"chat-microservice/internal/repository"
	"chat-microservice/internal/service"
	"chat-microservice/internal/ws"
	"chat-microservice/pkg/models"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

const (
	mongoURITest    = "mongodb://localhost:27017"
	dbNameTest      = "chat_test"
	collectionTest  = "messages_test"
	jwtSecretTest   = "test-secret"
	numUsers        = 10
	messagesPerUser = 5
)

var (
	testServer *httptest.Server
	chatSvc    *service.ChatService
)

// SimulatedUser represents a user in our test environment
type SimulatedUser struct {
	ID             string
	Token          string
	Conn           *websocket.Conn
	Received       chan *models.Message
	sentMessages   map[string]bool // Key: message content
	mu             sync.RWMutex
	t              *testing.T
	wg             *sync.WaitGroup
	expectedToRecv int
}

func NewSimulatedUser(t *testing.T, id int, wg *sync.WaitGroup) *SimulatedUser {
	userID := fmt.Sprintf("user-%d", id)
	token, err := GenerateTestJWT(userID, jwtSecretTest)
	require.NoError(t, err)

	return &SimulatedUser{
		ID:           userID,
		Token:        token,
		Received:     make(chan *models.Message, messagesPerUser*numUsers),
		sentMessages: make(map[string]bool),
		t:            t,
		wg:           wg,
	}
}

func (u *SimulatedUser) Connect(serverURL string) {
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	header := http.Header{"Authorization": {"Bearer " + u.Token}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	require.NoError(u.t, err, "Failed to connect for user %s", u.ID)
	u.Conn = conn

	// Start listening for messages
	go u.listen()
}

func (u *SimulatedUser) listen() {
	defer u.Conn.Close()
	for {
		_, message, err := u.Conn.ReadMessage()
		if err != nil {
			// Check for clean close
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("User %s listener error: %v", u.ID, err)
			}
			return
		}

		var msg models.Message
		err = json.Unmarshal(message, &msg)
		require.NoError(u.t, err)

		// Don't count our own messages
		if msg.Sender == u.ID {
			continue
		}

		u.Received <- &msg
		u.wg.Done()
	}
}

func (u *SimulatedUser) SendMessage(participants []string, content string) {
	u.mu.Lock()
	u.sentMessages[content] = true
	u.mu.Unlock()

	url := testServer.URL + "/api/messages"
	payload := fmt.Sprintf(`{"participants": ["%s"], "content": "%s"}`, strings.Join(participants, `","`), content)

	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	require.NoError(u.t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+u.Token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(u.t, err)
	defer resp.Body.Close()

	assert.Equal(u.t, http.StatusAccepted, resp.StatusCode, "Failed to send message")
}

func (u *SimulatedUser) Close() {
	if u.Conn != nil {
		u.Conn.Close()
	}
}

// TestMain sets up and tears down the test server
func TestMain(m *testing.M) {
	// Clean DB before starting
	repo, err := repository.NewMongoRepository(mongoURITest, dbNameTest, collectionTest)
	if err != nil {
		log.Fatalf("Failed to connect to mongo for cleanup: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo.Collection().Drop(ctx)

	// Setup server
	hub := ws.NewHub()
	go hub.Run()
	chatSvc = service.NewChatService(repo, hub, 3)
	handler := httpapi.NewHandler(chatSvc)
	authMiddleware := middleware.NewAuthMiddleware(jwtSecretTest)

	router := http.NewServeMux()
	router.Handle("/ws", authMiddleware.Verify(http.HandlerFunc(handler.HandleWebsocket)))
	router.Handle("/api/messages", authMiddleware.Verify(http.HandlerFunc(handler.HandleSendMessage)))
	router.Handle("/api/messages/get", authMiddleware.Verify(http.HandlerFunc(handler.HandleGetMessages)))

	testServer = httptest.NewServer(router)
	defer testServer.Close()
	defer chatSvc.Stop()

	// Run tests
	code := m.Run()

	os.Exit(code)
}

func TestHighConcurrency(t *testing.T) {
	var wg sync.WaitGroup
	users := make([]*SimulatedUser, numUsers)

	// 1. Create and connect users
	for i := 0; i < numUsers; i++ {
		users[i] = NewSimulatedUser(t, i, &wg)
		users[i].Connect(testServer.URL)
		defer users[i].Close()
	}

	// Wait a moment for all connections to be established
	time.Sleep(100 * time.Millisecond)

	// 2. Define chat groups and expected message counts
	// In this test, each user sends a message to every other user individually.
	totalMessagesSent := 0
	for i := 0; i < numUsers; i++ {
		for j := 0; j < numUsers; j++ {
			if i == j {
				continue
			}
			// User i sends a message to user j. User j expects to receive it.
			users[j].expectedToRecv++
			totalMessagesSent++
		}
	}
	wg.Add(totalMessagesSent)

	// 3. Send messages concurrently
	for i := 0; i < numUsers; i++ {
		go func(sender *SimulatedUser) {
			for j := 0; j < numUsers; j++ {
				if sender.ID == users[j].ID {
					continue
				}
				recipient := users[j]
				participants := []string{sender.ID, recipient.ID}
				content := fmt.Sprintf("Message from %s to %s", sender.ID, recipient.ID)
				sender.SendMessage(participants, content)
			}
		}(users[i])
	}

	// 4. Wait for all messages to be received or timeout
	waitTimeout(&wg, 10*time.Second, t)

	// Wait for async database writes to complete
	time.Sleep(500 * time.Millisecond)

	// 5. Verify received messages
	for _, user := range users {
		close(user.Received)
		assert.Equal(t, user.expectedToRecv, len(user.Received), "User %s did not receive all expected messages", user.ID)

		// Check that received messages are correct
		for msg := range user.Received {
			// Ensure the user was a participant
			assert.Contains(t, msg.Participants, user.ID, "User %s received a message for a channel they are not in", user.ID)
			// Ensure the user was not the sender
			assert.NotEqual(t, user.ID, msg.Sender, "User %s received a message they sent themselves", user.ID)
		}
	}

	// 6. Verify persistence via REST API
	// Check a few channels to ensure history is correct
	for i := 0; i < 3; i++ {
		sender := users[i]
		recipient := users[numUsers-1-i]
		participants := []string{sender.ID, recipient.ID}
		sort.Strings(participants)

		url := fmt.Sprintf("%s/api/messages/get?participants=%s", testServer.URL, strings.Join(participants, ","))
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+sender.Token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		var messages []*models.Message
		err = json.NewDecoder(resp.Body).Decode(&messages)
		require.NoError(t, err)

		// In our test, two messages are exchanged between each pair (one from each user)
		assert.GreaterOrEqual(t, len(messages), 2, "Expected at least 2 messages retrieved for channel %v", participants)
	}

	log.Printf("Test completed successfully! Total users: %d, Messages sent: %d", numUsers, totalMessagesSent)
}

func TestGroupChat(t *testing.T) {
	var wg sync.WaitGroup
	numGroupUsers := 5
	users := make([]*SimulatedUser, numGroupUsers)

	// 1. Create and connect users
	for i := 0; i < numGroupUsers; i++ {
		users[i] = NewSimulatedUser(t, i, &wg)
		users[i].Connect(testServer.URL)
		defer users[i].Close()
	}

	time.Sleep(100 * time.Millisecond)

	// 2. Create a group with all users
	participants := make([]string, numGroupUsers)
	for i := 0; i < numGroupUsers; i++ {
		participants[i] = users[i].ID
	}

	// Each user sends one message to the group
	// Each user should receive (numGroupUsers - 1) messages (all except their own)
	for _, user := range users {
		user.expectedToRecv = numGroupUsers - 1
	}
	wg.Add(numGroupUsers * (numGroupUsers - 1)) // Total messages to be received

	// 3. Send messages
	for i, sender := range users {
		go func(s *SimulatedUser, idx int) {
			content := fmt.Sprintf("Group message from %s (#%d)", s.ID, idx)
			s.SendMessage(participants, content)
		}(sender, i)
	}

	// 4. Wait for all messages to be received
	waitTimeout(&wg, 10*time.Second, t)
	time.Sleep(500 * time.Millisecond) // Wait for DB writes

	// 5. Verify each user received the expected number of messages
	for _, user := range users {
		close(user.Received)
		receivedCount := 0
		for msg := range user.Received {
			receivedCount++
			// Verify sender exclusion
			assert.NotEqual(t, user.ID, msg.Sender, "User %s received their own message", user.ID)
			// Verify all participants are in the message
			assert.ElementsMatch(t, participants, msg.Participants, "Participants mismatch")
		}
		assert.Equal(t, user.expectedToRecv, receivedCount, "User %s did not receive expected messages", user.ID)
	}

	// 6. Verify persistence - all messages should be stored
	sort.Strings(participants)
	url := fmt.Sprintf("%s/api/messages/get?participants=%s", testServer.URL, strings.Join(participants, ","))
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+users[0].Token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var messages []*models.Message
	err = json.NewDecoder(resp.Body).Decode(&messages)
	require.NoError(t, err)

	assert.Equal(t, numGroupUsers, len(messages), "Expected %d messages in group chat", numGroupUsers)
	log.Printf("Group chat test completed successfully! Users: %d, Messages: %d", numGroupUsers, len(messages))
}

func TestPagination(t *testing.T) {
	var wg sync.WaitGroup
	user1 := NewSimulatedUser(t, 100, &wg)
	user2 := NewSimulatedUser(t, 101, &wg)

	user1.Connect(testServer.URL)
	user2.Connect(testServer.URL)
	defer user1.Close()
	defer user2.Close()

	time.Sleep(100 * time.Millisecond)

	// Send 25 messages
	totalMessages := 25
	participants := []string{user1.ID, user2.ID}

	user1.expectedToRecv = totalMessages
	wg.Add(totalMessages)

	for i := 0; i < totalMessages; i++ {
		content := fmt.Sprintf("Pagination test message %d", i)
		user2.SendMessage(participants, content)
		time.Sleep(10 * time.Millisecond) // Small delay to ensure order
	}

	waitTimeout(&wg, 10*time.Second, t)
	time.Sleep(500 * time.Millisecond) // Wait for DB writes

	close(user1.Received)
	receivedCount := 0
	for range user1.Received {
		receivedCount++
	}
	assert.Equal(t, totalMessages, receivedCount, "User1 did not receive all messages")

	// Test pagination
	sort.Strings(participants)
	participantsStr := strings.Join(participants, ",")

	// Page 0, size 10
	url := fmt.Sprintf("%s/api/messages/get?participants=%s&page=0&size=10", testServer.URL, participantsStr)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+user1.Token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var page1 []*models.Message
	err = json.NewDecoder(resp.Body).Decode(&page1)
	require.NoError(t, err)
	assert.Equal(t, 10, len(page1), "Expected 10 messages on page 0")

	// Page 1, size 10
	url = fmt.Sprintf("%s/api/messages/get?participants=%s&page=1&size=10", testServer.URL, participantsStr)
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+user1.Token)

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var page2 []*models.Message
	err = json.NewDecoder(resp.Body).Decode(&page2)
	require.NoError(t, err)
	assert.Equal(t, 10, len(page2), "Expected 10 messages on page 1")

	// Page 2, size 10 (should have remaining 5)
	url = fmt.Sprintf("%s/api/messages/get?participants=%s&page=2&size=10", testServer.URL, participantsStr)
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+user1.Token)

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var page3 []*models.Message
	err = json.NewDecoder(resp.Body).Decode(&page3)
	require.NoError(t, err)
	assert.Equal(t, 5, len(page3), "Expected 5 messages on page 2")

	// Verify no duplicate messages across pages
	allIDs := make(map[string]bool)
	for _, msg := range append(append(page1, page2...), page3...) {
		assert.False(t, allIDs[msg.Content], "Duplicate message found: %s", msg.Content)
		allIDs[msg.Content] = true
	}

	log.Printf("Pagination test completed successfully! Total messages: %d", len(allIDs))
}

func TestUnauthorizedAccess(t *testing.T) {
	var wg sync.WaitGroup
	user1 := NewSimulatedUser(t, 200, &wg)
	user2 := NewSimulatedUser(t, 201, &wg)
	user3 := NewSimulatedUser(t, 202, &wg)

	// User1 and User2 have a private conversation
	participants := []string{user1.ID, user2.ID}
	sort.Strings(participants)

	// Try to access messages as user3 (who is not a participant)
	url := fmt.Sprintf("%s/api/messages/get?participants=%s", testServer.URL, strings.Join(participants, ","))
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+user3.Token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var messages []*models.Message
	err = json.NewDecoder(resp.Body).Decode(&messages)
	require.NoError(t, err)

	// User3 should get an empty array (no access to this channel)
	assert.Empty(t, messages, "User3 should not have access to user1-user2 conversation")

	log.Printf("Unauthorized access test completed successfully!")
}

func TestSenderMustBeParticipant(t *testing.T) {
	var wg sync.WaitGroup
	user1 := NewSimulatedUser(t, 300, &wg)

	// Try to send a message to a channel where the sender is not a participant
	participants := []string{"user-999", "user-998"} // Neither is user1

	url := testServer.URL + "/api/messages"
	payload := fmt.Sprintf(`{"participants": ["%s"], "content": "Test message"}`, strings.Join(participants, `","`))

	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+user1.Token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should return 403 Forbidden
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "Expected forbidden when sender not in participants")

	log.Printf("Sender validation test completed successfully!")
}

func TestRateLimiting(t *testing.T) {
	rateLimitRPS := 2.0
	rateLimitBurst := 3

	repo, err := repository.NewMongoRepository(mongoURITest, dbNameTest, collectionTest+"_ratelimit")
	if err != nil {
		t.Skip("Skipping test: MongoDB not available")
	}

	hub := ws.NewHub()
	go hub.Run()
	svc := service.NewChatService(repo, hub, 3)
	defer svc.Stop()

	authMiddleware := middleware.NewAuthMiddleware(jwtSecretTest)
	rateLimiter := middleware.NewRateLimiter(rate.Limit(rateLimitRPS), rateLimitBurst)

	handler := httpapi.NewHandler(svc)

	router := http.NewServeMux()
	router.Handle("/api/messages", authMiddleware.Verify(rateLimiter.Middleware(http.HandlerFunc(handler.HandleSendMessage))))

	rateLimitTestServer := httptest.NewServer(router)
	defer rateLimitTestServer.Close()

	var wg sync.WaitGroup
	user := NewSimulatedUser(t, 999, &wg)

	rateLimitHit := false
	successCount := 0

	for i := 0; i < 10; i++ {
		url := rateLimitTestServer.URL + "/api/messages"
		payload := fmt.Sprintf(`{"participants": ["user-999"], "content": "rate limit test %d"}`, i)
		req, _ := http.NewRequest("POST", url, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+user.Token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		if resp.StatusCode == http.StatusTooManyRequests {
			rateLimitHit = true
			resp.Body.Close()
			break
		} else if resp.StatusCode == http.StatusAccepted {
			successCount++
		}
		resp.Body.Close()
	}

	assert.True(t, rateLimitHit, "Expected rate limit to be exceeded after %d successful requests", successCount)

	time.Sleep(2 * time.Second)

	url2 := rateLimitTestServer.URL + "/api/messages"
	payload2 := `{"participants": ["user-999"], "content": "rate limit test after recovery"}`
	req2, _ := http.NewRequest("POST", url2, strings.NewReader(payload2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+user.Token)

	resp2, err2 := http.DefaultClient.Do(req2)
	require.NoError(t, err2)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp2.StatusCode, "Request should succeed after waiting")

	log.Println("Rate limiting test completed successfully!")
}

func TestInvalidToken(t *testing.T) {
	var wg sync.WaitGroup
	user := NewSimulatedUser(t, 888, &wg)

	// Generate a token with a wrong secret
	invalidToken, err := GenerateTestJWT(user.ID, "wrong-secret")
	require.NoError(t, err)

	url := testServer.URL + "/api/messages"
	payload := `{"participants": ["user-888"], "content": "invalid token test"}`
	req, _ := http.NewRequest("POST", url, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+invalidToken)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Expected unauthorized with invalid token")

	log.Println("Invalid token test completed successfully!")
}

// waitTimeout waits for the waitgroup for the specified duration.
// Returns true if waiting timed out.
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration, t *testing.T) {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		// Completed successfully
	case <-time.After(timeout):
		t.Fatal("Test timed out waiting for messages to be received")
	}
}
