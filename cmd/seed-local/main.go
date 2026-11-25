// Command seed-local populates the dedicated loopback BakeU demo database.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"time"
	_ "time/tzdata"

	"bakeu/internal/config"
	"bakeu/internal/models"
	"bakeu/internal/mongoclient"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/crypto/bcrypt"
)

const database = "bakeu_local_demo"

var menu = []struct {
	title, category, filename string
	price                     float64
}{
	{"Медовик с кремом", "Торты", "cake.jpg", 98000},
	{"Шоколадное печенье", "Печенье", "cookie.jpg", 14000},
	{"Ташкентская самса", "Самса", "somsa.jpg", 12000},
	{"Хлеб на закваске", "Хлеб", "bread.jpg", 18000},
	{"Симит с кунжутом", "Хлеб", "smit.jpg", 11000},
	{"Салат Ачичук", "Салаты", "achi-chuchuk.webp", 23000},
	{"Салат с огурцами и зеленью", "Салаты", "cucumber-salad.webp", 25000},
	{"Летний овощной салат", "Салаты", "summer-salad.webp", 26000},
}

var shopNames = []string{
	"Tandir Non", "Asal Bakery", "Chorsu Kitchen", "Shirin Oqshom", "Rayhon",
	"Yangi Ta’m", "Choy va Non", "Oila Oshxonasi", "Anor", "Ko‘k Tandir",
	"Lola Bakery", "Samarkand Sweets", "Sahovat", "Gulzor", "Bahor Salat",
	"Atlas Kitchen", "Yulduz", "Navro‘z", "Dasturxon", "Uy Ta’mi",
}

var buyerNames = []string{
	"Aziza Karimova", "Jasur Abdullayev", "Madina Yusupova", "Sardor Ismoilov", "Nilufar Aliyeva",
	"Bekzod Raximov", "Zarina Usmonova", "Akmal Tursunov", "Lola Ergasheva", "Oybek Sobirov",
	"Malika Qodirova", "Farrux Hamidov", "Dilnoza Alimova", "Temur Rustamov", "Umida Yunusova",
	"Sanjar Mirzayev", "Sevara Nazarova", "Aziz Mamatov", "Nodira Xasanova", "Javlon Olimov",
	"Shahnoza Haydarova", "Ulug‘bek Salimov", "Gulnoza Sattorova", "Diyor Mahmudov", "Nozima Jamolova",
	"Abbos Sharipov", "Muslima Sobirova", "Sherzod Normatov", "Zilola Abdullayeva", "Shoxrux Hakimov",
}

func insertDemo(ctx context.Context, db *mongo.Database, collection, id string, value any) error {
	_, err := db.Collection(collection).UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$setOnInsert": value}, options.UpdateOne().SetUpsert(true))
	return err
}

func imageURL(ctx context.Context, db *mongo.Database, directory, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	var id bson.ObjectID
	copy(id[:], sum[:])
	var existing bson.M
	err = db.Collection("fs.files").FindOne(ctx, bson.M{"_id": id}).Decode(&existing)
	if errors.Is(err, mongo.ErrNoDocuments) {
		contentType := mime.TypeByExtension(filepath.Ext(name))
		err = db.GridFSBucket().UploadFromStreamWithID(ctx, id, name, bytes.NewReader(data),
			options.GridFSUpload().SetMetadata(bson.M{"contentType": contentType}))
	}
	if err != nil {
		return "", err
	}
	return "/api/images/" + id.Hex(), nil
}

