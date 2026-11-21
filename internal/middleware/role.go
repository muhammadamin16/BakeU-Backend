package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"bakeu/internal/models"
	"bakeu/internal/repository"
)

// contextUser holds the loaded *models.User for the current request.
const contextUser = "user"

// RequireActive also protects account reads and profile/cart writes.
func RequireActive(users *repository.UserRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		u, err := users.Get(c.Request.Context(), UID(c))
		if errors.Is(err, repository.ErrNotFound) || err == nil && !u.Active() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "account is unavailable"})
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "account lookup unavailable"})
			return
		}
		c.Set(contextUser, u)
		c.Next()
	}
}

// RequireRole loads the caller's profile and ensures it has the given role.
// Must run after AuthRequired. The loaded user is cached in the context and is
// retrievable via CurrentUser.
func RequireRole(users *repository.UserRepo, role models.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := UID(c)
		u, err := users.Get(c.Request.Context(), uid)
		if errors.Is(err, repository.ErrNotFound) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "create a profile first"})
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to load profile"})
			return
		}
		if !u.Active() || u.Role != role {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "requires " + string(role) + " role"})
			return
		}
		c.Set(contextUser, u)
		c.Next()
	}
}

// CurrentUser returns the profile loaded by RequireRole, or nil if none.
func CurrentUser(c *gin.Context) *models.User {
	v, ok := c.Get(contextUser)
	if !ok {
		return nil
	}
	u, _ := v.(*models.User)
	return u
}
