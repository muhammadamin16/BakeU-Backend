package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimit caps the size of the request body. Reads beyond max bytes fail, so
// handlers that parse the body (JSON bind, multipart) return an error rather
// than buffering unbounded input. Apply the smallest limit a route needs; do
// not stack two limits on one route, as the inner (smaller) reader wins.
func BodyLimit(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		c.Next()
	}
}
