package main

import (
	"context"
	"go-search-logger/config"
	"log"

	"go-search-logger/internal/database"
	"go-search-logger/internal/searchlogger"
	"go-search-logger/internal/server"

	"github.com/go-redis/redis/v8"
)

func main() {
	redisClient := redis.NewClient(&redis.Options{
		Addr: config.RedisAddr,
	})

	db := database.ConnectPostgres(config.DBConnStr)

	logger := &searchlogger.Logger{
		Redis: redisClient,
		DB:    db,
	}
	ctx := context.Background()
	// Start listener in background
	go logger.StartKeyspaceListener(ctx)

	srv := server.NewServer(logger)
	if err := srv.Start(config.Port); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
