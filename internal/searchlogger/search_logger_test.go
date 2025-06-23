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

	// Enable Redis keyspace notifications for expired events
	err := logger.Redis.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err()
	if err != nil {
		t.Fatalf("Failed to enable keyspace notifications: %v", err)
	}

	// Start listener in background
	done := make(chan struct{})
	go func() {
		logger.StartKeyspaceListener(ctx)
		close(done)
	}()

	userAgent := "TestBrowser/1.0"
	userID := ""
	query := "testquery"

	_ = logger.LogSearch(ctx, userID, userAgent, "t")
	_ = logger.LogSearch(ctx, userID, userAgent, "te")
	_ = logger.LogSearch(ctx, userID, userAgent, "tes")
	_ = logger.LogSearch(ctx, userID, userAgent, "testq")
	_ = logger.LogSearch(ctx, userID, userAgent, query)

	anonID := generateAnonID(userAgent)
	// _ = logger.FlushUser(ctx, "", anonID)
	time.Sleep(11 * time.Second) // Wait for TTL expiry
	searchText := getLatestQuery(t, logger, anonID)
	if searchText != query {
		t.Errorf("expected 'testquery', got '%s'", searchText)
	}
}

// TestAnonSearchReset checks that a new search resets the previous one for anonymous users.
func TestAnonSearchReset(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	// Start listener in background
	done := make(chan struct{})
	go func() {
		logger.StartKeyspaceListener(ctx)
		close(done)
	}()

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
	time.Sleep(11 * time.Second) // Wait for TTL expiry
	got = getLatestQuery(t, logger, anonID)
	if got != "data" {
		t.Errorf("expected 'data', got '%s'", got)
	}
}

// TestLoggedInUserSearch checks that the last full query before a reset is written for logged-in users.
func TestLoggedInUserSearch(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	// Start listener in background
	done := make(chan struct{})
	go func() {
		logger.StartKeyspaceListener(ctx)
		close(done)
	}()
	userID := "user123"
	_ = logger.LogSearch(ctx, userID, "", "cat")
	_ = logger.LogSearch(ctx, userID, "", "caterpillar")
	_ = logger.LogSearch(ctx, userID, "", "dog") // triggers flush

	got := getLatestQuery(t, logger, userID)
	if got != "caterpillar" {
		t.Errorf("expected 'caterpillar', got '%s'", got)
	}
	time.Sleep(11 * time.Second) // Wait for TTL expiry
	got = getLatestQuery(t, logger, userID)
	if got != "dog" {
		t.Errorf("expected 'dog', got '%s'", got)
	}
}

