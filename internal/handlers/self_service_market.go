package handlers

import (
	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"github.com/gin-gonic/gin"
	"math"
	"strings"
	"unicode/utf8"
)

func (h *MarketHandler) OpenOwn(c *gin.Context) {
	var req struct {
		FirstName   string   `json:"firstName"`
		LastName    string   `json:"lastName"`
		Name        string   `json:"name"`
		Address     string   `json:"address"`
		Description string   `json:"description"`
		Latitude    *float64 `json:"lat"`
		Longitude   *float64 `json:"lng"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"error": "Проверьте данные магазина."})
		return
	}
	req.FirstName, req.LastName, req.Name, req.Address, req.Description = strings.TrimSpace(req.FirstName), strings.TrimSpace(req.LastName), strings.TrimSpace(req.Name), strings.TrimSpace(req.Address), strings.TrimSpace(req.Description)
	for _, field := range []struct {
		value    string
		min, max int
	}{{req.FirstName, 1, 80}, {req.LastName, 1, 80}, {req.Name, 2, 150}, {req.Address, 3, 250}, {req.Description, 0, 2000}} {
		n := utf8.RuneCountInString(field.value)
		if n < field.min || n > field.max {
			c.JSON(400, gin.H{"error": "Проверьте имя, фамилию, название и адрес магазина."})
			return
		}
	}
	if req.Latitude == nil || req.Longitude == nil || math.IsNaN(*req.Latitude) || math.IsNaN(*req.Longitude) || math.IsInf(*req.Latitude, 0) || math.IsInf(*req.Longitude, 0) || math.Abs(*req.Latitude) > 90 || math.Abs(*req.Longitude) > 180 {
		c.JSON(400, gin.H{"error": "Выберите точку магазина на карте."})
		return
	}
	m, err := h.markets.ActivateOwnMarket(c.Request.Context(), middleware.UID(c), req.FirstName, req.LastName, req.Name, req.Address, req.Description, *req.Latitude, *req.Longitude)
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(200, m)
}

func (h *MarketHandler) Moderate(c *gin.Context) {
	var req struct {
		Expected models.MarketStatus `json:"expectedStatus"`
		Status   models.MarketStatus `json:"status"`
		Reason   string              `json:"reason"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"error": "invalid moderation request"})
		return
	}
	allowed := (req.Expected == models.MarketApproved && req.Status == models.MarketSuspended) || (req.Expected == models.MarketSuspended && req.Status == models.MarketApproved) || (req.Expected == models.MarketPending && (req.Status == models.MarketApproved || req.Status == models.MarketRejected))
	req.Reason = strings.TrimSpace(req.Reason)
	if !allowed || utf8.RuneCountInString(req.Reason) < 2 || utf8.RuneCountInString(req.Reason) > 500 {
		c.JSON(400, gin.H{"error": "Укажите допустимый переход и причину (2–500 символов)."})
		return
	}
	m, err := h.markets.Moderate(c.Request.Context(), c.Param("id"), middleware.UID(c), req.Expected, req.Status, req.Reason)
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(200, m)
}

func (h *MarketHandler) ListAdmin(c *gin.Context) {
	after := c.Query("after")
	if len(after) > 128 {
		c.JSON(400, gin.H{"error": "invalid cursor"})
		return
	}
	items, err := h.markets.ListForAdmin(c.Request.Context(), after)
	if err != nil {
		orderError(c, err)
		return
	}
	next := ""
	if len(items) > 100 {
		items = items[:100]
		next = items[len(items)-1].ID
	}
	c.JSON(200, gin.H{"items": items, "next": next})
}

func RequireVerifiedAdmin(users *repository.UserRepo, ids []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := middleware.UID(c)
		allowed := false
		for _, id := range ids {
			if id == uid {
				allowed = true
				break
			}
		}
		u, err := users.Get(c.Request.Context(), uid)
		if !allowed || err != nil || !u.Active() || !u.PhoneVerified() {
			c.AbortWithStatusJSON(403, gin.H{"error": "verified administrator required"})
			return
		}
		c.Next()
	}
}
