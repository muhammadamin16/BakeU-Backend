package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Health is an unauthenticated liveness/readiness probe.
func Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}
