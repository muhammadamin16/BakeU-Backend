package router

import (
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
	r := gin.Default()
	r.Use(middleware.CORS())

	// Cap the in-memory portion of multipart uploads; larger parts spill to
	// disk and are still bounded by the per-route BodyLimit middleware below.
	r.MaxMultipartMemory = 8 << 20 // 8 MB

	// Only trust forwarding headers from the platform's edge proxy (Railway
	// fronts the app from a private address). Without this, clients could spoof
	// X-Forwarded-For to evade the login throttle or trigger account lockouts on
	// arbitrary victims. Refine to Railway's actual proxy CIDR if it is known.
	_ = r.SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"})

	r.GET("/healthz", handlers.Health)

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
	authH := handlers.NewAuthHandler(userRepo, jwtm, loginGuard)
	profile := handlers.NewProfileHandler(userRepo, marketRepo)
	product := handlers.NewProductHandler(productRepo, imageStore, marketRepo)
	market := handlers.NewMarketHandler(marketRepo, userRepo, imageStore, mailer,
		cfg.AdminEmails, cfg.AppBaseURL, cfg.MarketReviewSecret)
	image := handlers.NewImageHandler(imageStore)
	cart := handlers.NewCartHandler(cartRepo)
	order := handlers.NewOrderHandler(orderRepo, cartRepo, productRepo)
	review := handlers.NewReviewHandler(reviewRepo)
	dashboard := handlers.NewDashboardHandler(orderRepo, reviewRepo, productRepo, userRepo, cache.New())

	// Public routes (no auth)
	public := r.Group("/api")
	public.Use(middleware.BodyLimit(jsonBodyLimit))
	{
		public.POST("/auth/signup", authH.Signup)
		public.POST("/auth/login", authH.Login)

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
	authed.Use(middleware.AuthRequired(jwtm), middleware.BodyLimit(jsonBodyLimit))
	{
		authed.GET("/me", profile.Me)
		authed.POST("/profile", profile.Upsert)

		authed.GET("/cart", cart.Get)
		authed.POST("/cart/items", cart.AddItem)
		authed.DELETE("/cart/items/:productId", cart.RemoveItem)

		authed.GET("/orders", order.List)
	}

	// Customer-only routes
	customer := r.Group("/api")
	customer.Use(middleware.AuthRequired(jwtm), middleware.RequireRole(userRepo, models.RoleCustomer),
		middleware.BodyLimit(jsonBodyLimit))
	{
		customer.POST("/orders", order.Checkout)
		customer.POST("/products/:id/reviews", review.Create)
	}

	// Seller-only routes. The base group carries the auth + role check; two
	// sub-groups apply different body limits (a single route must not stack two
	// limits, as the smaller inner reader would win).
	seller := r.Group("/api")
	seller.Use(middleware.AuthRequired(jwtm), middleware.RequireRole(userRepo, models.RoleSeller))

	sellerJSON := seller.Group("")
	sellerJSON.Use(middleware.BodyLimit(jsonBodyLimit))
	{
		sellerJSON.GET("/market/mine", market.Mine)

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
