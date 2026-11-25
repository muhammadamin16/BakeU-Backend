package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"
	_ "time/tzdata"

	"bakeu/internal/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type AdminWorkspace struct {
	db *mongo.Database
}

func NewAdminWorkspace(db *mongo.Database) *AdminWorkspace {
	return &AdminWorkspace{db: db}
}

const adminPageSize = 25

func adminRecords[T any](ctx context.Context, collection *mongo.Collection, filter bson.M, page int64, sort bson.D) ([]T, bool, error) {
	cursor, err := collection.Find(ctx, filter, options.Find().
		SetSort(sort).SetSkip((page-1)*adminPageSize).SetLimit(adminPageSize+1))
	if err != nil {
		return nil, false, err
	}
	rows := make([]T, 0)
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, false, err
	}
	if len(rows) > adminPageSize {
		return rows[:adminPageSize], true, nil
	}
	return rows, false, nil
}

func (h *AdminWorkspace) Records(c *gin.Context) {
	page, err := strconv.ParseInt(c.DefaultQuery("page", "1"), 10, 32)
	if err != nil || page < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверная страница."})
		return
	}
	ctx := c.Request.Context()
	filter := bson.M{}
	sort := bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}
	var rows any
	var more bool
	switch c.Param("kind") {
	case "customers", "sellers":
		role := models.RoleCustomer
		if c.Param("kind") == "sellers" {
			role = models.RoleSeller
		}
		filter["role"] = role
		rows, more, err = adminRecords[models.User](ctx, h.db.Collection("users"), filter, page, sort)
	case "shops":
		rows, more, err = adminRecords[models.Market](ctx, h.db.Collection("markets"), filter, page, sort)
	case "products":
		rows, more, err = adminRecords[models.Product](ctx, h.db.Collection("products"), filter, page, sort)
	case "orders":
		if buyer := c.Query("buyer"); buyer != "" {
			filter["customerId"] = buyer
		}
		var orders []models.Order
		orders, more, err = adminRecords[models.Order](ctx, h.db.Collection("orders"), filter, page, sort)
		for i := range orders {
			orders[i].Project()
		}
		rows = orders
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": "Раздел не найден."})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Не удалось загрузить данные."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "more": more})
}

const adminSalesTimezone = "Asia/Tashkent"

type productSales struct {
	Day       string  `bson:"day" json:"day"`
	ProductID string  `bson:"productId" json:"productId"`
	Title     string  `bson:"title" json:"title"`
	Quantity  int     `bson:"quantity" json:"quantity"`
	Revenue   float64 `bson:"revenue" json:"revenue"`
}

func (h *AdminWorkspace) Sales(c *gin.Context) {
	zone, err := time.LoadLocation(adminSalesTimezone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Часовой пояс недоступен."})
		return
	}
	date, err := time.ParseInLocation(time.DateOnly, c.DefaultQuery("date", time.Now().In(zone).Format(time.DateOnly)), zone)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверная дата."})
		return
	}
	start, end := date, date.AddDate(0, 0, 1)
	switch c.DefaultQuery("period", "week") {
	case "day":
	case "week":
		start = date.AddDate(0, 0, -6)
	case "month":
		start = time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, zone)
		end = start.AddDate(0, 1, 0)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный период."})
		return
	}

	// Use the completion workflow event for completed orders and the delivery
	// event for legacy delivered orders. Older documents can lack history.
	events := bson.M{"$filter": bson.M{
		"input": bson.M{"$ifNull": bson.A{"$history", bson.A{}}},
		"as":    "event", "cond": bson.M{"$eq": bson.A{"$$event.status", "$status"}},
	}}
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{"$or": bson.A{
			bson.M{"status": models.OrderCompleted},
			bson.M{"status": models.OrderDelivered, "workflowVersion": bson.M{"$ne": 2}},
		}}}},
		bson.D{{Key: "$set", Value: bson.M{"soldAt": bson.M{"$ifNull": bson.A{
			bson.M{"$let": bson.M{
				"vars": bson.M{"last": bson.M{"$arrayElemAt": bson.A{events, -1}}},
				"in":   "$$last.at",
			}},
			"$updatedAt", "$createdAt",
		}}}}},
		bson.D{{Key: "$match", Value: bson.M{"soldAt": bson.M{"$gte": start, "$lt": end}}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "soldAt", Value: -1}, {Key: "_id", Value: -1}}}},
		bson.D{{Key: "$unwind", Value: "$items"}},
		bson.D{{Key: "$group", Value: bson.M{
			"_id": bson.M{"day": bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$soldAt", "timezone": adminSalesTimezone}},
				"productId": "$items.productId"},
			"title":    bson.M{"$first": "$items.title"},
			"quantity": bson.M{"$sum": "$items.qty"},
			"revenue":  bson.M{"$sum": bson.M{"$multiply": bson.A{"$items.qty", "$items.price"}}},
		}}},
		bson.D{{Key: "$project", Value: bson.M{"_id": 0, "day": "$_id.day", "productId": "$_id.productId", "title": 1, "quantity": 1, "revenue": 1}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "day", Value: -1}, {Key: "quantity", Value: -1}, {Key: "productId", Value: 1}}}},
	}
	ctx := c.Request.Context()
	cursor, err := h.db.Collection("orders").Aggregate(ctx, pipeline)
	rows := make([]productSales, 0)
	if err == nil {
		err = cursor.All(ctx, &rows)
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Не удалось загрузить продажи."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "from": start.Format(time.DateOnly), "through": end.AddDate(0, 0, -1).Format(time.DateOnly)})
}
