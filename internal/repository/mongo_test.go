package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"chat-microservice/pkg/models"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// mongoURI points at MONGO_TEST_URI or a throwaway mongo:7.0 container started
// for this package; tests are skipped when neither is available.
var mongoURI string

func TestMain(m *testing.M) {
	mongoURI = os.Getenv("MONGO_TEST_URI")
	var cleanup func()
	if mongoURI == "" {
		mongoURI, cleanup = startMongo()
	}
	code := m.Run()
	if cleanup != nil {
		cleanup()
	}
	os.Exit(code)
}

func startMongo() (string, func()) {
	out, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::27017", "mongo:7.0").Output()
	if err != nil {
		return "", nil
	}
	id := strings.TrimSpace(string(out))
	cleanup := func() { _ = exec.Command("docker", "rm", "-f", id).Run() }
	port, err := exec.Command("docker", "port", id, "27017/tcp").Output()
	if err != nil {
		cleanup()
		return "", nil
	}
	addr := strings.TrimSpace(strings.Split(string(port), "\n")[0])
	return "mongodb://" + addr, cleanup
}

func newRepo(t testing.TB) *MongoRepository {
	t.Helper()
	if mongoURI == "" {
		t.Skip("no MongoDB available: set MONGO_TEST_URI or install Docker")
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var repo *MongoRepository
	var err error
	for range 30 {
		if repo, err = NewMongoRepository(ctx, mongoURI, "test_"+hex.EncodeToString(suffix), "messages"); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.collection.Database().Drop(context.Background())
		_ = repo.Close(context.Background())
	})
	return repo
}

func message(participants []string, content string, at time.Time) *models.Message {
	return &models.Message{ID: bson.NewObjectID(), Sender: participants[0], Content: content, CreatedAt: at, Participants: participants}
}

func save(t *testing.T, repo *MongoRepository, msgs ...*models.Message) {
	t.Helper()
	for _, m := range msgs {
		if err := repo.SaveBatch(context.Background(), []*models.Message{m}); err != nil {
			t.Fatalf("SaveBatch: %v", err)
		}
	}
}

