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
	orders  *repository.OrderRepo
}

func NewReviewHandler(reviews *repository.ReviewRepo, orders *repository.OrderRepo) *ReviewHandler {
	return &ReviewHandler{reviews: reviews, orders: orders}
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

	ctx := c.Request.Context()
	uid := middleware.UID(c)

	// Only verified buyers may review: the customer must have a delivered order
	// containing this product.
	bought, err := h.orders.HasDeliveredProduct(ctx, uid, productID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify purchase"})
		return
	}
	if !bought {
		c.JSON(http.StatusForbidden, gin.H{"error": "you can only review products you have purchased"})
		return
	}

	rev := models.Review{
		CustomerID: uid,
		Rating:     req.Rating,
		Comment:    req.Comment,
		CreatedAt:  time.Now().UTC(),
	}

	_, err = h.reviews.Create(ctx, productID, &rev)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if errors.Is(err, repository.ErrDuplicate) {
		c.JSON(http.StatusConflict, gin.H{"error": "you already reviewed this product"})
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
