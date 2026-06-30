package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"bakeu/internal/cache"
	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
)

// errInvalidMonth is returned when the ?month= param is neither a 1-12 number
// nor a recognized month name.
var errInvalidMonth = errors.New("invalid month (use a name like 'February' or a number 1-12)")

// Dashboard tunables.
const (
	// dashboardTimeout caps every query the dashboard issues. The aggregations
	// are heavier than the rest of the API, so a tighter ceiling is applied.
	dashboardTimeout = 5 * time.Second
	// cacheTTL is how long the heavy (revenue / rating / products / monthly)
	// sections are cached. Active orders are always computed live.
	cacheTTL = 3 * time.Minute
	// tashkentOffset is the fixed UTC+5 offset used to bucket orders into local
	// calendar days. A FixedZone avoids depending on tzdata in the container.
	tashkentOffset = 5 * 60 * 60
)

// DashboardHandler powers the seller analytics dashboard.
type DashboardHandler struct {
	orders   *repository.OrderRepo
	reviews  *repository.ReviewRepo
	products *repository.ProductRepo
	users    *repository.UserRepo
	cache    *cache.Cache
}

func NewDashboardHandler(orders *repository.OrderRepo, reviews *repository.ReviewRepo,
	products *repository.ProductRepo, users *repository.UserRepo, c *cache.Cache) *DashboardHandler {
	return &DashboardHandler{orders: orders, reviews: reviews, products: products, users: users, cache: c}
}

type chartPoint struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

type revenueBlock struct {
	Total         float64      `json:"total"`
	PercentChange *float64     `json:"percentChange"` // null when the prior period had no revenue
	Chart         []chartPoint `json:"chart"`
}

type activeBlock struct {
	Total     int            `json:"total"`
	Breakdown map[string]int `json:"breakdown"`
	Chart     []chartPoint   `json:"chart"`
}

type ratingBlock struct {
	Average float64 `json:"average"`
	Count   int     `json:"count"`
}

type productsBlock struct {
	Count int64 `json:"count"`
}

type monthlyBlock struct {
	Month             string                   `json:"month"`
	TotalOrders       int                      `json:"totalOrders"`
	TotalRevenue      float64                  `json:"totalRevenue"`
	AverageOrderValue float64                  `json:"averageOrderValue"`
	TopCustomers      []repository.TopCustomer `json:"topCustomers"`
}

// cacheable bundles the dashboard sections that are safe to serve from cache.
type cacheable struct {
	Revenue  revenueBlock
	Rating   ratingBlock
	Products productsBlock
	Monthly  monthlyBlock
}

type dashboardResponse struct {
	Revenue      revenueBlock  `json:"revenue"`
	ActiveOrders activeBlock   `json:"activeOrders"`
	Rating       ratingBlock   `json:"rating"`
	Products     productsBlock `json:"products"`
	Monthly      monthlyBlock  `json:"monthly"`
}

// Dashboard handles GET /api/seller/dashboard (seller only). sellerID always
// comes from the JWT, never from query params.
func (h *DashboardHandler) Dashboard(c *gin.Context) {
	sellerID := middleware.UID(c)

	period := c.DefaultQuery("period", "week")
	if period != "week" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported period (only 'week' is supported)"})
		return
	}

	loc := time.FixedZone("Asia/Tashkent", tashkentOffset)
	now := time.Now().In(loc)

	month, err := parseMonth(c.Query("month"), now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), dashboardTimeout)
	defer cancel()

	// Heavy sections: served from cache when warm.
	cacheKey := strings.Join([]string{"dashboard", sellerID, period, month.String()}, "|")
	cached, ok := h.cache.Get(cacheKey)
	heavy, _ := cached.(*cacheable)
	if !ok || heavy == nil {
		heavy, err = h.buildCacheable(ctx, sellerID, now, loc, month)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build dashboard"})
			return
		}
		h.cache.Set(cacheKey, heavy, cacheTTL)
	}

	// Active orders: always live (operational — sellers refresh to watch orders).
	active, err := h.buildActive(ctx, sellerID, now, loc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load active orders"})
		return
	}

	c.JSON(http.StatusOK, dashboardResponse{
		Revenue:      heavy.Revenue,
		ActiveOrders: active,
		Rating:       heavy.Rating,
		Products:     heavy.Products,
		Monthly:      heavy.Monthly,
	})
}

