package handlers

import (
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"bakeu/internal/identity"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type PhoneAuthHandler struct {
	auth *AuthHandler
	repo *repository.TelegramRepo
	bot  string
}

var phoneProofRE = regexp.MustCompile(`^[a-f0-9]{48}$`)

func NewPhoneAuthHandler(auth *AuthHandler, repo *repository.TelegramRepo, bot string) *PhoneAuthHandler {
	return &PhoneAuthHandler{auth: auth, repo: repo, bot: bot}
}

func (h *PhoneAuthHandler) Start(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`).MatchString(h.bot) {
		c.JSON(503, gin.H{"error": "Telegram verification is not configured"})
		return
	}
	var req struct {
		signupRequest
		Purpose    string `json:"purpose"`
		MarketName string `json:"marketName"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"error": "invalid request"})
		return
	}
	phone, err := identity.NormalizePhone(req.Phone)
	if err != nil || (req.Purpose != "register" && req.Purpose != "login") {
		c.JSON(400, gin.H{"error": "invalid phone or purpose"})
		return
	}
	key := "phone-start:" + phone
	if ok, _ := h.auth.guard.Check(c.ClientIP(), key); !ok {
		c.JSON(429, gin.H{"error": "Попробуйте позже."})
		return
	}
	h.auth.guard.RecordFailure(c.ClientIP(), key) // Successful starts also count against request rate.
	var draft *models.User
	if req.Purpose == "register" {
		email := normalizeEmail(req.Email)
		addr, e := mail.ParseAddress(email)
		if e != nil || addr.Address != email || len(email) > 254 || !req.Role.Valid() || len(req.Password) < minPasswordLen || len(req.Password) > maxPasswordLen || len(strings.TrimSpace(req.Name)) < 2 || len(req.Name) > 120 || len(req.Address) > 500 || (req.Role == models.RoleSeller && (len(strings.TrimSpace(req.MarketName)) < 2 || len(req.MarketName) > 120)) {
			c.JSON(400, gin.H{"error": "Проверьте имя, email, пароль (8–72 байт), роль и название магазина."})
			return
		}
		hash, e := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if e != nil {
			c.JSON(503, gin.H{"error": "registration unavailable"})
			return
		}
		draft = &models.User{Email: email, PasswordHash: string(hash), Name: strings.TrimSpace(req.Name), Phone: phone, Address: strings.TrimSpace(req.Address), Role: req.Role}
	}
	token, expiry, err := h.repo.StartPhoneProof(c.Request.Context(), req.Purpose, phone, strings.TrimSpace(req.MarketName), draft)
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(201, gin.H{"id": token, "expiresAt": expiry, "telegramUrl": "https://t.me/" + url.PathEscape(h.bot) + "?start=goauth_" + token})
}

func (h *PhoneAuthHandler) Verify(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	if c.ShouldBindJSON(&req) != nil || !phoneProofRE.MatchString(req.ID) || !regexp.MustCompile(`^[0-9]{6}$`).MatchString(req.Code) {
		c.JSON(400, gin.H{"error": "Проверьте код подтверждения."})
		return
	}
	key := "phone-code:" + req.ID
	if ok, _ := h.auth.guard.Check(c.ClientIP(), key); !ok {
		c.JSON(429, gin.H{"error": "Попробуйте позже."})
		return
	}
	h.auth.guard.RecordFailure(c.ClientIP(), key)
	u, err := h.repo.FinishPhoneProof(c.Request.Context(), req.ID, req.Code)
	if err != nil {
		orderError(c, err)
		return
	}
	h.auth.guard.Reset(c.ClientIP(), key)
	h.auth.issue(c, 200, u)
}

// Contact is service-authenticated; the bot must reject forwarded/foreign contacts.
func (h *PhoneAuthHandler) Contact(c *gin.Context) {
	actor := c.GetHeader("X-Telegram-User-Id")
	var req struct {
		ID    string `json:"id"`
		Phone string `json:"phone"`
	}
	if !telegramActorRE.MatchString(actor) {
		c.JSON(403, gin.H{"error": "private Telegram identity required"})
		return
	}
	if c.ShouldBindJSON(&req) != nil || !phoneProofRE.MatchString(req.ID) {
		c.JSON(400, gin.H{"error": "invalid challenge"})
		return
	}
	phone, err := identity.NormalizePhone(req.Phone)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid phone"})
		return
	}
	code, err := h.repo.IssuePhoneCode(c.Request.Context(), req.ID, actor, phone)
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(200, gin.H{"code": code})
}
