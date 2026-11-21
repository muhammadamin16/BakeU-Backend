package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bakeu/internal/auth"
	"bakeu/internal/identity"
	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

var telegramActorRE = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var challengeRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

type TelegramHandler struct {
	repo     *repository.TelegramRepo
	users    *repository.UserRepo
	orders   *repository.OrderRepo
	markets  *repository.MarketRepo
	products *repository.ProductRepo
	guard    *auth.LoginGuard
	siteURL  string
}

func NewTelegramHandler(r *repository.TelegramRepo, u *repository.UserRepo, o *repository.OrderRepo, m *repository.MarketRepo, p *repository.ProductRepo, site string) *TelegramHandler {
	return &TelegramHandler{repo: r, users: u, orders: o, markets: m, products: p, guard: auth.NewLoginGuard(), siteURL: site}
}
func TelegramServiceAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		a := sha256.Sum256([]byte(provided))
		b := sha256.Sum256([]byte(secret))
		if len(secret) < 32 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(a[:], b[:]) != 1 || c.GetHeader("Origin") != "" {
			c.AbortWithStatusJSON(401, gin.H{"error": "invalid service credential"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
func (h *TelegramHandler) actor(c *gin.Context) (string, bool) {
	actor := c.GetHeader("X-Telegram-User-Id")
	if !telegramActorRE.MatchString(actor) {
		c.JSON(403, gin.H{"error": "private Telegram identity required"})
		return "", false
	}
	return actor, true
}
func (h *TelegramHandler) who(c *gin.Context, role string) (*repository.TelegramIdentity, bool) {
	actor, ok := h.actor(c)
	if !ok {
		return nil, false
	}
	who, e := h.repo.Identity(c.Request.Context(), actor)
	if e != nil {
		orderError(c, e)
		return nil, false
	}
	if who == nil {
		c.JSON(401, gin.H{"error": "confirmed account binding required"})
		return nil, false
	}
	if role != "" && who.User.Role != role && !(role == "CLIENT" && who.User.CanBuy) {
		orderError(c, repository.ErrForbidden)
		return nil, false
	}
	return who, true
}
func (h *TelegramHandler) Me(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	who, e := h.repo.Identity(c.Request.Context(), actor)
	if e != nil {
		orderError(c, e)
		return
	}
	if who == nil {
		c.JSON(200, gin.H{"linked": false})
		return
	}
	c.JSON(200, who)
}

// Diagnostics is service-only and read-only. It contains counts/capabilities,
// never user records, connection strings, bridge credentials or message bodies.
func (h *TelegramHandler) Diagnostics(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	storage, err := h.repo.StorageReadiness(ctx)
	if err != nil {
		c.JSON(503, gin.H{"code": storage.Code, "storage": storage, "error": "Database readiness unavailable"})
		return
	}
	counts, err := h.repo.DiagnosticCounts(ctx)
	if err != nil {
		c.JSON(503, gin.H{"code": "DATABASE_READ_DENIED", "error": "Database inventory unavailable"})
		return
	}
	c.JSON(200, gin.H{"bridgeVersion": 1, "databaseConnected": true, "transactionsAvailable": storage.Ready, "storage": storage, "phoneAccounts": c.GetBool("phoneAccounts"), "refreshSessions": c.GetBool("refreshSessions"), "counts": counts})
}
func (h *TelegramHandler) StartLink(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	var req struct {
		Phone string `json:"phone"`
		Role  string `json:"requestedRole"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"error": "invalid contact"})
		return
	}
	phone, e := identity.NormalizePhone(req.Phone)
	if e != nil || (req.Role != "CLIENT" && req.Role != "SELLER") {
		c.JSON(400, gin.H{"error": "invalid phone or role"})
		return
	}
	role := models.RoleCustomer
	if req.Role == "SELLER" {
		role = models.RoleSeller
	}
	token, expiry, e := h.repo.Challenge(c.Request.Context(), actor, phone, role)
	if e != nil {
		orderError(c, e)
		return
	}
	site, e := url.Parse(h.siteURL)
	if e != nil || site.Host == "" || site.Scheme != "https" || site.User != nil || site.RawQuery != "" {
		c.JSON(503, gin.H{"error": "Telegram website URL is not configured"})
		return
	}
	site.Fragment = "/telegram/link?challenge=" + token
	c.JSON(200, gin.H{"id": token, "expiresAt": expiry, "confirmationUrl": site.String()})
}
func (h *TelegramHandler) ConfirmLink(c *gin.Context) {
	var req struct {
		Challenge string `json:"challengeId"`
		Password  string `json:"password"`
	}
	if c.ShouldBindJSON(&req) != nil || !challengeRE.MatchString(req.Challenge) || len(req.Password) > 72 || len(req.Password) < 8 {
		c.JSON(400, gin.H{"error": "Проверьте ссылку и пароль."})
		return
	}
	uid := middleware.UID(c)
	key := "telegram-proof:" + uid
	if ok, _ := h.guard.Check(c.ClientIP(), key); !ok {
		c.JSON(429, gin.H{"error": "Слишком много попыток. Повторите позже."})
		return
	}
	u, e := h.users.Get(c.Request.Context(), uid)
	if e != nil {
		orderError(c, e)
		return
	}
	if !u.Active() || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		h.guard.RecordFailure(c.ClientIP(), key)
		c.JSON(403, gin.H{"error": "Подтверждение аккаунта не выполнено."})
		return
	}

	if e = h.repo.Confirm(c.Request.Context(), req.Challenge, uid); e != nil {
		if errors.Is(e, repository.ErrConflict) {
			c.JSON(409, gin.H{"error": "Ссылка недействительна или аккаунт уже привязан. Вернитесь в бот для проверки."})
			return
		}
		if errors.Is(e, repository.ErrForbidden) {
			c.JSON(403, gin.H{"error": "Телефон или роль аккаунта не совпадает с подтверждённым контактом."})
			return
		}
		orderError(c, e)
		return
	}

	h.guard.Reset(c.ClientIP(), key)
	c.JSON(200, gin.H{"linked": true, "confirmed": true})
}
func (h *TelegramHandler) Shop(c *gin.Context) {
	who, ok := h.who(c, "SELLER")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	m, e := h.markets.GetBySeller(ctx, who.User.ID)
	if e != nil {
		orderError(c, e)
		return
	}
	orders, e := h.orders.ListBySeller(ctx, who.User.ID)
	if e != nil {
		orderError(c, e)
		return
	}
	counts := map[models.OrderStatus]int{}
	for _, s := range []models.OrderStatus{models.OrderPending, models.OrderAccepted, models.OrderPreparing, models.OrderReady, models.OrderDelivering, models.OrderDelivered, models.OrderCompleted, models.OrderCancelled, models.OrderRejected} {
		counts[s] = 0
	}
	revenue := 0.0
	for _, o := range orders {
		o.Project()
		counts[o.Status]++
		if o.Status == models.OrderCompleted || o.Status == models.OrderDelivered {
			revenue += o.Total
		}
	}
	n, e := h.products.CountBySeller(ctx, who.User.ID)
	if e != nil {
		orderError(c, e)
		return
	}
	c.JSON(200, gin.H{"id": m.ID, "ownerId": who.User.ID, "name": m.Name, "phone": m.Phone, "address": m.Address, "productCount": n, "revenue": revenue, "counts": counts})
}
func (h *TelegramHandler) Products(c *gin.Context) {
	who, ok := h.who(c, "SELLER")
	if !ok {
		return
	}
	items, e := h.products.List(c.Request.Context(), "", who.User.ID, "", nil)
	if e != nil {
		orderError(c, e)
		return
	}
	if items == nil {
		items = []models.Product{}
	}
	c.JSON(200, gin.H{"ownerId": who.User.ID, "shopId": who.ShopID, "items": items})
}
func (h *TelegramHandler) Orders(c *gin.Context) {
	aud := c.Query("audience")
	role := "CLIENT"
	if aud == "seller" {
		role = "SELLER"
	} else if aud != "buyer" {
		c.JSON(400, gin.H{"error": "invalid audience"})
		return
	}
	who, ok := h.who(c, role)
	if !ok {
		return
	}
	page, e := strconv.Atoi(c.DefaultQuery("page", "0"))
	if e != nil || page < 0 || page > 99999 {
		c.JSON(400, gin.H{"error": "invalid page"})
		return
	}
	orders, next, e := h.orders.Page(c.Request.Context(), who.User.ID, aud == "seller", c.DefaultQuery("view", "all"), page)
	if e != nil {
		orderError(c, e)
		return
	}
	c.JSON(200, gin.H{"orders": orders, "nextPage": next})
}
func telegramOwns(who *repository.TelegramIdentity, o *models.Order) bool {
	return who.User.CanBuy && o.CustomerID == who.User.ID || who.User.Role == "SELLER" && o.SellerID == who.User.ID && o.ShopID == who.ShopID
}
func (h *TelegramHandler) Order(c *gin.Context) {
	who, ok := h.who(c, "")
	if !ok {
		return
	}
	o, e := h.orders.Get(c.Request.Context(), c.Param("id"))
	if e != nil {
		orderError(c, e)
		return
	}
	if e = h.orders.Hydrate(c.Request.Context(), o); e != nil {
		orderError(c, e)
		return
	}
	if !telegramOwns(who, o) {
		orderError(c, repository.ErrForbidden)
		return
	}
	c.JSON(200, o)
}
func (h *TelegramHandler) Transition(c *gin.Context) {
	who, ok := h.who(c, "SELLER")
	if !ok {
		return
	}
	var req statusRequest
	if c.ShouldBindJSON(&req) != nil || req.ExpectedStatus == "" || !validKey(c.GetHeader("Idempotency-Key")) {
		c.JSON(400, gin.H{"error": "expectedStatus and Idempotency-Key required"})
		return
	}
	o, e := h.orders.Get(c.Request.Context(), c.Param("id"))
	if e != nil {
		orderError(c, e)
		return
	}
	if e = h.orders.Hydrate(c.Request.Context(), o); e != nil {
		orderError(c, e)
		return
	}
	if !telegramOwns(who, o) {
		orderError(c, repository.ErrForbidden)
		return
	}
	changed, e := h.orders.TransitionCommitted(c.Request.Context(), o.ID, who.User.ID, req.ExpectedStatus, req.Status, c.GetHeader("Idempotency-Key"), nil)
	if e != nil {
		orderError(c, e)
		return
	}
	c.JSON(200, changed)
}
func (h *TelegramHandler) Inbox(c *gin.Context) {
	u, e := h.users.Get(c.Request.Context(), middleware.UID(c))
	if e != nil {
		orderError(c, e)
		return
	}
	if !u.Active() {
		orderError(c, repository.ErrForbidden)
		return
	}
	items, e := h.repo.Inbox(c.Request.Context(), u.UID)
	if e != nil {
		orderError(c, e)
		return
	}
	c.JSON(200, gin.H{"items": items})
}
func (h *TelegramHandler) Claim(c *gin.Context) {
	jobs, e := h.repo.Claim(c.Request.Context())
	if e != nil {
		orderError(c, e)
		return
	}
	c.JSON(200, gin.H{"jobs": jobs})
}

type leaseRequest struct {
	Lease     string    `json:"leaseToken"`
	MessageID int64     `json:"messageId"`
	State     string    `json:"state"`
	Code      int       `json:"code"`
	RetryAt   time.Time `json:"retryAt"`
}

func (h *TelegramHandler) LeaseAction(c *gin.Context) {
	var req leaseRequest
	if c.ShouldBindJSON(&req) != nil || !challengeRE.MatchString(req.Lease) {
		c.JSON(400, gin.H{"error": "invalid lease"})
		return
	}
	id := c.Param("id")
	var e error
	switch c.Param("action") {
	case "begin":
		var started bool
		started, e = h.repo.Begin(c.Request.Context(), id, req.Lease)
		if e == nil {
			c.JSON(200, gin.H{"started": started})
			return
		}
	case "ack":
		if req.MessageID <= 0 {
			c.JSON(400, gin.H{"error": "invalid Telegram receipt"})
			return
		}
		e = h.repo.Ack(c.Request.Context(), id, req.Lease, req.MessageID)
	case "fail":
		e = h.repo.Fail(c.Request.Context(), id, req.Lease, req.State, req.Code, req.RetryAt)
	default:
		e = repository.ErrNotFound
	}
	if e != nil {
		orderError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
