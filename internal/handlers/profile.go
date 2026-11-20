package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
)

// ProfileHandler serves the current user's profile endpoints.
type ProfileHandler struct {
	users   *repository.UserRepo
	markets *repository.MarketRepo
}

func NewProfileHandler(users *repository.UserRepo, markets *repository.MarketRepo) *ProfileHandler {
	return &ProfileHandler{users: users, markets: markets}
}

type profileRequest struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	Address string `json:"address"`
}

// Upsert handles POST /api/profile — updates the caller's mutable profile
// fields (name/phone/address). Role and email are fixed at signup and cannot be
// changed here.
func (h *ProfileHandler) Upsert(c *gin.Context) {
	uid := middleware.UID(c)

	var req profileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	u, err := h.users.UpdateProfile(ctx, uid, req.Name, req.Phone, req.Address)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save profile"})
		return
	}
	c.JSON(http.StatusOK, u)
}

// meResponse is the user profile with the seller's market embedded (null for
// customers or sellers who haven't applied). The anonymous *models.User flattens
// its fields to the top level, so existing clients keep working.
type meResponse struct {
	*models.User
	Market *models.Market `json:"market"`
}

// Me handles GET /api/me — returns the caller's profile plus their market.
func (h *ProfileHandler) Me(c *gin.Context) {
	ctx := c.Request.Context()
	u, err := h.users.Get(ctx, middleware.UID(c))
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load profile"})
		return
	}

	var market *models.Market
	if u.Role == models.RoleSeller {
		m, err := h.markets.GetBySeller(ctx, u.UID)
		if err == nil {
			market = m
		} else if !errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load market"})
			return
		}
	}
	c.JSON(http.StatusOK, meResponse{User: u, Market: market})
}