func contents(msgs []*models.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func TestSaveAndHistoryRoundTrip(t *testing.T) {
	repo := newRepo(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	want := message([]string{"alice", "bob"}, "hello 日本語 🚀", at)
	save(t, repo, want)

	got, err := repo.History(context.Background(), []string{"alice", "bob"}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("History = %d messages", len(got))
	}
	m := got[0]
	if m.ID != want.ID || m.Sender != want.Sender || m.Content != want.Content || !m.CreatedAt.Equal(want.CreatedAt) || !slices.Equal(m.Participants, want.Participants) {
		t.Errorf("round trip = %+v, want %+v", m, want)
	}
}

func TestHistoryIsNewestFirstWithStableTies(t *testing.T) {
	repo := newRepo(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	channel := []string{"alice", "bob"}
	save(t, repo,
		message(channel, "oldest", base),
		message(channel, "tie-1", base.Add(time.Second)),
		message(channel, "tie-2", base.Add(time.Second)),
		message(channel, "newest", base.Add(2*time.Second)),
	)
	got, err := repo.History(context.Background(), channel, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"newest", "tie-2", "tie-1", "oldest"}; !slices.Equal(contents(got), want) {
		t.Errorf("order = %q, want %q", contents(got), want)
	}
}

func TestHistoryPagination(t *testing.T) {
	repo := newRepo(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	channel := []string{"alice", "bob"}
	for i := range 25 {
		save(t, repo, message(channel, fmt.Sprintf("m%02d", i), base.Add(time.Duration(i)*time.Millisecond)))
	}

	var seen []string
	for page := 0; ; page++ {
		got, err := repo.History(context.Background(), channel, page, 10)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("History returned nil instead of an empty slice")
		}
		if len(got) == 0 {
			break
		}
		if len(got) > 10 {
			t.Fatalf("page %d has %d messages", page, len(got))
		}
		seen = append(seen, contents(got)...)
	}
	if len(seen) != 25 || seen[0] != "m24" || seen[24] != "m00" {
		t.Fatalf("paged through %q", seen)
	}
	if got, _ := repo.History(context.Background(), channel, 10_000, 100); len(got) != 0 {
		t.Errorf("far page returned %d messages", len(got))
	}
}

func TestHistoryMatchesTheExactChannel(t *testing.T) {
	repo := newRepo(t)
	at := time.Now().UTC()
	save(t, repo,
		message([]string{"alice", "bob"}, "pair", at),
		message([]string{"alice", "bob", "carol"}, "group", at),
		message([]string{"alice"}, "self", at),
		message([]string{"bob", "carol"}, "other", at),
	)
	tests := map[string][]string{
		"pair":  {"alice", "bob"},
		"group": {"alice", "bob", "carol"},
		"self":  {"alice"},
	}
	for want, channel := range tests {
		got, err := repo.History(context.Background(), channel, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(contents(got), []string{want}) {
			t.Errorf("channel %q returned %q", channel, contents(got))
		}
	}
	if got, _ := repo.History(context.Background(), []string{"nobody"}, 0, 10); len(got) != 0 {
		t.Errorf("unknown channel returned %q", contents(got))
	}
}

func TestSaveBatchInsertsEveryMessage(t *testing.T) {
	repo := newRepo(t)
	channel := []string{"alice", "bob"}
	base := time.Now().UTC().Truncate(time.Millisecond)
	batch := make([]*models.Message, 300)
	for i := range batch {
		batch[i] = message(channel, fmt.Sprintf("m%03d", i), base.Add(time.Duration(i)*time.Millisecond))
	}
	if err := repo.SaveBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.History(context.Background(), channel, 0, 100)
	if len(got) != 100 || got[0].Content != "m299" {
		t.Fatalf("history after batch = %d messages starting %q", len(got), got[0].Content)
	}
}

func TestSaveBatchRetriesAreIdempotent(t *testing.T) {
	repo := newRepo(t)
	channel := []string{"alice", "bob"}
	first := message(channel, "first", time.Now())
	save(t, repo, first)

	retried := []*models.Message{first, message(channel, "second", time.Now()), first}
	if err := repo.SaveBatch(context.Background(), retried); err != nil {
		t.Fatalf("retrying a partially applied batch = %v, want nil", err)
	}
	got, _ := repo.History(context.Background(), channel, 0, 10)
	if len(got) != 2 {
		t.Fatalf("stored %d messages, want 2 without duplicates", len(got))
	}
}

func TestSaveBatchReportsRealFailures(t *testing.T) {
	repo := newRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repo.SaveBatch(ctx, []*models.Message{message([]string{"alice"}, "x", time.Now())}); err == nil {
		t.Fatal("cancelled insert reported success")
	}
	if onlyDuplicates(nil) || onlyDuplicates(errors.New("boom")) || onlyDuplicates(mongo.BulkWriteException{}) {
		t.Fatal("non-duplicate errors classified as duplicates")
	}
}

func TestHistoryIndexCoversQueryAndSort(t *testing.T) {
	repo := newRepo(t)
	specs, err := repo.collection.Indexes().ListSpecifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
	}
	if !slices.Contains(names, "participants_1_created_at_-1__id_-1") {
		t.Fatalf("indexes = %q", names)
	}
}

func TestOperationsHonourContext(t *testing.T) {
	repo := newRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repo.SaveBatch(ctx, []*models.Message{message([]string{"a"}, "x", time.Now())}); !errors.Is(err, context.Canceled) {
		t.Errorf("SaveBatch with cancelled context = %v", err)
	}
	if _, err := repo.History(ctx, []string{"a"}, 0, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("History with cancelled context = %v", err)
	}
}

func TestNewMongoRepositoryFailures(t *testing.T) {
	if _, err := NewMongoRepository(context.Background(), "not-a-uri", "db", "c"); err == nil {
		t.Error("invalid URI accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := NewMongoRepository(ctx, "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=300", "db", "c"); err == nil {
		t.Error("unreachable server accepted")
	}
}

func BenchmarkSaveBatch(b *testing.B) {
	for _, size := range []int{1, 64} {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			repo := newRepo(b)
			channel := []string{"alice", "bob"}
			batch := make([]*models.Message, size)
			b.ReportAllocs()
			for b.Loop() {
				for i := range batch {
					batch[i] = message(channel, "hello", time.Now())
				}
				_ = repo.SaveBatch(context.Background(), batch)
			}
			b.ReportMetric(float64(b.N*size)/b.Elapsed().Seconds(), "msgs/s")
		})
	}
}

func BenchmarkHistory(b *testing.B) {
	repo := newRepo(b)
	channel := []string{"alice", "bob"}
	batch := make([]*models.Message, 500)
	for i := range batch {
		batch[i] = message(channel, fmt.Sprint(i), time.Now())
	}
	_ = repo.SaveBatch(context.Background(), batch)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = repo.History(context.Background(), channel, 0, 50)
	}
}
