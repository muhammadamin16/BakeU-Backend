package handlers

import (
	"errors"
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
	carts    *repository.CartRepo
	products *repository.ProductRepo
	markets  *repository.MarketRepo
}

func NewCartHandler(carts *repository.CartRepo, products *repository.ProductRepo, markets *repository.MarketRepo) *CartHandler {
	return &CartHandler{carts: carts, products: products, markets: markets}
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
	var req struct {
		models.CartItem
		CartRevision *string `json:"cartRevision"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.ProductID == "" || req.Qty <= 0 || req.Qty > maxItemQty {
		c.JSON(http.StatusBadRequest, gin.H{"error": "productId required and qty must be > 0"})
		return
	}

	ctx := c.Request.Context()
	product, err := h.products.Get(ctx, req.ProductID)
	if errors.Is(err, repository.ErrNotFound) || err == nil && !product.Available {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product is unavailable"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "catalog is unavailable"})
		return
	}
	approved, err := h.markets.IsApprovedSeller(ctx, product.SellerID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "catalog is unavailable"})
		return
	}
	if !approved {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product is unavailable"})
		return
	}
	if req.VariantID != "" {
		found := false
		for _, variant := range product.Variants {
			if variant.ID == req.VariantID {
				found = true
				break
			}
		}
		if !found {
			c.JSON(http.StatusBadRequest, gin.H{"error": "product variant is unavailable"})
			return
		}
	}

	cart, err := h.carts.Mutate(ctx, middleware.UID(c), req.CartRevision, func(cart *models.Cart) error {
		for i, item := range cart.Items {
			if item.ProductID == req.ProductID && item.VariantID == req.VariantID {
				cart.Items[i].Qty = req.Qty
				return nil
			}
		}
		if len(cart.Items) >= maxCartItems {
			return errCartFull
		}
		cart.Items = append(cart.Items, req.CartItem)
		return nil
	})
	writeCart(c, cart, err)
}

// RemoveItem handles DELETE /api/cart/items/:productId.
func (h *CartHandler) RemoveItem(c *gin.Context) {
	productID := c.Param("productId")
	variantID, specificVariant := c.GetQuery("variantId")

	var expected *string
	if revision, exists := c.GetQuery("cartRevision"); exists {
		expected = &revision
	}
	cart, err := h.carts.Mutate(c.Request.Context(), middleware.UID(c), expected, func(cart *models.Cart) error {
		filtered := make([]models.CartItem, 0, len(cart.Items))
		for _, item := range cart.Items {
			if item.ProductID != productID || specificVariant && item.VariantID != variantID {
				filtered = append(filtered, item)
			}
		}
		cart.Items = filtered
		return nil
	})
	writeCart(c, cart, err)
}

var errCartFull = errors.New("cart is full")

func writeCart(c *gin.Context, cart *models.Cart, err error) {
	switch {
	case errors.Is(err, errCartFull):
		c.JSON(http.StatusBadRequest, gin.H{"error": "cart is full"})
	case errors.Is(err, repository.ErrCartConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "cart changed; refresh and retry"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save cart"})
	default:
		c.JSON(http.StatusOK, cart)
	}
}
