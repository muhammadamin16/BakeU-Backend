package handlers

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"bakeu/internal/identity"
	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"github.com/gin-gonic/gin"
)

const maxItemQty = 1000

type OrderHandler struct {
	orders   *repository.OrderRepo
	carts    *repository.CartRepo
	products *repository.ProductRepo
	markets  *repository.MarketRepo
	users    *repository.UserRepo
}

func NewOrderHandler(o *repository.OrderRepo, c *repository.CartRepo, p *repository.ProductRepo, m *repository.MarketRepo, users ...*repository.UserRepo) *OrderHandler {
	h := &OrderHandler{orders: o, carts: c, products: p, markets: m}
	if len(users) > 0 {
		h.users = users[0]
	}
	return h
}

type deliveryRequest struct {
	Fulfillment     string              `json:"fulfillment"`
	DeliveryAddress string              `json:"deliveryAddress"`
	Location        *models.Coordinates `json:"location"`
	ScheduledAt     *time.Time          `json:"scheduledAt"`
	Comment         string              `json:"comment"`
}
type checkoutRequest struct {
	deliveryRequest
	Items      []models.CartItem          `json:"items"`
	Deliveries map[string]deliveryRequest `json:"deliveries"`
}

func validKey(key string) bool {
	if len(key) < 1 || len(key) > 100 {
		return false
	}
	for _, v := range key {
		if !(v >= 'a' && v <= 'z' || v >= 'A' && v <= 'Z' || v >= '0' && v <= '9' || v == ':' || v == '-' || v == '_') {
			return false
		}
	}
	return true
}
func validateDelivery(d *deliveryRequest, v2 bool, now time.Time) bool {
	if d.Fulfillment == "" {
		d.Fulfillment = "delivery"
	}
	if d.Fulfillment != "pickup" && d.Fulfillment != "delivery" {
		return false
	}
	d.DeliveryAddress = strings.TrimSpace(d.DeliveryAddress)
	d.Comment = strings.TrimSpace(d.Comment)
	if utf8.RuneCountInString(d.Comment) > 1000 || utf8.RuneCountInString(d.DeliveryAddress) > 500 {
		return false
	}
	if d.Fulfillment == "pickup" {
		d.DeliveryAddress = ""
		d.Location = nil
	} else if d.DeliveryAddress == "" {
		return false
	}
	if d.Location != nil && (math.IsNaN(d.Location.Latitude) || math.IsNaN(d.Location.Longitude) || math.IsInf(d.Location.Latitude, 0) || math.IsInf(d.Location.Longitude, 0) || math.Abs(d.Location.Latitude) > 90 || math.Abs(d.Location.Longitude) > 180) {
		return false
	}
	if v2 && (d.ScheduledAt == nil || !d.ScheduledAt.After(now) || d.ScheduledAt.After(now.Add(366*24*time.Hour))) {
		return false
	}
	return true
}
func orderError(c *gin.Context, err error) {
	status := http.StatusServiceUnavailable
	message := "Сервис заказов временно недоступен."
	if errors.Is(err, repository.ErrForbidden) {
		status = 403
		message = "Недостаточно прав."
	}
	if errors.Is(err, repository.ErrConflict) {
		status = 409
		message = "Данные изменились. Обновите заказ."
	}
	if errors.Is(err, repository.ErrNotFound) {
		status = 404
		message = "Заказ не найден."
	}
	c.JSON(status, gin.H{"error": message})
}
func (h *OrderHandler) current(c *gin.Context) (*models.User, error) {
	if h.users == nil {
		return nil, repository.ErrForbidden
	}
	u, e := h.users.Get(c.Request.Context(), middleware.UID(c))
	if e != nil {
		return nil, e
	}
	if !u.Active() {
		return nil, repository.ErrForbidden
	}
	return u, nil
}
func (h *OrderHandler) Checkout(c *gin.Context) {
	var req checkoutRequest
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"error": "Неверные данные заказа."})
		return
	}
	u, e := h.current(c)
	if e != nil {
		orderError(c, e)
		return
	}
	if u.Role != models.RoleCustomer {
		orderError(c, repository.ErrForbidden)
		return
	}
	if len(req.Items) == 0 || len(req.Items) > maxCartItems {
		c.JSON(400, gin.H{"error": "Проверьте корзину."})
		return
	}
	key := c.GetHeader("Idempotency-Key")
	v2 := req.Fulfillment != "" || req.ScheduledAt != nil || req.Deliveries != nil
	if v2 && !validKey(key) {
		c.JSON(400, gin.H{"error": "Idempotency-Key is required"})
		return
	}
	if key == "" {
		key = "legacy-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	hash := repository.RequestHash(req)
	ctx := c.Request.Context()
	if previous, err := h.orders.ReplayCheckout(ctx, u.UID, key, hash); err == nil {
		c.JSON(200, gin.H{"orders": previous})
		return
	} else if !errors.Is(err, repository.ErrNotFound) {
		orderError(c, err)
		return
	}
	buyerPhone, e := identity.NormalizePhone(u.Phone)
	if e != nil {
		c.JSON(400, gin.H{"error": "Укажите международный телефон в профиле."})
		return
	}
	quantities := map[string]int{}
	for _, item := range req.Items {
		quantities[item.ProductID] += item.Qty
		if item.Qty < 1 || quantities[item.ProductID] > maxItemQty {
			c.JSON(400, gin.H{"error": "Неверное количество товара."})
			return
		}
	}
	itemsBySeller := map[string][]models.OrderItem{}
	ids := []string{}
	for id := range quantities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p, err := h.products.Get(ctx, id)
		if err != nil {
			orderError(c, err)
			return
		}
		if !p.Available || p.Price < 0 || p.Price > 1e12 || math.IsNaN(p.Price) || math.IsInf(p.Price, 0) {
			c.JSON(400, gin.H{"error": "Товар недоступен."})
			return
		}
		itemsBySeller[p.SellerID] = append(itemsBySeller[p.SellerID], models.OrderItem{ProductID: p.ID, Title: p.Title, Price: p.Price, Qty: quantities[id]})
	}
	sellers := []string{}
	for seller := range itemsBySeller {
		sellers = append(sellers, seller)
	}
	sort.Strings(sellers)
	now := time.Now().UTC()
	orders := []*models.Order{}
	used := map[string]bool{}
	for _, seller := range sellers {
		m, err := h.markets.GetBySeller(ctx, seller)
		if err != nil {
			orderError(c, err)
			return
		}
		sellerUser, err := h.users.Get(ctx, seller)
		if err != nil {
			orderError(c, err)
			return
		}
		if !sellerUser.Active() || sellerUser.Role != models.RoleSeller {
			orderError(c, repository.ErrForbidden)
			return
		}
		if m.Status != models.MarketApproved {
			c.JSON(400, gin.H{"error": "Магазин сейчас недоступен."})
			return
		}
		d := req.deliveryRequest
		if req.Deliveries != nil {
			var found bool
			d, found = req.Deliveries[m.ID]
			if !found {
				c.JSON(400, gin.H{"error": "Укажите получение для каждого магазина."})
				return
			}
			used[m.ID] = true
		}
		if !validateDelivery(&d, v2, now) {
			c.JSON(400, gin.H{"error": "Проверьте адрес, координаты и будущее время получения."})
			return
		}
		if (d.Fulfillment == "pickup" && m.Fulfillment == models.FulfillmentDelivery) || (d.Fulfillment == "delivery" && m.Fulfillment == models.FulfillmentPickup) {
			c.JSON(400, gin.H{"error": "Магазин не поддерживает выбранный способ получения."})
			return
		}
		total := 0.0
		for _, item := range itemsBySeller[seller] {
			total += item.Price * float64(item.Qty)
		}
		if total > 1e12 {
			c.JSON(400, gin.H{"error": "Сумма заказа слишком велика."})
			return
		}
		workflow := 1
		if v2 {
			workflow = 2
		}
		orders = append(orders, &models.Order{CustomerID: u.UID, SellerID: seller, ShopID: m.ID, ShopName: m.Name, BuyerName: u.Name, BuyerPhone: buyerPhone, Items: itemsBySeller[seller], Total: total, Status: models.OrderPending, DeliveryAddress: d.DeliveryAddress, Fulfillment: d.Fulfillment, Location: d.Location, ScheduledAt: d.ScheduledAt, Comment: d.Comment, CreatedAt: now, WorkflowVersion: workflow})
	}
	if len(used) != len(req.Deliveries) {
		c.JSON(400, gin.H{"error": "Получение указано для постороннего магазина."})
		return
	}
	created, e := h.orders.CreateCommitted(ctx, u.UID, key, hash, orders)
	if e != nil {
		orderError(c, e)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"orders": created})
}
func (h *OrderHandler) List(c *gin.Context) {
	u, e := h.current(c)
	if e != nil {
		orderError(c, e)
		return
	}
	ctx := c.Request.Context()
	placed := []models.Order{}
	incoming := []models.Order{}
	if u.Role == models.RoleCustomer {
		placed, e = h.orders.ListByCustomer(ctx, u.UID)
	} else {
		incoming, e = h.orders.ListBySeller(ctx, u.UID)
	}
	if e != nil {
		orderError(c, e)
		return
	}
	if placed == nil {
		placed = []models.Order{}
	}
	if incoming == nil {
		incoming = []models.Order{}
	}
	for i := range placed {
		if e = h.orders.Hydrate(ctx, &placed[i]); e != nil {
			orderError(c, e)
			return
		}
	}
	for i := range incoming {
		if e = h.orders.Hydrate(ctx, &incoming[i]); e != nil {
			orderError(c, e)
			return
		}
	}
	c.JSON(200, gin.H{"placed": placed, "incoming": incoming})
}

