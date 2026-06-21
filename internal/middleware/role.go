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
		if u.Role != role {
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
