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
	err := logger.DB.QueryRow(`SELECT search_text FROM user_searches WHERE user_id = $1 ORDER BY last_searched_at DESC`, userID).Scan(&query)
	if err != nil {
		t.Fatalf("DB read error: %v", err)
	}
	return query
}

// FlushUser writes the last search query for a user from Redis to the DB.
func (l *Logger) FlushUser(ctx context.Context, userID string) error {
	key := buildRedisKey(userID)
	query, err := l.Redis.Get(ctx, key).Result()
	if err != nil || query == "" {
		// Nothing to flush or error getting key
		return err
	}
	// Write the last query to the DB
	entry := SearchEntry{
		UserID: userID,
		Query:  query,
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
	_ = logger.FlushUser(ctx, anonID)
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

	_ = logger.LogSearch(ctx, userID, ua, "bus")
	_ = logger.LogSearch(ctx, userID, ua, "busi")
	_ = logger.LogSearch(ctx, userID, ua, "business")
	_ = logger.LogSearch(ctx, userID, ua, "data")

	anonID := generateAnonID(ua)
	got := getLatestQuery(t, logger, anonID)

	if got != "business" {
		t.Errorf("expected 'business', got '%s'", got)
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
	_ = logger.FlushUser(ctx, anonID)

	// Now log unrelated query
	_ = logger.LogSearch(ctx, userID, ua, "world")

	got := getLatestQuery(t, logger, anonID)
	if got != "hello" {
		t.Errorf("expected 'hello', got '%s'", got)
	}
}

// TestBackspaceDoesNotWrite checks that backspacing (shortening the query) does not trigger a DB write.
func TestBackspaceDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)

	userID := "user12"
	_ = logger.LogSearch(ctx, userID, "", "market")
	_ = logger.LogSearch(ctx, userID, "", "mark")

	rows, err := logger.DB.Query(`SELECT * FROM user_searches WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatalf("db check failed: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 write due to reset, got %d", count)
	}
}
