package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"bakeu/internal/models"
	"bakeu/internal/repository"
)

type publicMarket struct {
	ID          string             `json:"id"`
	SellerID    string             `json:"sellerId"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	City        string             `json:"city"`
	Area        string             `json:"area"`
	Address     string             `json:"address,omitempty"`
	Latitude    *float64           `json:"lat,omitempty"`
	Longitude   *float64           `json:"lng,omitempty"`
	LogoURL     string             `json:"logoURL"`
	Fulfillment models.Fulfillment `json:"fulfillment"`
}

func storefront(m models.Market) publicMarket {
	return publicMarket{
		ID: m.ID, SellerID: m.SellerID, Name: m.Name, Description: m.Description,
		City: m.City, Area: m.Area, Address: m.Address, Latitude: m.Latitude,
		Longitude: m.Longitude, LogoURL: m.LogoURL, Fulfillment: m.Fulfillment,
	}
}

func (h *MarketHandler) ListPublic(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	after := c.Query("after")
	if err != nil || limit < 1 || limit > 100 || len(after) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid market pagination"})
		return
	}
	markets, err := h.markets.ListApproved(c.Request.Context(), after, limit+1)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list markets"})
		return
	}
	next := ""
	if len(markets) > limit {
		markets = markets[:limit]
		next = markets[limit-1].ID
	}
	items := make([]publicMarket, 0, len(markets))
	for _, market := range markets {
		items = append(items, storefront(market))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "next": next})
}

func (h *MarketHandler) GetPublic(c *gin.Context) {
	market, err := h.markets.Get(c.Request.Context(), c.Param("id"))
	if errors.Is(err, repository.ErrNotFound) || (err == nil && market.Status != models.MarketApproved) {
		c.JSON(http.StatusNotFound, gin.H{"error": "market not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load market"})
		return
	}
	c.JSON(http.StatusOK, storefront(*market))
}
