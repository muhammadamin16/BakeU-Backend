package router

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"bakeu/internal/auth"
	"bakeu/internal/cache"
	"bakeu/internal/config"
	"bakeu/internal/email"
	"bakeu/internal/handlers"
	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"bakeu/internal/storage"
)

// Request body size limits.
const (
	jsonBodyLimit  = 1 << 20 // 1 MB for JSON requests
	imageBodyLimit = 5 << 20 // 5 MB for image uploads
)

// New builds the HTTP router with all routes registered.
func New(db *mongo.Database, cfg config.Config) *gin.Engine {
	r := gin.New()
	// Approval links carry credentials in query parameters. Avoid logging
	// those parameters or dumping cookies/headers on panic.
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{SkipQueryString: true}))
	r.Use(gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, _ any) {
		log.Printf("request panic: %s %s\n%s", c.Request.Method, c.FullPath(), debug.Stack())
		c.AbortWithStatus(http.StatusInternalServerError)
	}))
	r.Use(middleware.CORS(cfg.BrowserOrigins))

	// Cap the in-memory portion of multipart uploads; larger parts spill to
	// disk and are still bounded by the per-route BodyLimit middleware below.
	r.MaxMultipartMemory = 8 << 20 // 8 MB

	// Only trust forwarding headers from the platform's edge proxy (Railway
	// fronts the app from a private address). Without this, clients could spoof
	// X-Forwarded-For to evade the login throttle or trigger account lockouts on
	// arbitrary victims. Refine to Railway's actual proxy CIDR if it is known.
	_ = r.SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"})

	r.GET("/healthz", handlers.Health)
	r.GET("/readyz", handlers.NewHealthHandler(db.Client()).Ready)

	jwtm := auth.NewManager(cfg.JWTSecret)
	loginGuard := auth.NewLoginGuard()
	mailer := email.NewMailer(cfg.ResendAPIKey, cfg.EmailFrom)

	// Repositories
	userRepo := repository.NewUserRepo(db)
	productRepo := repository.NewProductRepo(db)
	marketRepo := repository.NewMarketRepo(db)
	cartRepo := repository.NewCartRepo(db)
	orderRepo := repository.NewOrderRepo(db)
	reviewRepo := repository.NewReviewRepo(db)
	imageStore := storage.NewStore(db)

	// Handlers
	sessions := repository.NewSessionRepo(db)
	var refreshSessions *repository.SessionRepo
	if cfg.RefreshSessions {
		refreshSessions = sessions
	}
	authH := handlers.NewAuthHandler(userRepo, jwtm, loginGuard, refreshSessions, cfg.BrowserOrigins)
	profile := handlers.NewProfileHandler(userRepo, marketRepo)
	profile.ConfigureCapabilities(cfg.SellerSelfService, cfg.AdminUserIDs)
	product := handlers.NewProductHandler(productRepo, imageStore, marketRepo)
	market := handlers.NewMarketHandler(marketRepo, userRepo, imageStore, mailer,
		cfg.AdminEmails, cfg.AppBaseURL, cfg.MarketReviewSecret)
	image := handlers.NewImageHandler(imageStore)
	cart := handlers.NewCartHandler(cartRepo, productRepo, marketRepo)
	favorites := handlers.NewFavoritesHandler(repository.NewFavoritesRepo(db), productRepo, marketRepo)
	order := handlers.NewOrderHandler(orderRepo, cartRepo, productRepo, marketRepo, userRepo)
	review := handlers.NewReviewHandler(reviewRepo, orderRepo)
	dashboard := handlers.NewDashboardHandler(orderRepo, reviewRepo, productRepo, userRepo, cache.New())

	telegramRepo := repository.NewTelegramRepo(db)
	telegram := handlers.NewTelegramHandler(telegramRepo, userRepo, orderRepo, marketRepo, productRepo, cfg.TelegramSiteURL, cfg.ListenHost == "127.0.0.1")
	bridge := r.Group("/api/integrations/telegram", handlers.TelegramServiceAuth(cfg.TelegramBridgeToken), middleware.BodyLimit(jsonBodyLimit))
	bridge.GET("/me", telegram.Me)
	bridge.GET("/diagnostics", func(c *gin.Context) {
		c.Set("phoneAccounts", cfg.PhoneAccounts)
		c.Set("refreshSessions", cfg.RefreshSessions)
		telegram.Diagnostics(c)
	})
	bridge.POST("/link/challenges", telegram.StartLink)
	bridge.GET("/shop", telegram.Shop)
	bridge.GET("/shop/products", telegram.Products)
	bridge.GET("/orders", telegram.Orders)
	bridge.GET("/orders/:id", telegram.Order)
	bridge.PATCH("/orders/:id/status", telegram.Transition)
	bridge.POST("/notifications/claim", telegram.Claim)
	bridge.POST("/notifications/:id/:action", telegram.LeaseAction)
	if cfg.PhoneAccounts {
		phoneAuth := handlers.NewPhoneAuthHandler(authH, telegramRepo, cfg.TelegramBotUsername)
		phone := r.Group("/api/auth/phone", middleware.BodyLimit(jsonBodyLimit), authH.CheckSessionOrigin)
		phone.POST("/start", phoneAuth.Start)
		phone.POST("/verify", phoneAuth.Verify)
		phone.POST("/telegram", phoneAuth.Exchange)
		bridge.POST("/auth/phone/contact", phoneAuth.Contact)
		bridge.POST("/auth/launch", phoneAuth.Launch)
	}
	// Public routes (no auth)
	public := r.Group("/api")
	public.Use(middleware.BodyLimit(jsonBodyLimit))
	{
		public.POST("/auth/signup", authH.CheckSessionOrigin, func(c *gin.Context) {
			if cfg.PhoneAccounts {
				c.JSON(409, gin.H{"error": "Подтвердите телефон через /auth/phone/start."})
				return
			}
			authH.Signup(c)
		})
		public.POST("/auth/login", authH.CheckSessionOrigin, authH.Login)
		if cfg.RefreshSessions {
			public.POST("/auth/refresh", authH.CheckSessionOrigin, authH.Refresh)
			public.POST("/auth/logout", authH.CheckSessionOrigin, authH.Logout)
		}

		public.GET("/markets", market.ListPublic)
		public.GET("/markets/:id", market.GetPublic)

		public.GET("/products", product.List)
		public.GET("/products/:id", product.GetOne)
		public.GET("/products/:id/reviews", review.ListByProduct)

		public.GET("/images/:id", image.Get)

		// Bakery approval review, opened from the admin email. Guarded by an
		// HMAC token, not app auth. GET only displays; POST applies the decision.
		public.GET("/market/review/:id", market.ReviewPage)
		public.POST("/market/review/:id", market.ReviewDecision)
	}

	// Authenticated routes
	authed := r.Group("/api")
	authed.Use(middleware.AuthRequired(jwtm, sessions), middleware.RequireActive(userRepo), middleware.BodyLimit(jsonBodyLimit))
	{
		authed.GET("/me", profile.Me)
		if cfg.SellerSelfService {
			authed.POST("/market/self-service", market.OpenOwn)
		}
		if cfg.RefreshSessions {
			authed.POST("/auth/logout-all", authH.CheckSessionOrigin, authH.LogoutAll)
		}
		authed.POST("/profile", profile.Upsert)

		authed.GET("/cart", cart.Get)
		authed.GET("/favorites", favorites.Get)
		authed.PUT("/favorites/:id", favorites.Set)
		authed.DELETE("/favorites/:id", favorites.Set)
		authed.DELETE("/favorites", favorites.Clear)
		authed.POST("/cart/items", cart.AddItem)
		authed.DELETE("/cart/items/:productId", cart.RemoveItem)

		authed.GET("/orders", order.List)
		authed.GET("/orders/capabilities", order.Capabilities)
		authed.POST("/me/telegram/link/confirm", telegram.ConfirmLink)
		authed.GET("/me/notifications", telegram.Inbox)
	}

	// Customer-only routes
	customer := r.Group("/api")
	customer.Use(middleware.AuthRequired(jwtm, sessions), middleware.RequireActive(userRepo),
		middleware.BodyLimit(jsonBodyLimit))
	{
		customer.POST("/orders", order.Checkout)
		customer.POST("/products/:id/reviews", review.Create)
	}
	admin := r.Group("/api/admin", middleware.AuthRequired(jwtm, sessions), handlers.RequireVerifiedAdmin(userRepo, cfg.AdminUserIDs), middleware.BodyLimit(jsonBodyLimit))
	adminWorkspace := handlers.NewAdminWorkspace(db)
	admin.GET("/records/:kind", adminWorkspace.Records)
	admin.GET("/sales", adminWorkspace.Sales)
	admin.GET("/markets", market.ListAdmin)
	admin.PATCH("/markets/:id/status", market.Moderate)

	// Seller-only routes. The base group carries the auth + role check; two
	// sub-groups apply different body limits (a single route must not stack two
	// limits, as the smaller inner reader would win).
	seller := r.Group("/api")
	seller.Use(middleware.AuthRequired(jwtm, sessions), middleware.RequireRole(userRepo, models.RoleSeller))

	sellerJSON := seller.Group("")
	sellerJSON.Use(middleware.BodyLimit(jsonBodyLimit))
	{
		sellerJSON.GET("/market/mine", market.Mine)
		sellerJSON.PATCH("/market/mine", market.EditMine)

		sellerJSON.GET("/seller/dashboard", dashboard.Dashboard)

		sellerJSON.POST("/products", product.Create)
		sellerJSON.PUT("/products/:id", product.Update)
		sellerJSON.DELETE("/products/:id", product.Delete)

		sellerJSON.PATCH("/orders/:id/status", order.UpdateStatus)
	}

	sellerUpload := seller.Group("")
	sellerUpload.Use(middleware.BodyLimit(imageBodyLimit))
	{
		// Bakery market application (allowed before approval); carries the logo.
		sellerUpload.POST("/market", market.Apply)
		sellerUpload.POST("/products/:id/image", product.UploadImage)
	}

	return r
}
