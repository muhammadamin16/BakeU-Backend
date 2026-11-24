package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"

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
	Variants    *[]models.ProductVariant `json:"variants"`
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Price       float64                  `json:"price"`
	Category    string                   `json:"category"`
	Available   bool                     `json:"available"`
}

func (req *productRequest) validVariants() bool {
	if req.Variants == nil {
		return true
	}
	if len(*req.Variants) > 20 {
		return false
	}
	names := make(map[string]bool)
	ids := make(map[string]bool)
	for i, variant := range *req.Variants {
		variant.Name = strings.TrimSpace(variant.Name)
		key := strings.ToLower(variant.Name)
		if key == "" || len([]rune(key)) > 100 || !models.ValidProductPrice(variant.Price) || names[key] {
			return false
		}
		if variant.ID != "" {
			if _, err := bson.ObjectIDFromHex(variant.ID); err != nil || ids[variant.ID] {
				return false
			}
			ids[variant.ID] = true
		}
		names[key] = true
		(*req.Variants)[i] = variant
	}
	return true
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
	if !req.validVariants() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product variants"})
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || !models.ValidProductPrice(req.Price) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required and price must be finite, > 0 and <= 1000000000000"})
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

	if req.Variants != nil {
		p.Variants = *req.Variants
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
	if !req.validVariants() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product variants"})
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || !models.ValidProductPrice(req.Price) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required and price must be finite, > 0 and <= 1000000000000"})
		return
	}

	p.Title = req.Title
	p.Description = req.Description
	p.Price = req.Price
	p.Category = req.Category
	p.Available = req.Available
	p.UpdatedAt = time.Now().UTC()
	if req.Variants != nil {
		known := make(map[string]bool)
		byName := make(map[string]string)
		used := make(map[string]bool)
		for _, variant := range p.Variants {
			known[variant.ID] = true
			byName[strings.ToLower(variant.Name)] = variant.ID
		}
		for i, variant := range *req.Variants {
			if variant.ID != "" && !known[variant.ID] {
				c.JSON(400, gin.H{"error": "unknown product variant id"})
				return
			}
			if variant.ID == "" {
				variant.ID = byName[strings.ToLower(variant.Name)]
				if variant.ID == "" {
					variant.ID = bson.NewObjectID().Hex()
				}
			}
			if used[variant.ID] {
				c.JSON(400, gin.H{"error": "duplicate product variant id"})
				return
			}
			used[variant.ID] = true
			(*req.Variants)[i] = variant
		}
	}

	updated, err := h.products.UpdateDetails(c.Request.Context(), p, req.Variants)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update product"})
		return
	}
	c.JSON(http.StatusOK, updated)
}

// Delete archives a listing without destroying its document, images or order
// snapshots. Public reads, checkout and seller lists exclude archived items.
func (h *ProductHandler) Delete(c *gin.Context) {
	p, ok := h.loadOwned(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := h.products.Archive(ctx, p.ID, p.SellerID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete product"})
		return
	}
	c.Status(http.StatusNoContent)
}

// List handles GET /api/products (public). Supports ?category=, ?seller=, ?q=.
// Only products from bakeries with an approved market are returned.
func (h *ProductHandler) List(c *gin.Context) {
	limit := 0
	after := c.Query("after")
	if raw, paged := c.GetQuery("limit"); paged || after != "" {
		if !paged {
			raw = "50"
		}
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 || len(after) > 128 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product pagination"})
			return
		}
	}
	ctx := c.Request.Context()
	approved, err := h.markets.ApprovedSellerIDs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list products"})
		return
	}
	if approved == nil {
		approved = []string{}
	}
	fetchLimit := 0
	if limit > 0 {
		fetchLimit = limit + 1
	}
	products, err := h.products.ListPage(
		ctx,
		c.Query("category"),
		c.Query("seller"),
		c.Query("q"),
		approved, after, fetchLimit,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list products"})
		return
	}
	if products == nil {
		products = []models.Product{}
	}
	if limit > 0 {
		next := ""
		if len(products) > limit {
			products = products[:limit]
			next = products[limit-1].ID
		}
		c.JSON(http.StatusOK, gin.H{"items": products, "next": next})
		return
	}
	c.JSON(http.StatusOK, products)
}

// GetOne handles GET /api/products/:id (public). Like List, it only exposes
// products whose bakery is approved — a direct fetch by ID must not leak more
// than the listing does.
func (h *ProductHandler) GetOne(c *gin.Context) {
	ctx := c.Request.Context()
	p, err := h.products.Get(ctx, c.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load product"})
		return
	}
	approved, err := h.markets.IsApprovedSeller(ctx, p.SellerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load product"})
		return
	}
	if !approved {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	c.JSON(http.StatusOK, p)
}

// maxProductImages caps how many images one product may accumulate, bounding
// per-product GridFS usage.
const maxProductImages = 5

// UploadImage handles POST /api/products/:id/image (seller only, owner only, approved market).
func (h *ProductHandler) UploadImage(c *gin.Context) {
	if !h.requireApproved(c) {
		return
	}
	p, ok := h.loadOwned(c)
	if !ok {
		return
	}
	if len(p.ImageURLs) >= maxProductImages {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("a product can have at most %d images", maxProductImages)})
		return
	}

	url, ok := uploadImageFile(c, h.store, "image")
	if !ok {
		return
	}

	ctx := c.Request.Context()
	updated, err := h.products.AppendImage(ctx, p.ID, p.SellerID, url, maxProductImages)
	if err != nil {
		// The URL was never persisted, so the stored file would be orphaned.
		deleteImageByURL(ctx, h.store, url)
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusConflict, gin.H{"error": "product no longer accepts images"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save image url"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imageURLs": updated.ImageURLs})
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
