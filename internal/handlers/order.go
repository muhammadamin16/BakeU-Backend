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

// OrderHandler serves checkout, order listing and seller status updates.
type OrderHandler struct {
	orders   *repository.OrderRepo
	carts    *repository.CartRepo
	products *repository.ProductRepo
}

func NewOrderHandler(orders *repository.OrderRepo, carts *repository.CartRepo, products *repository.ProductRepo) *OrderHandler {
	return &OrderHandler{orders: orders, carts: carts, products: products}
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

	ctx := c.Request.Context()
	uid := middleware.UID(c)

	// Group order items by seller, resolving each product to a price snapshot.
	itemsBySeller := make(map[string][]models.OrderItem)
	for _, item := range req.Items {
		if item.Qty <= 0 {
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

	now := time.Now().UTC()
	created := make([]models.Order, 0, len(itemsBySeller))
	for sellerID, items := range itemsBySeller {
		var total float64
		for _, it := range items {
			total += it.Price * float64(it.Qty)
		}
		order := &models.Order{
			CustomerID:      uid,
			SellerID:        sellerID,
			Items:           items,
			Total:           total,
			Status:          models.OrderPending,
			DeliveryAddress: req.DeliveryAddress,
			CreatedAt:       now,
		}
		if _, err := h.orders.Create(ctx, order); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
			return
		}
		created = append(created, *order)
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

	order.Status = req.Status
	if err := h.orders.Update(ctx, order); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
		return
	}
	c.JSON(http.StatusOK, order)
}
