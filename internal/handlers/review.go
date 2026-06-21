package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
)

// ReviewHandler serves product reviews.
type ReviewHandler struct {
	reviews *repository.ReviewRepo
}

func NewReviewHandler(reviews *repository.ReviewRepo) *ReviewHandler {
	return &ReviewHandler{reviews: reviews}
}

// Create handles POST /api/products/:id/reviews (customer only).
func (h *ReviewHandler) Create(c *gin.Context) {
	productID := c.Param("id")

	var req struct {
		Rating  int    `json:"rating"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Rating < 1 || req.Rating > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rating must be between 1 and 5"})
		return
	}

	rev := models.Review{
		CustomerID: middleware.UID(c),
		Rating:     req.Rating,
		Comment:    req.Comment,
		CreatedAt:  time.Now().UTC(),
	}

	_, err := h.reviews.Create(c.Request.Context(), productID, &rev)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create review"})
		return
	}
	c.JSON(http.StatusCreated, rev)
}

// ListByProduct handles GET /api/products/:id/reviews (public).
func (h *ReviewHandler) ListByProduct(c *gin.Context) {
	reviews, err := h.reviews.ListByProduct(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list reviews"})
		return
	}
	if reviews == nil {
		reviews = []models.Review{}
	}
	c.JSON(http.StatusOK, reviews)
}
