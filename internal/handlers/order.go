package handlers

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
)

// maxItemQty caps the quantity of a single order line; there is no stock
// tracking, so this is the only bound on absurd orders.
const maxItemQty = 1000

// OrderHandler serves checkout, order listing and seller status updates.
type OrderHandler struct {
	orders   *repository.OrderRepo
	carts    *repository.CartRepo
	products *repository.ProductRepo
	markets  *repository.MarketRepo
}

func NewOrderHandler(orders *repository.OrderRepo, carts *repository.CartRepo,
	products *repository.ProductRepo, markets *repository.MarketRepo) *OrderHandler {
	return &OrderHandler{orders: orders, carts: carts, products: products, markets: markets}
}

type checkoutRequest struct {
	DeliveryAddress string            `json:"deliveryAddress"`
	Items           []models.CartItem `json:"items"`
}

// Checkout handles POST /api/orders (customer only). The cart lives on the
// client, so the request carries the items (final summed qty per product). It
// turns them into one order per seller, snapshotting product title/price.
// Returns the list of created orders.
func (h *OrderHandler) Checkout(c *gin.Context) {
	var req checkoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.DeliveryAddress == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deliveryAddress is required"})
		return
	}
	if len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cart is empty"})
		return
	}
	// Each item costs a product lookup, so bound the list (mirrors maxCartItems).
	if len(req.Items) > maxCartItems {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many items in one order"})
		return
	}

	ctx := c.Request.Context()
	uid := middleware.UID(c)

	// Group order items by seller, resolving each product to a price snapshot.
	itemsBySeller := make(map[string][]models.OrderItem)
	for _, item := range req.Items {
		if item.Qty <= 0 || item.Qty > maxItemQty {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid quantity for product: " + item.ProductID})
			return
		}
		p, err := h.products.Get(ctx, item.ProductID)
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "product no longer available: " + item.ProductID})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load product"})
			return
		}
		if !p.Available {
			c.JSON(http.StatusBadRequest, gin.H{"error": "product is unavailable: " + p.Title})
			return
		}
		itemsBySeller[p.SellerID] = append(itemsBySeller[p.SellerID], models.OrderItem{
			ProductID: p.ID,
			Title:     p.Title,
			Price:     p.Price,
			Qty:       item.Qty,
		})
	}

	// A product only stays purchasable while its bakery is approved; listings
	// hide unapproved markets, but checkout must enforce it too.
	for sellerID := range itemsBySeller {
		approved, err := h.markets.IsApprovedSeller(ctx, sellerID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check seller"})
			return
		}
		if !approved {
			c.JSON(http.StatusBadRequest, gin.H{"error": "one of the bakeries is no longer available"})
			return
		}
	}

	now := time.Now().UTC()
	orders := make([]*models.Order, 0, len(itemsBySeller))
	for sellerID, items := range itemsBySeller {
		var total float64
		for _, it := range items {
			total += it.Price * float64(it.Qty)
		}
		orders = append(orders, &models.Order{
			CustomerID:      uid,
			SellerID:        sellerID,
			Items:           items,
			Total:           total,
			Status:          models.OrderPending,
			DeliveryAddress: req.DeliveryAddress,
			CreatedAt:       now,
			UpdatedAt:       &now,
			History:         []models.OrderEvent{{Status: models.OrderPending, At: now, ActorID: uid}},
		})
	}

	// All orders are created in one transaction: a multi-seller checkout either
	// fully succeeds or fails without leaving partial orders for a retry to
	// duplicate.
	if err := h.orders.CreateAll(ctx, orders); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
		return
	}

	// The purchase went through; the server-side cart copy is now stale.
	// Best-effort: a failure here must not fail the checkout.
	if err := h.carts.Clear(ctx, uid); err != nil {
		log.Printf("[order] clear cart for %s: %v", uid, err)
	}

	created := make([]models.Order, 0, len(orders))
	for _, o := range orders {
		created = append(created, *o)
	}
	c.JSON(http.StatusCreated, gin.H{"orders": created})
}

// List handles GET /api/orders — returns every order the caller is involved in,
// whether as the customer who placed it or the seller fulfilling it.
func (h *OrderHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	uid := middleware.UID(c)

	asCustomer, err := h.orders.ListByCustomer(ctx, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list orders"})
		return
	}
	asSeller, err := h.orders.ListBySeller(ctx, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list orders"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"placed":   asCustomer,
		"incoming": asSeller,
	})
}

type statusRequest struct {
	Status models.OrderStatus `json:"status"`
}

// UpdateStatus handles PATCH /api/orders/:id/status (seller only, owner only).
func (h *OrderHandler) UpdateStatus(c *gin.Context) {
	var req statusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !models.ValidOrderStatus(req.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order status"})
		return
	}

	ctx := c.Request.Context()
	order, err := h.orders.Get(ctx, c.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load order"})
		return
	}
	if order.SellerID != middleware.UID(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this order"})
		return
	}
	if !models.CanTransition(order.Status, req.Status) {
		c.JSON(http.StatusConflict, gin.H{
			"error": "cannot change status from '" + string(order.Status) + "' to '" + string(req.Status) + "'",
		})
		return
	}

	updated, err := h.orders.Transition(ctx, order.ID, order.SellerID, order.Status, req.Status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
		return
	}
	if updated == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "order status changed; reload and try again"})
		return
	}
	c.JSON(http.StatusOK, updated)
}
