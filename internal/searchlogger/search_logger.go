package searchlogger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

// Logger handles logging of user search queries to Redis and a SQL database.
type Logger struct {
	Redis *redis.Client // Redis client for caching recent searches
	DB    *sql.DB       // SQL database for persistent search logs
}

// SearchEntry represents a search to be logged.
type SearchEntry struct {
	UserID string
	Query  string
	AnonID string // new field for anon id
}

// normalizeQuery lowercases and trims the input search query.
func normalizeQuery(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}

// buildRedisKey constructs a Redis key for storing the last search of a user.
func buildRedisKey(userID string) string {
	return "search:last:" + userID
}

const insertQuery = `INSERT INTO user_searches (user_id, search_text, last_searched_at, anon_id)
			VALUES ($1, $2, NOW(), $3)`

// LogSearch processes and logs a user's search query.
// It uses Redis to track the latest query and only writes to the DB when a "reset" is detected
// or when the query is extended significantly.
func (l *Logger) LogSearch(ctx context.Context, userID, userAgent, query string) error {
	normalizedQuery := normalizeQuery(query)
	if normalizedQuery == "" {
		log.Printf("LogSearch: empty query ignored for userID=%s", userID)
		return nil
	}

	isAnon := false
	anonID := ""
	if strings.TrimSpace(userID) == "" {
		anonID = generateAnonID(userAgent)
		isAnon = true
		log.Printf("LogSearch: generated anonymous anonID=%s from userAgent", anonID)
	}

	idForRedis := userID
	if isAnon {
		idForRedis = anonID
	}

	redisKey := buildRedisKey(idForRedis)
	bufferKey := "search:buffer:" + idForRedis
	lastQuery, _ := l.Redis.Get(ctx, redisKey).Result()

	// If lastQuery is completely different from the new query, write it to the DB.
	if lastQuery != "" &&
		!strings.HasPrefix(normalizedQuery, lastQuery) && !strings.HasPrefix(lastQuery, normalizedQuery) {

		log.Printf("LogSearch: detected reset for userID=%s, lastQuery='%s', newQuery='%s'", userID, lastQuery, normalizedQuery)
		entry := SearchEntry{
			UserID: userID,
			Query:  lastQuery,
			AnonID: anonID,
		}
		if err := l.writeSearch(ctx, entry); err != nil {
			log.Printf("LogSearch: error writing search to DB for userID=%s: %v", userID, err)
			return err
		}
	}

	err1 := l.Redis.Set(ctx, redisKey, normalizedQuery, 10*time.Second).Err()
	err2 := l.Redis.Set(ctx, bufferKey, normalizedQuery, 1*time.Hour).Err()
	if err1 != nil || err2 != nil {
		log.Printf("LogSearch: Redis set error: key=%s err1=%v, bufferKey=%s err2=%v", redisKey, err1, bufferKey, err2)
		return fmt.Errorf("redis set error: %v %v", err1, err2)
	}
	log.Printf("LogSearch: updated Redis and buffer with new query for redisKey=%s", redisKey)
	return nil
}

// writeSearch writes the user's search query to the SQL database in a transaction.
func (l *Logger) writeSearch(ctx context.Context, entry SearchEntry) error {
	if entry.Query == "" {
		log.Printf("writeSearch: empty query for userID=%s, skipping write", entry.UserID)
		return nil
	}
	tx, err := l.DB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("writeSearch: error starting transaction for userID=%s: %v", entry.UserID, err)
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			log.Printf("writeSearch: panic recovered for userID=%s: %v", entry.UserID, p)
			panic(p)
		}
	}()

	args := []interface{}{entry.UserID, entry.Query, entry.AnonID}
	_, err = tx.ExecContext(ctx, insertQuery, args...)
	if err != nil {
		tx.Rollback()
		log.Printf("writeSearch: error inserting query for userID=%s: %v", entry.UserID, err)
		return err
	}

	if err := tx.Commit(); err != nil {
		log.Printf("writeSearch: error committing transaction for userID=%s: %v", entry.UserID, err)
		return err
	}
	log.Printf("writeSearch: successfully logged search for userID=%s, query='%s'", entry.UserID, entry.Query)
	return nil
}

// generateAnonID generates a stable anonymous ID from the User-Agent string.
func generateAnonID(userAgent string) string {
	return "anon" + fmt.Sprintf("%x", sha256.Sum256([]byte(userAgent)))
}

// StartKeyspaceListener listens to Redis key expiry events and flushes expired queries to the DB.
func (l *Logger) StartKeyspaceListener(ctx context.Context) {
	pubsub := l.Redis.PSubscribe(ctx, "__keyevent@0__:expired")
	defer pubsub.Close()
	ch := pubsub.Channel()

	log.Println("Started Redis keyspace listener")

	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping keyspace listener")
			return
		case msg := <-ch:
			expiredKey := msg.Payload
			if !strings.HasPrefix(expiredKey, "search:last:") {
				continue
			}

			userID := strings.TrimPrefix(expiredKey, "search:last:")
			bufferKey := "search:buffer:" + userID

			query, err := l.Redis.Get(ctx, bufferKey).Result()
			if err != nil {
				log.Printf("KeyspaceListener: could not retrieve buffered query for userID=%s: %v", userID, err)
				continue
			}

			isAnon := strings.HasPrefix(userID, "anon") // robust check for anon ID
			entry := SearchEntry{
				UserID: "",
				Query:  query,
				AnonID: "",
			}
			if isAnon {
				entry.AnonID = userID
			} else {
				entry.UserID = userID
			}
			if err := l.writeSearch(ctx, entry); err != nil {
				log.Printf("KeyspaceListener: failed to write search to DB for userID=%s: %v", userID, err)
				continue
			}
			_ = l.Redis.Del(ctx, bufferKey).Err()
			log.Printf("KeyspaceListener: flushed expired query for userID=%s", userID)
		}
	}
}
