package repository

import (
	"context"
	"log"
	"sort"
	"time"

	"chat-microservice/pkg/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Repository interface {
	Save(*models.Message) error
	SaveAsync(*models.Message, int)
	List() []*models.Message
	GetMessagesByParticipants(participants []string) ([]*models.Message, error)
	GetMessagesByParticipantsWithPagination(participants []string, page int, size int) ([]*models.Message, error)
}

type MongoRepository struct {
	collection *mongo.Collection
}

func NewMongoRepository(mongoURI, database, collection string) (*MongoRepository, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		return nil, err
	}

	if err := client.Ping(ctx, nil); err != nil {
		return nil, err
	}

	coll := client.Database(database).Collection(collection)

	// Create index on participants array for efficient querying
	indexModel := mongo.IndexModel{
		Keys: bson.D{{Key: "participants", Value: 1}},
	}
	_, err = coll.Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		log.Printf("warning: failed to create index on participants: %v", err)
	}

	return &MongoRepository{collection: coll}, nil
}

func (m *MongoRepository) Save(msg *models.Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ensure participants are sorted before saving
	sort.Strings(msg.Participants)

	_, err := m.collection.InsertOne(ctx, msg)
	return err
}

func (m *MongoRepository) List() []*models.Message {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := m.collection.Find(ctx, bson.M{})
	if err != nil {
		return []*models.Message{}
	}
	defer cursor.Close(ctx)

	var messages []*models.Message
	if err := cursor.All(ctx, &messages); err != nil {
		return []*models.Message{}
	}

	return messages
}

// GetMessagesByParticipants retrieves all messages for a channel identified by its participants
// The participants array should be sorted before calling this method
func (m *MongoRepository) GetMessagesByParticipants(participants []string) ([]*models.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ensure participants are sorted
	sorted := make([]string, len(participants))
	copy(sorted, participants)
	sort.Strings(sorted)

	// Query for exact match of participants array (order-independent due to sorting)
	filter := bson.M{"participants": sorted}

	cursor, err := m.collection.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var messages []*models.Message
	if err := cursor.All(ctx, &messages); err != nil {
		return nil, err
	}

	return messages, nil
}

// GetMessagesByParticipantsWithPagination retrieves messages with pagination
// Messages are sorted by created_at descending (latest first)
// page: page number (0-indexed)
// size: number of messages per page
// offset = size * page
func (m *MongoRepository) GetMessagesByParticipantsWithPagination(participants []string, page int, size int) ([]*models.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ensure participants are sorted
	sorted := make([]string, len(participants))
	copy(sorted, participants)
	sort.Strings(sorted)

	// Calculate offset
	offset := int64(size * page)

	// Query for exact match of participants array (order-independent due to sorting)
	filter := bson.M{"participants": sorted}

	// Sort by created_at descending (latest first), skip offset, limit by size
	findOptions := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(offset).
		SetLimit(int64(size))

	cursor, err := m.collection.Find(ctx, filter, findOptions)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var messages []*models.Message
	if err := cursor.All(ctx, &messages); err != nil {
		return nil, err
	}

	return messages, nil
}

func (m *MongoRepository) SaveAsync(msg *models.Message, maxRetries int) {
	go func() {
		var lastErr error
		for attempt := 1; attempt <= maxRetries; attempt++ {
			err := m.Save(msg)
			if err == nil {
				return
			}
			lastErr = err
			log.Printf("failed to save message (attempt %d/%d): %v", attempt, maxRetries, err)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
			}
		}
		log.Printf("failed to save message after %d attempts: %v", maxRetries, lastErr)
	}()
}
