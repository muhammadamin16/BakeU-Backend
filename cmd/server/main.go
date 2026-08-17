package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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

	// Railway (and any orchestrator) signals SIGTERM before killing the
	// container; shut down gracefully so in-flight requests finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	clients, err := mongoclient.New(initCtx, cfg)
	if err != nil {
		log.Fatalf("failed to initialize mongo: %v", err)
	}
	defer clients.Close()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router.New(clients.DB, cfg),
		ReadHeaderTimeout: 10 * time.Second,
		// Generous enough for a 5 MB image upload/download on a slow mobile
		// connection, while still bounding how long a connection can be held.
		ReadTimeout:  2 * time.Minute,
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Bakeu API listening on :%s", cfg.Port)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	case <-ctx.Done():
		log.Println("shutting down…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown: %v", err)
		}
	}
}
