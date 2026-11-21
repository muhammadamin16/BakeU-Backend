package middleware

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/gin-gonic/gin"
)

// TrustedBrowserOrigin matches exact HTTPS origins. Cookies are only accepted
// from this allowlist, including when a request arrives through a same-origin proxy.
func TrustedBrowserOrigin(origin string, allowed []string) bool {
	value, err := url.Parse(origin)
	return err == nil && value.Scheme == "https" && value.Host != "" &&
		origin == "https://"+value.Host && slices.Contains(allowed, origin)
}

func CORS(allowed []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		c.Writer.Header().Add("Vary", "Origin")
		if TrustedBrowserOrigin(origin, allowed) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-BakeU-Session, X-BakeU-Account, Idempotency-Key")
		} else {
			if origin != "" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "request origin is not allowed"})
				return
			}
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
