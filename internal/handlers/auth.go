package handlers

import (
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"bakeu/internal/auth"
	"bakeu/internal/models"
	"bakeu/internal/repository"
)

// Password length bounds. The upper bound matches bcrypt, which silently
// truncates input beyond 72 bytes; rejecting longer input avoids that surprise.
const (
	minPasswordLen = 8
	maxPasswordLen = 72
)

// AuthHandler serves signup and login, issuing app JWTs.
type AuthHandler struct {
	users          *repository.UserRepo
	jwt            *auth.Manager
	guard          *auth.LoginGuard
	sessions       *repository.SessionRepo
	browserOrigins []string
}

func NewAuthHandler(users *repository.UserRepo, jwt *auth.Manager, guard *auth.LoginGuard, sessions *repository.SessionRepo, browserOrigins []string) *AuthHandler {
	return &AuthHandler{users: users, jwt: jwt, guard: guard, sessions: sessions, browserOrigins: browserOrigins}
}

// normalizeEmail lower-cases and trims an email so case/whitespace differences
// cannot create duplicate accounts or break login.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

type signupRequest struct {
	Email    string      `json:"email"`
	Password string      `json:"password"`
	Role     models.Role `json:"role"`
	Name     string      `json:"name"`
	Phone    string      `json:"phone"`
	Address  string      `json:"address"`
}

// Signup handles POST /api/auth/signup — creates an account and returns a token.
func (h *AuthHandler) Signup(c *gin.Context) {
	var req signupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	email := normalizeEmail(req.Email)
	if email == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email and password are required"})
		return
	}
	// Require a plain, parseable address (no display name) so approval emails
	// can actually be delivered to it later.
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || strings.HasSuffix(email, phoneAccountEmailSuffix) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email address"})
		return
	}
	if len(req.Password) < minPasswordLen || len(req.Password) > maxPasswordLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password must be between 8 and 72 characters"})
		return
	}
	if !req.Role.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be 'seller' or 'customer'"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	u := &models.User{
		Email:        email,
		PasswordHash: string(hash),
		Role:         req.Role,
		Name:         req.Name,
		Phone:        req.Phone,
		Address:      req.Address,
		CreatedAt:    time.Now().UTC(),
	}
	// The unique email index is the source of truth for duplicates: a pre-check
	// would race with concurrent signups for the same address.
	if err := h.users.Create(c.Request.Context(), u); err != nil {
		if errors.Is(err, repository.ErrDuplicate) {
			c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
		return
	}

	h.issue(c, http.StatusCreated, u)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login handles POST /api/auth/login — verifies credentials and returns a token.
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	email := normalizeEmail(req.Email)
	ip := c.ClientIP()

	// Reject brute-force attempts before doing any work or leaking timing.
	if allowed, retryAfter := h.guard.Check(ip, email); !allowed {
		c.Header("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many attempts, try again later"})
		return
	}

	ctx := c.Request.Context()
	u, err := h.users.GetByEmail(ctx, email)
	if errors.Is(err, repository.ErrNotFound) {
		h.guard.RecordFailure(ip, email)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load user"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		h.guard.RecordFailure(ip, email)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	h.guard.Reset(ip, email)
	if !u.Active() {
		c.JSON(http.StatusForbidden, gin.H{"error": "account is unavailable"})
		return
	}
	h.issue(c, http.StatusOK, u)
}
