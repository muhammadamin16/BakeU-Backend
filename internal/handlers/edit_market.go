package handlers

import (
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"

	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
)

type marketChanges struct {
	Name        *string             `json:"name"`
	Description *string             `json:"description"`
	City        *string             `json:"city"`
	Area        *string             `json:"area"`
	Address     *string             `json:"address"`
	Latitude    *float64            `json:"lat"`
	Longitude   *float64            `json:"lng"`
	ClearPoint  bool                `json:"clearPoint"`
	Fulfillment *models.Fulfillment `json:"fulfillment"`
}

func (req marketChanges) fields() (bson.M, error) {
	fields := bson.M{}
	for _, item := range []struct {
		key      string
		value    *string
		max      int
		required bool
	}{
		{"name", req.Name, 150, true},
		{"description", req.Description, 2000, true},
		{"city", req.City, 100, true},
		{"area", req.Area, 100, false},
		{"address", req.Address, 250, false},
	} {
		if item.value == nil {
			continue
		}
		value := strings.TrimSpace(*item.value)
		if len([]rune(value)) > item.max || (item.required && value == "") {
			return nil, errors.New("invalid " + item.key)
		}
		fields[item.key] = value
	}
	if req.Latitude != nil || req.Longitude != nil {
		if req.ClearPoint || req.Latitude == nil || req.Longitude == nil ||
			math.IsNaN(*req.Latitude) || math.IsNaN(*req.Longitude) ||
			math.Abs(*req.Latitude) > 90 || math.Abs(*req.Longitude) > 180 {
			return nil, errors.New("lat and lng must be a valid coordinate pair")
		}
		fields["latitude"], fields["longitude"] = *req.Latitude, *req.Longitude
	}
	if req.ClearPoint {
		fields["latitude"], fields["longitude"] = nil, nil
	}
	if req.Fulfillment != nil {
		if !req.Fulfillment.Valid() {
			return nil, errors.New("invalid fulfillment")
		}
		fields["fulfillment"] = *req.Fulfillment
	}
	if len(fields) == 0 {
		return nil, errors.New("no storefront fields supplied")
	}
	return fields, nil
}

func (h *MarketHandler) EditMine(c *gin.Context) {
	var req marketChanges
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid storefront update"})
		return
	}
	fields, err := req.fields()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	market, err := h.markets.EditApproved(c.Request.Context(), middleware.UID(c), fields)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "approved storefront not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update storefront"})
		return
	}
	c.JSON(http.StatusOK, market)
}
