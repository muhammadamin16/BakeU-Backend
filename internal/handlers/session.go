package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"bakeu/internal/auth"
	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
)

type refreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

func (h *AuthHandler) issue(c *gin.Context, status int, user *models.User) {
	c.Header("Cache-Control", "no-store")
	if h.sessions != nil && c.GetHeader("X-BakeU-Session") == "refresh" {
		session, refresh, err := h.sessions.Create(c.Request.Context(), user.UID)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session storage unavailable"})
			return
		}
		h.sendSession(c, status, session, refresh, user)
		return
	}
	token, err := h.jwt.Sign(user.UID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue token"})
		return
	}
	c.JSON(status, gin.H{"token": token, "user": user})
}

func (h *AuthHandler) sendSession(c *gin.Context, status int, session repository.Session, refresh string, user *models.User) {
	token, err := h.jwt.SignSession(session.UserID, session.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue token"})
		return
	}
	result := gin.H{"token": token, "refreshToken": refresh, "expiresIn": int(auth.SessionAccessTTL.Seconds())}
	if user != nil {
		result["user"] = user
	}
	c.JSON(status, result)
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "refreshToken is required"})
		return
	}
	session, refresh, err := h.sessions.Rotate(c.Request.Context(), req.RefreshToken)
	if errors.Is(err, repository.ErrInvalidSession) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired session"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session storage unavailable"})
		return
	}
	h.sendSession(c, http.StatusOK, session, refresh, nil)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "refreshToken is required"})
		return
	}
	if err := h.sessions.Revoke(c.Request.Context(), req.RefreshToken); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session storage unavailable"})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) LogoutAll(c *gin.Context) {
	if err := h.sessions.RevokeAll(c.Request.Context(), middleware.UID(c)); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session storage unavailable"})
		return
	}
	c.Status(http.StatusNoContent)
}
