package handlers

import (
	"errors"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"bakeu/internal/identity"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/crypto/bcrypt"
)

type PhoneAuthHandler struct {
	auth *AuthHandler
	repo *repository.TelegramRepo
	bot  string
}

const phoneAccountEmailSuffix = "@phone.bakeu.invalid"

var errInvalidPhoneCredentials = errors.New("invalid phone signup credentials")

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
		name, market := strings.TrimSpace(req.Name), strings.TrimSpace(req.MarketName)
		if !req.Role.Valid() || len([]rune(name)) < 2 || len([]rune(name)) > 120 || len([]rune(req.Address)) > 500 ||
			(req.Role == models.RoleSeller && (len([]rune(market)) < 2 || len([]rune(market)) > 120)) {
			c.JSON(400, gin.H{"error": "Проверьте имя, роль и название магазина."})
			return
		}
		email, hash, err := phoneSignupCredentials(req.Email, req.Password)
		if err != nil {
			c.JSON(400, gin.H{"error": "Проверьте email и пароль (8–72 байт)."})
			return
		}
		draft = &models.User{Email: email, PasswordHash: hash, Name: name, Phone: phone, Address: strings.TrimSpace(req.Address), Role: req.Role}
	}
	token, expiry, err := h.repo.StartPhoneProof(c.Request.Context(), req.Purpose, phone, strings.TrimSpace(req.MarketName), draft)
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(201, gin.H{"id": token, "expiresAt": expiry, "telegramUrl": "https://t.me/" + url.PathEscape(h.bot) + "?start=goauth_" + token})
}

// Phone-only accounts retain a unique email index key without a usable email or password login.
func phoneSignupCredentials(email, password string) (string, string, error) {
	email = normalizeEmail(email)
	if email == "" && password == "" {
		return bson.NewObjectID().Hex() + phoneAccountEmailSuffix, "", nil
	}
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email ||
		len(email) > 254 || strings.HasSuffix(email, phoneAccountEmailSuffix) ||
		len(password) < minPasswordLen || len(password) > maxPasswordLen {
		return "", "", errInvalidPhoneCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return email, string(hash), err
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

func (h *PhoneAuthHandler) Launch(c *gin.Context) {
	actor := c.GetHeader("X-Telegram-User-Id")
	if !telegramActorRE.MatchString(actor) {
		c.JSON(403, gin.H{"error": "private Telegram identity required"})
		return
	}
	if h.auth.sessions == nil {
		c.JSON(503, gin.H{"error": "Go refresh sessions are required"})
		return
	}
	ticket, uid, err := h.repo.NewLoginTicket(c.Request.Context(), actor)
	if err != nil {
		orderError(c, err)
		return
	}
	if ticket == "" {
		c.JSON(200, gin.H{"linked": false})
		return
	}
	c.JSON(200, gin.H{"linked": true, "ticket": ticket, "uid": uid})
}

func (h *PhoneAuthHandler) Exchange(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	mode := c.GetHeader("X-BakeU-Session")
	if mode != browserSession && mode != "refresh" {
		c.JSON(400, gin.H{"error": "a revocable session mode is required"})
		return
	}
	if h.auth.sessions == nil {
		c.JSON(503, gin.H{"error": "Go refresh sessions are required"})
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
	}
	if c.ShouldBindJSON(&req) != nil || !challengeRE.MatchString(req.Ticket) {
		c.JSON(400, gin.H{"error": "invalid launch ticket"})
		return
	}
	u, err := h.repo.ConsumeLoginTicket(c.Request.Context(), req.Ticket)
	if err != nil {
		orderError(c, err)
		return
	}
	h.auth.issue(c, 200, u)
}