// buildCacheable computes the revenue, rating, products and monthly sections.
func (h *DashboardHandler) buildCacheable(ctx context.Context, sellerID string, now time.Time, loc *time.Location, month time.Month) (*cacheable, error) {
	weekStart := startOfDay(now, loc).AddDate(0, 0, -6) // 7 days: weekStart..today
	currentEnd := startOfDay(now, loc).AddDate(0, 0, 1) // end of today (exclusive)
	priorStart := weekStart.AddDate(0, 0, -7)

	// One query over the full 14-day window covers both this week and last week.
	buckets, err := h.orders.DeliveredRevenueByDay(ctx, sellerID, priorStart, currentEnd)
	if err != nil {
		return nil, err
	}
	chart := bucketsToChart(buckets, weekStart, loc)
	var currentTotal float64
	for _, p := range chart {
		currentTotal += p.Value // card total == chart total by construction
	}
	weekStartInstant := weekStart.UTC()
	var priorTotal float64
	for _, b := range buckets {
		if b.Day.Before(weekStartInstant) {
			priorTotal += b.Value
		}
	}

	avg, count, err := h.reviews.SellerRating(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	productCount, err := h.products.CountBySeller(ctx, sellerID)
	if err != nil {
		return nil, err
	}

	monthly, err := h.buildMonthly(ctx, sellerID, now, loc, month)
	if err != nil {
		return nil, err
	}

	return &cacheable{
		Revenue: revenueBlock{
			Total:         currentTotal,
			PercentChange: percentChange(currentTotal, priorTotal),
			Chart:         chart,
		},
		Rating:   ratingBlock{Average: avg, Count: count},
		Products: productsBlock{Count: productCount},
		Monthly:  monthly,
	}, nil
}

// buildMonthly aggregates the selected month's delivered orders.
func (h *DashboardHandler) buildMonthly(ctx context.Context, sellerID string, now time.Time, loc *time.Location, month time.Month) (monthlyBlock, error) {
	monthStart := time.Date(now.Year(), month, 1, 0, 0, 0, 0, loc)
	monthEnd := monthStart.AddDate(0, 1, 0)

	orders, revenue, top, err := h.orders.MonthlySummary(ctx, sellerID, monthStart, monthEnd)
	if err != nil {
		return monthlyBlock{}, err
	}
	if top == nil {
		top = []repository.TopCustomer{}
	}

	// Resolve customer display names in a single lookup.
	ids := make([]string, 0, len(top))
	for _, t := range top {
		ids = append(ids, t.CustomerID)
	}
	names, err := h.users.NamesByID(ctx, ids)
	if err != nil {
		return monthlyBlock{}, err
	}
	for i := range top {
		top[i].Name = names[top[i].CustomerID]
	}

	var aov float64
	if orders > 0 {
		aov = revenue / float64(orders)
	}
	return monthlyBlock{
		Month:             month.String(),
		TotalOrders:       orders,
		TotalRevenue:      revenue,
		AverageOrderValue: aov,
		TopCustomers:      top,
	}, nil
}

// buildActive computes the live active-orders section.
func (h *DashboardHandler) buildActive(ctx context.Context, sellerID string, now time.Time, loc *time.Location) (activeBlock, error) {
	counts, err := h.orders.ActiveBreakdown(ctx, sellerID)
	if err != nil {
		return activeBlock{}, err
	}
	// Always expose all three statuses so the card has stable keys.
	breakdown := map[string]int{
		string(models.OrderPending):        counts[models.OrderPending],
		string(models.OrderPreparing):      counts[models.OrderPreparing],
		string(models.OrderOutForDelivery): counts[models.OrderOutForDelivery],
	}
	total := 0
	for _, n := range breakdown {
		total += n
	}

	weekStart := startOfDay(now, loc).AddDate(0, 0, -6)
	currentEnd := startOfDay(now, loc).AddDate(0, 0, 1)
	dayBuckets, err := h.orders.ActiveByDay(ctx, sellerID, weekStart, currentEnd)
	if err != nil {
		return activeBlock{}, err
	}

	return activeBlock{
		Total:     total,
		Breakdown: breakdown,
		Chart:     bucketsToChart(dayBuckets, weekStart, loc),
	}, nil
}

// startOfDay returns local midnight of t's day in loc.
func startOfDay(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// bucketsToChart turns sparse per-day sums into exactly 7 points starting at
// weekStart (a local midnight), backfilling missing days with 0 and labelling
// each with its weekday abbreviation. Buckets are keyed by the instant of their
// local day-start, which equals weekStart.AddDate(0,0,i).UTC() for slot i.
func bucketsToChart(buckets []repository.DayBucket, weekStart time.Time, loc *time.Location) []chartPoint {
	byInstant := make(map[int64]float64, len(buckets))
	for _, b := range buckets {
		byInstant[b.Day.Unix()] = b.Value
	}
	points := make([]chartPoint, 7)
	for i := 0; i < 7; i++ {
		day := weekStart.AddDate(0, 0, i)
		points[i] = chartPoint{
			Day:   day.In(loc).Weekday().String()[:3],
			Value: byInstant[day.UTC().Unix()],
		}
	}
	return points
}

// percentChange reports the percent change from prior to current. It returns nil
// when prior is 0, so the frontend can hide the pill instead of showing ∞/NaN.
func percentChange(current, prior float64) *float64 {
	if prior == 0 {
		return nil
	}
	pct := (current - prior) / prior * 100
	return &pct
}

// monthNames maps lower-cased full and abbreviated month names to their month.
var monthNames = func() map[string]time.Month {
	m := make(map[string]time.Month, 24)
	for mon := time.January; mon <= time.December; mon++ {
		name := strings.ToLower(mon.String())
		m[name] = mon
		m[name[:3]] = mon
	}
	return m
}()

// parseMonth resolves the ?month= param. Empty defaults to the current month;
// it accepts a numeric 1-12 or a (full or abbreviated) month name.
func parseMonth(raw string, now time.Time) (time.Month, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return now.Month(), nil
	}
	if n, err := strconv.Atoi(raw); err == nil {
		if n < 1 || n > 12 {
			return 0, errInvalidMonth
		}
		return time.Month(n), nil
	}
	if mon, ok := monthNames[strings.ToLower(raw)]; ok {
		return mon, nil
	}
	return 0, errInvalidMonth
}
