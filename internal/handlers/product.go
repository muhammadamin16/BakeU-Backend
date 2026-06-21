package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"bakeu/internal/storage"
)

// ProductHandler serves product browsing and seller product management.
type ProductHandler struct {
	products *repository.ProductRepo
	store    *storage.Store
	markets  *repository.MarketRepo
}

func NewProductHandler(products *repository.ProductRepo, store *storage.Store, markets *repository.MarketRepo) *ProductHandler {
	return &ProductHandler{products: products, store: store, markets: markets}
}

type productRequest struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
	Available   bool    `json:"available"`
}

// Create handles POST /api/products (seller only, approved market only).
func (h *ProductHandler) Create(c *gin.Context) {
	if !h.requireApproved(c) {
		return
	}
	var req productRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Title == "" || req.Price <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required and price must be > 0"})
		return
	}

	now := time.Now().UTC()
	p := &models.Product{
		SellerID:    middleware.UID(c),
		Title:       req.Title,
		Description: req.Description,
		Price:       req.Price,
		Category:    req.Category,
		Available:   req.Available,
		ImageURLs:   []string{},
		RatingAvg:   0,
		RatingCount: 0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if _, err := h.products.Create(c.Request.Context(), p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create product"})
		return
	}
	c.JSON(http.StatusCreated, p)
}

// Update handles PUT /api/products/:id (seller only, owner only, approved market).
func (h *ProductHandler) Update(c *gin.Context) {
	if !h.requireApproved(c) {
		return
	}
	p, ok := h.loadOwned(c)
	if !ok {
		return
	}

	var req productRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Title == "" || req.Price <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required and price must be > 0"})
		return
	}

	p.Title = req.Title
	p.Description = req.Description
	p.Price = req.Price
	p.Category = req.Category
	p.Available = req.Available
	p.UpdatedAt = time.Now().UTC()

	if err := h.products.Update(c.Request.Context(), p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update product"})
		return
	}
	c.JSON(http.StatusOK, p)
}

// Delete handles DELETE /api/products/:id (seller only, owner only).
func (h *ProductHandler) Delete(c *gin.Context) {
	p, ok := h.loadOwned(c)
	if !ok {
		return
	}
	if err := h.products.Delete(c.Request.Context(), p.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete product"})
		return
	}
	c.Status(http.StatusNoContent)
}

// List handles GET /api/products (public). Supports ?category=, ?seller=, ?q=.
// Only products from bakeries with an approved market are returned.
func (h *ProductHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	approved, err := h.markets.ApprovedSellerIDs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list products"})
		return
	}
	if approved == nil {
		approved = []string{}
	}
	products, err := h.products.List(
		ctx,
		c.Query("category"),
		c.Query("seller"),
		c.Query("q"),
		approved,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list products"})
		return
	}
	if products == nil {
		products = []models.Product{}
	}
	c.JSON(http.StatusOK, products)
}

// GetOne handles GET /api/products/:id (public).
func (h *ProductHandler) GetOne(c *gin.Context) {
	p, err := h.products.Get(c.Request.Context(), c.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load product"})
		return
	}
	c.JSON(http.StatusOK, p)
}

// UploadImage handles POST /api/products/:id/image (seller only, owner only, approved market).
func (h *ProductHandler) UploadImage(c *gin.Context) {
	if !h.requireApproved(c) {
		return
	}
	p, ok := h.loadOwned(c)
	if !ok {
		return
	}

	url, ok := uploadImageFile(c, h.store, "image")
	if !ok {
		return
	}

	p.ImageURLs = append(p.ImageURLs, url)
	if err := h.products.SetImageURLs(c.Request.Context(), p.ID, p.ImageURLs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save image url"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imageURLs": p.ImageURLs})
}

// requireApproved ensures the seller's market is approved before they can
// create or modify listings. Writes a 403 and returns false otherwise.
func (h *ProductHandler) requireApproved(c *gin.Context) bool {
	ok, err := h.markets.IsApprovedSeller(c.Request.Context(), middleware.UID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check market status"})
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "your bakery must be approved first"})
		return false
	}
	return true
}

// loadOwned fetches the product named by :id and verifies the caller owns it.
// It writes the appropriate error response and returns ok=false on failure.
func (h *ProductHandler) loadOwned(c *gin.Context) (*models.Product, bool) {
	p, err := h.products.Get(c.Request.Context(), c.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load product"})
		return nil, false
	}
	if p.SellerID != middleware.UID(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this product"})
		return nil, false
	}
	return p, true
}