type statusRequest struct {
	Status          models.OrderStatus `json:"status"`
	ExpectedStatus  models.OrderStatus `json:"expectedStatus"`
	ExpectedVersion *int               `json:"expectedVersion"`
}

func (h *OrderHandler) UpdateStatus(c *gin.Context) {
	var req statusRequest
	if c.ShouldBindJSON(&req) != nil || !models.ValidOrderStatus(req.Status) {
		c.JSON(400, gin.H{"error": "Неверный статус."})
		return
	}
	u, e := h.current(c)
	if e != nil {
		orderError(c, e)
		return
	}
	if u.Role != models.RoleSeller {
		orderError(c, repository.ErrForbidden)
		return
	}
	if req.ExpectedStatus == "" {
		o, err := h.orders.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			orderError(c, err)
			return
		}
		if o.WorkflowVersion >= 2 {
			c.JSON(400, gin.H{"error": "expectedStatus and Idempotency-Key are required"})
			return
		}
		req.ExpectedStatus = o.Status
	}
	key := c.GetHeader("Idempotency-Key")
	if !validKey(key) {
		c.JSON(400, gin.H{"error": "Idempotency-Key is required"})
		return
	}
	changed, e := h.orders.TransitionCommitted(c.Request.Context(), c.Param("id"), u.UID, req.ExpectedStatus, req.Status, key, req.ExpectedVersion)
	if e != nil {
		orderError(c, e)
		return
	}
	c.JSON(200, changed)
}
func (h *OrderHandler) Capabilities(c *gin.Context) {
	c.JSON(200, gin.H{"workflowVersion": 2, "idempotency": true, "history": true, "deliveryScheduling": true})
}
