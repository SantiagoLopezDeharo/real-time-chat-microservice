package repository

import (
	"context"
	"log"
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
	GetMessagesByChannel(channelID string) ([]*models.Message, error)
	GetMessagesForUser(userID string, groups []string) ([]*models.Message, error)
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

	return &MongoRepository{collection: coll}, nil
}

func (m *MongoRepository) Save(msg *models.Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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

func (m *MongoRepository) GetMessagesByChannel(channelID string) ([]*models.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"channel_id": channelID}
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

func (m *MongoRepository) GetMessagesForUser(userID string, groups []string) ([]*models.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	allIDs := append([]string{userID}, groups...)

	filter := bson.M{
		"$or": []bson.M{
			{"sender": userID},
			{"channel_id": bson.M{"$in": allIDs}},
		},
	}

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
