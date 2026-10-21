package repository

import (
	"context"
	"log/slog"

	"chat-microservice/pkg/models"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// newestFirst orders a channel's history; _id breaks ties between messages
// created in the same millisecond.
var newestFirst = bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}

type MongoRepository struct {
	client     *mongo.Client
	collection *mongo.Collection
}

func NewMongoRepository(ctx context.Context, uri, database, collection string) (*MongoRepository, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}

	coll := client.Database(database).Collection(collection)
	index := mongo.IndexModel{Keys: append(bson.D{{Key: "participants", Value: 1}}, newestFirst...)}
	if _, err := coll.Indexes().CreateOne(ctx, index); err != nil {
		slog.Warn("failed to create history index", "error", err)
	}
	return &MongoRepository{client: client, collection: coll}, nil
}

func (m *MongoRepository) Save(ctx context.Context, msg *models.Message) error {
	_, err := m.collection.InsertOne(ctx, msg)
	return err
}

// History returns one page of a channel's messages, newest first. Participants
// must already be normalized with models.NormalizeParticipants.
func (m *MongoRepository) History(ctx context.Context, participants []string, page, size int) ([]*models.Message, error) {
	opts := options.Find().
		SetSort(newestFirst).
		SetSkip(int64(page) * int64(size)).
		SetLimit(int64(size))

	cursor, err := m.collection.Find(ctx, bson.M{"participants": participants}, opts)
	if err != nil {
		return nil, err
	}
	messages := []*models.Message{}
	if err := cursor.All(ctx, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (m *MongoRepository) Close(ctx context.Context) error {
	return m.client.Disconnect(ctx)
}
