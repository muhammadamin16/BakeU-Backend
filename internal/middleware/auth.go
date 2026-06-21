package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"bakeu/internal/auth"
)

// contextUID is the gin context key under which the authenticated user ID is
// stored.
const contextUID = "uid"

// AuthRequired verifies the app JWT in the Authorization header and stores the
// caller's user ID in the request context. Requests without a valid token are
// rejected with 401.
func AuthRequired(jwtm *auth.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))

		uid, err := jwtm.Parse(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		c.Set(contextUID, uid)
		c.Next()
	}
}

// UID returns the authenticated user's ID from the context. It is only
// meaningful on routes guarded by AuthRequired.
func UID(c *gin.Context) string {
	v, _ := c.Get(contextUID)
	uid, _ := v.(string)
	return uid
}
