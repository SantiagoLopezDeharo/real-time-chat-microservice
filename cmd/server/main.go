package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"chat-microservice/internal/httpapi"
	"chat-microservice/internal/middleware"
	"chat-microservice/internal/repository"
	"chat-microservice/internal/service"
	"chat-microservice/internal/ws"

	"github.com/joho/godotenv"
	"golang.org/x/time/rate"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET environment variable not set")
	}

	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}

	mongoDB := os.Getenv("MONGO_DB")
	if mongoDB == "" {
		mongoDB = "chatdb"
	}

	mongoCollection := os.Getenv("MONGO_COLLECTION")
	if mongoCollection == "" {
		mongoCollection = "messages"
	}

	maxRetries := 5
	if retryStr := os.Getenv("RETRY_ATTEMPTS"); retryStr != "" {
		if parsed, err := strconv.Atoi(retryStr); err == nil && parsed > 0 {
			maxRetries = parsed
		}
	}

	rps := rate.Limit(5)
	if rpsStr := os.Getenv("RATE_LIMIT_RPS"); rpsStr != "" {
		if parsed, err := strconv.ParseFloat(rpsStr, 64); err == nil && parsed > 0 {
			rps = rate.Limit(parsed)
		}
	}

	burst := 10
	if burstStr := os.Getenv("RATE_LIMIT_BURST"); burstStr != "" {
		if parsed, err := strconv.Atoi(burstStr); err == nil && parsed > 0 {
			burst = parsed
		}
	}

	repo, err := repository.NewMongoRepository(mongoURI, mongoDB, mongoCollection)
	if err != nil {
		log.Fatalf("failed to connect to MongoDB: %v", err)
	}

	hub := ws.NewHub()
	svc := service.NewChatService(repo, hub, maxRetries)

	go hub.Run()

	h := httpapi.NewHandler(svc)

	authMiddleware := middleware.NewAuthMiddleware(jwtSecret)
	rateLimiter := middleware.NewRateLimiter(rps, burst)

	mux := http.NewServeMux()

	protectedAPI := http.NewServeMux()
	protectedAPI.HandleFunc("/api/messages", h.HandleSendMessage)
	protectedAPI.HandleFunc("/api/messages/get", h.HandleGetMessages)

	protectedWS := http.NewServeMux()
	protectedWS.HandleFunc("/ws", h.HandleWebsocket)

	mux.HandleFunc("/health", h.Health)
	mux.Handle("/api/", authMiddleware.Verify(rateLimiter.Middleware(protectedAPI)))
	mux.Handle("/ws", authMiddleware.Verify(protectedWS))
	mux.HandleFunc("/api/connections", h.HandleGetUserConnections)

	addr := ":8080"
	if v := os.Getenv("PORT"); v != "" {
		addr = ":" + v
	}

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("starting server on %s", addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