// TestTTLExpiryTriggersWrite checks that a search is written to DB after TTL expiry and flush.
func TestTTLExpiryTriggersWrite(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	// Start listener in background
	done := make(chan struct{})
	go func() {
		logger.StartKeyspaceListener(ctx)
		close(done)
	}()
	ua := "AgentX"
	userID := ""

	// Log "hello" and manually flush before TTL expiry
	_ = logger.LogSearch(ctx, userID, ua, "hello")

	anonID := generateAnonID(ua)

	// Wait a bit less than TTL and flush
	time.Sleep(8 * time.Second)

	// Now log unrelated query
	_ = logger.LogSearch(ctx, userID, ua, "world")

	got := getLatestQuery(t, logger, anonID)
	if got != "hello" {
		t.Errorf("expected 'hello', got '%s'", got)
	}
	time.Sleep(11 * time.Second)
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

func TestLogSearch_EmptyQueryIgnored(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	userID := "test-empty"
	userAgent := "TestAgent"
	err := logger.LogSearch(ctx, userID, userAgent, "   ")
	if err != nil {
		t.Errorf("expected no error for empty query, got %v", err)
	}
	// Should not write anything to Redis or DB
	val, _ := logger.Redis.Get(ctx, buildRedisKey(userID)).Result()
	if val != "" {
		t.Errorf("expected no value in Redis for empty query, got '%s'", val)
	}
}

func TestLogSearch_AnonUserStoresAnonID(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	userID := ""
	userAgent := "AnonTestAgent"
	query := "search term"
	anonID := generateAnonID(userAgent)

	err := logger.LogSearch(ctx, userID, userAgent, query)
	if err != nil {
		t.Fatalf("LogSearch error: %v", err)
	}
	val, _ := logger.Redis.Get(ctx, buildRedisKey(anonID)).Result()
	if val != normalizeQuery(query) {
		t.Errorf("expected Redis to store normalized query '%s', got '%s'", normalizeQuery(query), val)
	}
}

func TestLogSearch_LoggedInUserStoresUserID(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	userID := "test-user"
	userAgent := "TestAgent"
	query := "MyQuery"

	err := logger.LogSearch(ctx, userID, userAgent, query)
	if err != nil {
		t.Fatalf("LogSearch error: %v", err)
	}
	val, _ := logger.Redis.Get(ctx, buildRedisKey(userID)).Result()
	if val != normalizeQuery(query) {
		t.Errorf("expected Redis to store normalized query '%s', got '%s'", normalizeQuery(query), val)
	}
}

func TestLogSearch_ResetTriggersDBWrite(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	userID := "test-reset"
	userAgent := "TestAgent"
	// Start listener in background
	done := make(chan struct{})
	go func() {
		logger.StartKeyspaceListener(ctx)
		close(done)
	}()
	// First query
	err := logger.LogSearch(ctx, userID, userAgent, "alpha")
	if err != nil {
		t.Fatalf("LogSearch error: %v", err)
	}
	// Second query is a prefix extension (should not trigger DB write)
	err = logger.LogSearch(ctx, userID, userAgent, "alphabet")
	if err != nil {
		t.Fatalf("LogSearch error: %v", err)
	}
	// Third query is a reset (completely different)
	err = logger.LogSearch(ctx, userID, userAgent, "beta")
	if err != nil {
		t.Fatalf("LogSearch error: %v", err)
	}
	// The DB should have "alphabet" as the last search before reset
	got := getLatestQuery(t, logger, userID)
	if got != "alphabet" {
		t.Errorf("expected 'alphabet' to be written to DB, got '%s'", got)
	}
	// Now check the latest query after TTL expiry
	time.Sleep(11 * time.Second) // Wait for TTL expiry
	got = getLatestQuery(t, logger, userID)
	if got != "beta" {
		t.Errorf("expected 'beta' after TTL expiry, got '%s'", got)
	}
}

func TestLogSearch_PrefixExtensionDoesNotWriteDB(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	userID := "test-prefix"
	userAgent := "TestAgent"
	// Start listener in background
	done := make(chan struct{})
	go func() {
		logger.StartKeyspaceListener(ctx)
		close(done)
	}()
	_ = logger.LogSearch(ctx, userID, userAgent, "foo")
	_ = logger.LogSearch(ctx, userID, userAgent, "foob")
	_ = logger.LogSearch(ctx, userID, userAgent, "fooba")
	_ = logger.LogSearch(ctx, userID, userAgent, "foobar")

	// No reset, so nothing should be written to DB yet
	// Wait for TTL expiry and flush
	time.Sleep(11 * time.Second)
	got := getLatestQuery(t, logger, userID)
	if got != "foobar" {
		t.Errorf("expected 'foobar' after TTL expiry, got '%s'", got)
	}
}

func TestLogSearch_AnonResetTriggersDBWrite(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)
	userID := ""
	userAgent := "AnonResetAgent"
	anonID := generateAnonID(userAgent)
	// Start listener in background
	done := make(chan struct{})
	go func() {
		logger.StartKeyspaceListener(ctx)
		close(done)
	}()
	_ = logger.LogSearch(ctx, userID, userAgent, "one")
	_ = logger.LogSearch(ctx, userID, userAgent, "two")
	_ = logger.LogSearch(ctx, userID, userAgent, "three")
	_ = logger.LogSearch(ctx, userID, userAgent, "reset") // triggers DB write

	got := getLatestQuery(t, logger, anonID)
	if got != "three" {
		t.Errorf("expected 'three' to be written to DB, got '%s'", got)
	}
	// Now check the latest query after TTL expiry
	time.Sleep(11 * time.Second) // Wait for TTL expiry
	got = getLatestQuery(t, logger, anonID)
	if got != "reset" {
		t.Errorf("expected 'reset' after TTL expiry, got '%s'", got)
	}
}

func TestWriteSearch_EmptyQueryNoInsert(t *testing.T) {
	ctx := context.Background()
	logger := setupLogger(t)

	entry := SearchEntry{
		UserID: "test-empty-query",
		Query:  "",
		AnonID: "",
	}

	err := logger.writeSearch(ctx, entry)
	if err != nil {
		t.Fatalf("writeSearch should not error on empty query, got: %v", err)
	}

	// Should not insert anything, so DB query should fail
	var count int
	err = logger.DB.QueryRow(`SELECT COUNT(*) FROM user_searches WHERE user_id = $1`, entry.UserID).Scan(&count)
	if err != nil {
		t.Fatalf("DB count error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows inserted for empty query, got %d", count)
	}
}
