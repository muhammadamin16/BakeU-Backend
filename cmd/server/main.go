package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"bakeu/internal/config"
	"bakeu/internal/mongoclient"
	"bakeu/internal/router"
)

func main() {
	cfg := config.Load()
	if cfg.JWTSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}
	ctx := context.Background()

	clients, err := mongoclient.New(ctx, cfg)
	if err != nil {
		log.Fatalf("failed to initialize mongo: %v", err)
	}
	defer clients.Close()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router.New(clients.DB, cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("Bakeu API listening on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
