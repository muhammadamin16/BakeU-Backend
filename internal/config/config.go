package config

import (
	"errors"
	"os"
	"strings"
)

// Config holds all runtime configuration, sourced from environment variables.
type Config struct {
	SellerSelfService   bool
	AdminUserIDs        []string
	PhoneAccounts       bool
	TelegramBotUsername string
	TelegramBridgeToken string
	TelegramSiteURL     string
	ListenHost          string
	Port                string // HTTP port to listen on (Cloud Run sets PORT)
	MongoURI            string // MongoDB connection string (Atlas or local)
	MongoDB             string // database name
	JWTSecret           string // HMAC secret used to sign/verify auth tokens

	RefreshSessions bool
	BrowserOrigins  []string

	// Market approval pipeline.
	ResendAPIKey       string   // Resend API key for transactional email
	EmailFrom          string   // verified "from" address, e.g. "Bakeu <noreply@bakeu.app>"
	AdminEmails        []string // who reviews bakery applications
	AppBaseURL         string   // public base URL, used to build review links
	MarketReviewSecret string   // HMAC secret for signing market review links
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() Config {
	return Config{
		SellerSelfService:   os.Getenv("SELLER_SELF_SERVICE") == "true",
		AdminUserIDs:        splitAndTrim(os.Getenv("ADMIN_USER_IDS")),
		PhoneAccounts:       os.Getenv("AUTH_PHONE_ACCOUNTS") == "true",
		TelegramBotUsername: getEnv("TELEGRAM_BOT_USERNAME", "BakeUuzBot"),
		TelegramBridgeToken: os.Getenv("TELEGRAM_BRIDGE_TOKEN"),
		TelegramSiteURL:     getEnv("TELEGRAM_SITE_URL", "https://bakeu-website-production.up.railway.app"),
		ListenHost:          os.Getenv("BAKEU_LISTEN_HOST"),
		Port:                getEnv("PORT", "8080"),
		MongoURI:            strings.TrimSpace(os.Getenv("MONGODB_URI")),
		MongoDB:             strings.TrimSpace(os.Getenv("MONGODB_DB")),
		JWTSecret:           os.Getenv("JWT_SECRET"),
		RefreshSessions:     os.Getenv("AUTH_REFRESH_SESSIONS") == "true",
		BrowserOrigins:      splitAndTrim(os.Getenv("AUTH_BROWSER_ORIGINS")),

		ResendAPIKey:       os.Getenv("RESEND_API_KEY"),
		EmailFrom:          getEnv("EMAIL_FROM", "Bakeu <onboarding@resend.dev>"),
		AdminEmails:        splitAndTrim(os.Getenv("ADMIN_EMAILS")),
		AppBaseURL:         getEnv("APP_BASE_URL", "http://localhost:8080"),
		MarketReviewSecret: getEnv("MARKET_REVIEW_SECRET", os.Getenv("JWT_SECRET")),
	}
}

// ValidateMongoConfig fails before the server attempts to connect.
func (c Config) ValidateMongoConfig() error {
	if c.MongoURI == "" {
		return errors.New("MONGODB_URI is required; set it explicitly")
	}
	if !strings.HasPrefix(c.MongoURI, "mongodb://") && !strings.HasPrefix(c.MongoURI, "mongodb+srv://") {
		return errors.New("MONGODB_URI must use mongodb:// or mongodb+srv://")
	}
	if strings.TrimSpace(c.MongoDB) == "" {
		return errors.New("MONGODB_DB must name the intended database")
	}
	return nil
}

// splitAndTrim splits a comma-separated env value into trimmed, non-empty parts.
func splitAndTrim(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
