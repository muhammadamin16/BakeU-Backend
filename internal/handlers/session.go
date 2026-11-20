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

const refreshCookie = "__Host-bakeu-refresh"
const browserSession = "browser"

func (h *AuthHandler) CheckSessionOrigin(c *gin.Context) {
	if c.GetHeader("X-BakeU-Session") == browserSession &&
		(h.sessions == nil || !middleware.TrustedBrowserOrigin(c.GetHeader("Origin"), h.browserOrigins)) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "browser session origin is not allowed"})
		return
	}
	c.Next()
}

func setRefreshCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: refreshCookie, Value: value, MaxAge: maxAge, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

func (h *AuthHandler) issue(c *gin.Context, status int, user *models.User) {
	c.Header("Cache-Control", "no-store")
	mode := c.GetHeader("X-BakeU-Session")
	if h.sessions != nil && (mode == "refresh" || mode == browserSession) {
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
	result := gin.H{"token": token, "expiresIn": int(auth.SessionAccessTTL.Seconds())}
	if c.GetHeader("X-BakeU-Session") == browserSession {
		setRefreshCookie(c, refresh, 30*24*60*60)
	} else {
		result["refreshToken"] = refresh
	}
	if user != nil {
		result["user"] = user
	}
	c.JSON(status, result)
}

func readRefreshToken(c *gin.Context) (string, bool) {
	if c.GetHeader("X-BakeU-Session") == browserSession {
		value, err := c.Cookie(refreshCookie)
		return value, err == nil && value != ""
	}
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "refreshToken is required"})
		return "", false
	}
	return req.RefreshToken, true
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	token, ok := readRefreshToken(c)
	if !ok {
		if c.GetHeader("X-BakeU-Session") == browserSession {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired session"})
		}
		return
	}
	session, refresh, err := h.sessions.Rotate(c.Request.Context(), token)
	if errors.Is(err, repository.ErrInvalidSession) {
		if c.GetHeader("X-BakeU-Session") == browserSession {
			setRefreshCookie(c, "", -1)
		}
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
	token, ok := readRefreshToken(c)
	if !ok {
		if c.GetHeader("X-BakeU-Session") == browserSession {
			c.Status(http.StatusNoContent)
		}
		return
	}
	if err := h.sessions.Revoke(c.Request.Context(), token); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session storage unavailable"})
		return
	}
	if c.GetHeader("X-BakeU-Session") == browserSession {
		setRefreshCookie(c, "", -1)
	}
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) LogoutAll(c *gin.Context) {
	if err := h.sessions.RevokeAll(c.Request.Context(), middleware.UID(c)); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session storage unavailable"})
		return
	}
	if c.GetHeader("X-BakeU-Session") == browserSession {
		setRefreshCookie(c, "", -1)
	}
	c.Status(http.StatusNoContent)
}
