package handlers

import (
	"bakeu/internal/repository"
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// Health is an unauthenticated liveness probe: the process is up. It must not
// touch dependencies, so it stays truthful during a database outage.
func Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// HealthHandler serves the readiness probe, which also checks the database.
type HealthHandler struct {
	client *mongo.Client
}

func NewHealthHandler(client *mongo.Client) *HealthHandler {
	return &HealthHandler{client: client}
}

// Ready handles GET /readyz — 200 only when MongoDB is reachable and its
// topology supports the transactions required by identity, checkout and outbox.
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := h.client.Ping(ctx, readpref.Primary()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "error": "database unreachable"})
		return
	}
	transactions, err := repository.TransactionsAvailable(ctx, h.client)
	if err != nil || !transactions {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "code": "MONGO_TRANSACTIONS_REQUIRED", "error": "transaction-capable storage is not ready"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