func seed(ctx context.Context, db *mongo.Database, directory, password string, now time.Time) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	images := make(map[string]string)
	for _, item := range menu {
		if _, ok := images[item.filename]; ok {
			continue
		}
		url, err := imageURL(ctx, db, directory, item.filename)
		if err != nil {
			return fmt.Errorf("image %s: %w", item.filename, err)
		}
		images[item.filename] = url
	}

	created := now.AddDate(0, -2, 0)
	buyers := make([]models.User, len(buyerNames))
	for i, name := range buyerNames {
		buyer := models.User{
			UID: fmt.Sprintf("demo-buyer-%02d", i+1), Email: fmt.Sprintf("buyer%02d@bakeu.test", i+1),
			PasswordHash: string(hash), Role: models.RoleCustomer, Name: name,
			Phone: fmt.Sprintf("+9980001%05d", i+1), Address: fmt.Sprintf("Ташкент, Демо-квартал, дом %d", i+1),
			CreatedAt: created,
		}
		if i == 0 {
			buyer.VerifiedPhoneE164, buyer.PhoneVerifiedAt = buyer.Phone, &created
		}
		if err := insertDemo(ctx, db, "users", buyer.UID, buyer); err != nil {
			return err
		}
		buyers[i] = buyer
	}
	for i, name := range shopNames {
		seller := models.User{
			UID: fmt.Sprintf("demo-seller-%02d", i+1), Email: fmt.Sprintf("seller%02d@bakeu.test", i+1),
			PasswordHash: string(hash), Role: models.RoleSeller, Name: name,
			Phone: fmt.Sprintf("+9980002%05d", i+1), Address: fmt.Sprintf("Ташкент, Демо-улица, дом %d", i+1),
			CreatedAt: created,
		}
		if err := insertDemo(ctx, db, "users", seller.UID, seller); err != nil {
			return err
		}
		lat := 41.255 + float64(i/5)*0.026 + float64(i%3)*0.006
		lng := 69.185 + float64(i%5)*0.042 + float64(i%2)*0.008
		shop := models.Market{
			ID: fmt.Sprintf("demo-shop-%02d", i+1), SellerID: seller.UID, Name: name,
			Description: "Учебная витрина с выпечкой и готовыми блюдами.", Phone: seller.Phone, Email: seller.Email,
			City: "Ташкент", Area: "Демо-квартал", Address: seller.Address, Latitude: &lat, Longitude: &lng,
			Fulfillment: models.FulfillmentBoth, Status: models.MarketApproved, CreatedAt: created, UpdatedAt: created,
		}
		if err := insertDemo(ctx, db, "markets", shop.ID, shop); err != nil {
			return err
		}
		products := make([]models.Product, 3+i%2)
		for j := range products {
			dish := menu[(i*3+j)%len(menu)]
			product := models.Product{
				ID: fmt.Sprintf("demo-product-%02d-%d", i+1, j+1), SellerID: seller.UID,
				Title: dish.title, Category: dish.category, Description: "Приготовлено на заказ в " + name + ".",
				Price: dish.price + float64(i%5)*1000, Available: true, ImageURLs: []string{images[dish.filename]},
				CreatedAt: created, UpdatedAt: created,
			}
			if err := insertDemo(ctx, db, "products", product.ID, product); err != nil {
				return err
			}
			products[j] = product
		}
		for j := range 9 {
			product := products[(i+j)%len(products)]
			buyer := buyers[(i*7+j)%len(buyers)]
			day := now.AddDate(0, 0, -((j*4 + i*3) % 31))
			sold := time.Date(day.Year(), day.Month(), day.Day(), 10+j, 0, 0, 0, now.Location())
			if sold.After(now) {
				sold = now
			}
			quantity := 1 + (i+j*2)%5
			id := fmt.Sprintf("demo-order-%02d-%02d", i+1, j+1)
			status := models.OrderCompleted
			if j == 8 {
				status = models.OrderPending
				sold = now
			}
			order := models.Order{
				ID: id, Number: fmt.Sprintf("DEMO-%03d", i*9+j+1), CustomerID: buyer.UID, SellerID: seller.UID,
				ShopID: shop.ID, ShopName: shop.Name, BuyerName: buyer.Name, BuyerPhone: buyer.Phone,
				Fulfillment: "delivery", DeliveryAddress: buyer.Address, Status: status,
				Total: product.Price * float64(quantity), CreatedAt: sold.Add(-2 * time.Hour), UpdatedAt: &sold,
				Version: 1, WorkflowVersion: 2, History: []models.OrderEvent{{Status: status, At: sold, ActorID: seller.UID, Version: 1}},
				Items: []models.OrderItem{{ProductID: product.ID, Title: product.Title, Qty: quantity, Price: product.Price}},
			}
			if err := insertDemo(ctx, db, "orders", order.ID, order); err != nil {
				return err
			}
		}
	}
	return nil
}

func main() {
	port := flag.Int("mongo-port", 27017, "port of the disposable loopback MongoDB replica set")
	images := flag.String("images", "../BakeU.UZ/public/images/products", "product photo directory")
	flag.Parse()
	if *port < 1 || *port > 65535 {
		log.Fatal("invalid MongoDB port")
	}
	password := os.Getenv("BAKEU_DEMO_PASSWORD")
	if len(password) < 8 || len(password) > 72 {
		log.Fatal("set BAKEU_DEMO_PASSWORD to an 8–72 byte local test password")
	}
	zone, err := time.LoadLocation("Asia/Tashkent")
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	clients, err := mongoclient.New(ctx, config.Config{
		MongoURI: fmt.Sprintf("mongodb://127.0.0.1:%d/?replicaSet=bakeu-dev&directConnection=true", *port), MongoDB: database,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer clients.Close()
	if err := seed(ctx, clients.DB, *images, password, time.Now().In(zone)); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Demo records available: 20 sellers, 20 shops, 70 products, 30 buyers and 180 orders.")
}
