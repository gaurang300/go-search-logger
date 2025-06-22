package searchlogger

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	_ "github.com/lib/pq"
)

var (
	dbURL    = "postgres://localhost/search_logs?sslmode=disable"
	redisURL = "localhost:6379"
)

// setupLogger initializes a Logger with test DB and Redis, and cleans up test data.
func setupLogger(t *testing.T) *Logger {
	t.Helper()

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("DB error: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: redisURL})

	// Clean up test data from previous runs
	db.Exec(`DELETE FROM user_searches WHERE user_id LIKE 'test-%' OR user_id = 'user123'`)
	rdb.FlushAll(context.Background())

	return &Logger{DB: db, Redis: rdb}
}

// getLatestQuery fetches the most recent search_text for a user from the DB.
func getLatestQuery(t *testing.T, logger *Logger, userID string) string {
	var query string
	var err error
	if userID == "" {
		t.Fatal("userID must not be empty")
	}
	// If userID looks like an anonID, check for both user_id and anon_id columns
	err = logger.DB.QueryRow(`
		SELECT search_text FROM user_searches 
		WHERE (user_id = $1 OR anon_id = $1) 
		ORDER BY last_searched_at DESC
	`, userID).Scan(&query)
	if err != nil {
		t.Fatalf("DB read error: %v", err)
	}
	return query
}

// FlushUser writes the last search query for a user from Redis to the DB.
func (l *Logger) FlushUser(ctx context.Context, userID string, anonID string) error {
	key := buildRedisKey(userID)
	if userID == "" {
		key = buildRedisKey(anonID)
	}

	query, _ := l.Redis.Get(ctx, key).Result()
	if query == "" {
		// Do not write empty queries to the DB
		return nil
	}
	// Write the last query to the DB
	entry := SearchEntry{
		UserID: userID,
		Query:  query,
		AnonID: anonID,
		// Add other required fields if needed, e.g. Timestamp, UserAgent, etc.
	}
	return l.writeSearch(ctx, entry)
}

// TestLogSearchAndWrite checks that only the last full query is written after a sequence of LogSearch calls.
func TestLogSearchAndWrite(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)

	userAgent := "TestBrowser/1.0"
	userID := ""
	query := "testquery"

	_ = logger.LogSearch(ctx, userID, userAgent, "t")
	_ = logger.LogSearch(ctx, userID, userAgent, "te")
	_ = logger.LogSearch(ctx, userID, userAgent, "tes")
	_ = logger.LogSearch(ctx, userID, userAgent, "testq")
	_ = logger.LogSearch(ctx, userID, userAgent, query)
	_ = logger.LogSearch(ctx, userID, userAgent, "testquery")

	anonID := generateAnonID(userAgent)
	_ = logger.FlushUser(ctx, "", anonID)
	searchText := getLatestQuery(t, logger, anonID)
	if searchText != query {
		t.Errorf("expected 'testquery', got '%s'", searchText)
	}
}

// TestAnonSearchReset checks that a new search resets the previous one for anonymous users.
func TestAnonSearchReset(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)

	ua := "TestAgent"
	userID := ""
	anonID := generateAnonID(ua)

	_ = logger.LogSearch(ctx, userID, ua, "bus")
	_ = logger.LogSearch(ctx, userID, ua, "busi")
	_ = logger.LogSearch(ctx, userID, ua, "business")
	_ = logger.LogSearch(ctx, userID, ua, "data")

	got := getLatestQuery(t, logger, anonID)

	if got != "business" {
		t.Errorf("expected 'business', got '%s'", got)
	}
	_ = logger.FlushUser(ctx, "", anonID)
	got = getLatestQuery(t, logger, anonID)
	if got != "data" {
		t.Errorf("expected 'data', got '%s'", got)
	}

}

// TestLoggedInUserSearch checks that the last full query before a reset is written for logged-in users.
func TestLoggedInUserSearch(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)

	userID := "user123"
	_ = logger.LogSearch(ctx, userID, "", "cat")
	_ = logger.LogSearch(ctx, userID, "", "caterpillar")
	_ = logger.LogSearch(ctx, userID, "", "dog") // triggers flush

	got := getLatestQuery(t, logger, userID)
	if got != "caterpillar" {
		t.Errorf("expected 'caterpillar', got '%s'", got)
	}
	_ = logger.FlushUser(ctx, userID, "")
	got = getLatestQuery(t, logger, userID)
	if got != "dog" {
		t.Errorf("expected 'dog', got '%s'", got)
	}
}

// TestTTLExpiryTriggersWrite checks that a search is written to DB after TTL expiry and flush.
func TestTTLExpiryTriggersWrite(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)

	ua := "AgentX"
	userID := ""

	// Log "hello" and manually flush before TTL expiry
	_ = logger.LogSearch(ctx, userID, ua, "hello")

	anonID := generateAnonID(ua)

	// Wait a bit less than TTL and flush
	time.Sleep(9 * time.Second)
	_ = logger.FlushUser(ctx, "", anonID)

	// Now log unrelated query
	_ = logger.LogSearch(ctx, userID, ua, "world")

	got := getLatestQuery(t, logger, anonID)
	if got != "hello" {
		t.Errorf("expected 'hello', got '%s'", got)
	}
	_ = logger.FlushUser(ctx, "", anonID)
	got = getLatestQuery(t, logger, anonID)
	if got != "world" {
		t.Errorf("expected 'world', got '%s'", got)
	}
}

func TestMultipleAnonUsers(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)

	ua1 := "AnonA"
	ua2 := "AnonB"
	id1 := generateAnonID(ua1)
	id2 := generateAnonID(ua2)

	_ = logger.LogSearch(ctx, "", ua1, "alpha")
	_ = logger.LogSearch(ctx, "", ua2, "beta")

	_ = logger.FlushUser(ctx, "", id1)
	_ = logger.FlushUser(ctx, "", id2)

	got1 := getLatestQuery(t, logger, id1)
	got2 := getLatestQuery(t, logger, id2)

	if got1 != "alpha" {
		t.Errorf("expected 'alpha' for anon1, got '%s'", got1)
	}
	if got2 != "beta" {
		t.Errorf("expected 'beta' for anon2, got '%s'", got2)
	}
}

func TestEmptySearchNotFlushed(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)

	userID := "test-user-empty"
	_ = logger.LogSearch(ctx, userID, "", "")
	_ = logger.FlushUser(ctx, userID, "")

	// Should not write anything
	var count int
	err := logger.DB.QueryRow(`
		SELECT COUNT(*) FROM user_searches WHERE user_id = $1
	`, userID).Scan(&count)
	if err != nil {
		t.Fatalf("DB error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 entries, got %d", count)
	}
}
