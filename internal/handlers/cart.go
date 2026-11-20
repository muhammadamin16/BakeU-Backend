package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
)

// maxCartItems caps the number of distinct products in one cart.
const maxCartItems = 100

// CartHandler serves the customer's shopping cart.
type CartHandler struct {
	carts *repository.CartRepo
}

func NewCartHandler(carts *repository.CartRepo) *CartHandler {
	return &CartHandler{carts: carts}
}

// Get handles GET /api/cart.
func (h *CartHandler) Get(c *gin.Context) {
	cart, err := h.carts.Get(c.Request.Context(), middleware.UID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load cart"})
		return
	}
	c.JSON(http.StatusOK, cart)
}

// AddItem handles POST /api/cart/items. An existing line's quantity is replaced
// (not incremented); a new product is appended.
func (h *CartHandler) AddItem(c *gin.Context) {
	var req models.CartItem
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.ProductID == "" || req.Qty <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "productId required and qty must be > 0"})
		return
	}

	ctx := c.Request.Context()
	cart, err := h.carts.Get(ctx, middleware.UID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load cart"})
		return
	}

	found := false
	for i := range cart.Items {
		if cart.Items[i].ProductID == req.ProductID && cart.Items[i].VariantID == req.VariantID {
			cart.Items[i].Qty = req.Qty
			found = true
			break
		}
	}
	if !found {
		// Bound the cart document: repeated adds of distinct products must not
		// grow it toward Mongo's document size limit.
		if len(cart.Items) >= maxCartItems {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cart is full"})
			return
		}
		cart.Items = append(cart.Items, req)
	}

	if err := h.carts.Save(ctx, cart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save cart"})
		return
	}
	c.JSON(http.StatusOK, cart)
}

// RemoveItem handles DELETE /api/cart/items/:productId.
func (h *CartHandler) RemoveItem(c *gin.Context) {
	productID := c.Param("productId")
	variantID, specificVariant := c.GetQuery("variantId")

	ctx := c.Request.Context()
	cart, err := h.carts.Get(ctx, middleware.UID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load cart"})
		return
	}

	filtered := make([]models.CartItem, 0, len(cart.Items))
	for _, item := range cart.Items {
		if item.ProductID != productID || specificVariant && item.VariantID != variantID {
			filtered = append(filtered, item)
		}
	}
	cart.Items = filtered

	if err := h.carts.Save(ctx, cart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save cart"})
		return
	}
	c.JSON(http.StatusOK, cart)
}
