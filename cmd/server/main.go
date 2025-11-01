package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"chat-microservice/internal/httpapi"
	"chat-microservice/internal/middleware"
	"chat-microservice/internal/repository"
	"chat-microservice/internal/service"
	"chat-microservice/internal/ws"
)

func main() {
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

	repo, err := repository.NewMongoRepository(mongoURI, mongoDB, mongoCollection)
	if err != nil {
		log.Fatalf("failed to connect to MongoDB: %v", err)
	}

	hub := ws.NewHub()
	svc := service.NewChatService(repo, hub, maxRetries)

	go hub.Run()

	h := httpapi.NewHandler(svc)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.Health)
	mux.HandleFunc("/ws", middleware.JWTAuth(h.HandleWebsocket))
	mux.HandleFunc("/api/messages/counts", h.HandleGetClientCounts)
	mux.HandleFunc("/api/messages/", middleware.JWTAuth(h.HandleMessages))

	addr := ":8080"
	if v := os.Getenv("PORT"); v != "" {
		addr = ":" + v
	}

	log.Printf("starting server on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
