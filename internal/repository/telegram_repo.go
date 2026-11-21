package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"bakeu/internal/identity"
	"bakeu/internal/models"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var ErrConflict = errors.New("conflicting or expired operation")
var ErrForbidden = errors.New("forbidden")

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure randomness unavailable")
	}
	return hex.EncodeToString(b[:])
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type TelegramBinding struct {
	VerifiedPhoneE164 string    `bson:"verifiedPhoneE164,omitempty" json:"-"`
	TelegramID        string    `bson:"_id" json:"telegramId"`
	UserID            string    `bson:"userId" json:"-"`
	ConfirmedAt       time.Time `bson:"confirmedAt" json:"-"`
	BotStarted        bool      `bson:"botStarted" json:"botStarted"`
	Blocked           bool      `bson:"blocked" json:"-"`
}
type LinkChallenge struct {
	ID         string      `bson:"_id"`
	TelegramID string      `bson:"telegramId"`
	Phone      string      `bson:"phone"`
	Role       models.Role `bson:"role"`
	ExpiresAt  time.Time   `bson:"expiresAt"`
	CreatedAt  time.Time   `bson:"createdAt"`
	Used       bool        `bson:"used"`
}
type TelegramIdentity struct {
	Linked     bool   `json:"linked"`
	Confirmed  bool   `json:"confirmed"`
	TelegramID string `json:"telegramId"`
	BotStarted bool   `json:"botStarted"`
	ShopID     string `json:"shopId,omitempty"`
	User       struct {
		CanBuy bool   `json:"canBuy"`
		ID     string `json:"id"`
		Role   string `json:"role"`
		Status string `json:"status"`
	} `json:"user"`
}
type Notification struct {
	ID         string       `bson:"_id" json:"id"`
	UserID     string       `bson:"userId" json:"userId"`
	OrderID    string       `bson:"orderId" json:"orderId"`
	Title      string       `bson:"title" json:"title"`
	Kind       string       `bson:"kind" json:"-"`
	Order      models.Order `bson:"order" json:"-"`
	CreatedAt  time.Time    `bson:"createdAt" json:"createdAt"`
	State      string       `bson:"state" json:"telegramState"`
	Attempts   int          `bson:"attempts" json:"-"`
	NextAt     time.Time    `bson:"nextAt" json:"-"`
	LeaseToken string       `bson:"leaseToken,omitempty" json:"-"`
	LeaseUntil time.Time    `bson:"leaseUntil,omitempty" json:"-"`
	MessageID  int64        `bson:"messageId,omitempty" json:"-"`
	Code       int          `bson:"code,omitempty" json:"-"`
}
type NotificationJob struct {
	ID         string            `json:"id"`
	LeaseToken string            `json:"leaseToken"`
	Attempts   int               `json:"attempts"`
	Kind       string            `json:"kind"`
	Recipient  *TelegramIdentity `json:"recipient"`
	Order      models.Order      `json:"order"`
}
type TelegramRepo struct{ db *mongo.Database }

func NewTelegramRepo(db *mongo.Database) *TelegramRepo { return &TelegramRepo{db: db} }
func (r *TelegramRepo) EnsureIndexes(ctx context.Context) error {
	// Additive: old accounts with no proof are excluded. Never infer ownership
	// from the editable phone field or backfill it while starting the service.
	_, err := r.db.Collection("users").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "verifiedPhoneE164", Value: 1}},
		Options: options.Index().SetName("unique_verified_phone").SetUnique(true).
			SetPartialFilterExpression(bson.M{"verifiedPhoneE164": bson.M{"$type": "string", "$gt": ""}}),
	})
	if err != nil {
		return err
	}
	_, err = r.db.Collection("phone_proofs").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
		{Keys: bson.D{{Key: "phone", Value: 1}, {Key: "createdAt", Value: -1}}},
	})
	if err != nil {
		return err
	}
	_, err = r.db.Collection("telegram_login_tickets").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)})
	if err != nil {
		return err
	}
	_, err = r.db.Collection("telegram_bindings").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "userId", Value: 1}}, Options: options.Index().SetUnique(true)})
	if err != nil {
		return err
	}
	_, err = r.db.Collection("telegram_challenges").Indexes().CreateMany(ctx, []mongo.IndexModel{{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}, {Keys: bson.D{{Key: "telegramId", Value: 1}, {Key: "createdAt", Value: -1}}}})
	if err != nil {
		return err
	}
	_, err = r.db.Collection("order_notifications").Indexes().CreateMany(ctx, []mongo.IndexModel{{Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}}, {Keys: bson.D{{Key: "state", Value: 1}, {Key: "nextAt", Value: 1}, {Key: "createdAt", Value: 1}}}})
	return err
}
func (r *TelegramRepo) Identity(ctx context.Context, actor string) (*TelegramIdentity, error) {
	var b TelegramBinding
	err := r.db.Collection("telegram_bindings").FindOne(ctx, bson.M{"_id": actor}).Decode(&b)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u, err := NewUserRepo(r.db).Get(ctx, b.UserID)
	if err != nil {
		return nil, err
	}
	if !u.Active() || b.ConfirmedAt.IsZero() {
		return nil, ErrForbidden
	}
	if !u.PhoneVerified() || b.VerifiedPhoneE164 != u.VerifiedPhoneE164 {
		return nil, nil // Pre-verification bindings require a fresh contact + password proof.
	}
	out := &TelegramIdentity{Linked: true, Confirmed: true, TelegramID: actor, BotStarted: b.BotStarted}
	out.User.ID = u.UID
	out.User.Status = "active"
	out.User.Role = "CLIENT"
	out.User.CanBuy = true
	if u.Role == models.RoleSeller {
		out.User.Role = "SELLER"
		m, e := NewMarketRepo(r.db).GetBySeller(ctx, u.UID)
		if e != nil {
			return nil, e
		}
		out.ShopID = m.ID
	}
	return out, nil
}
func (r *TelegramRepo) Challenge(ctx context.Context, actor, phone string, role models.Role) (string, time.Time, error) {
	now := time.Now().UTC()
	n, err := r.db.Collection("telegram_challenges").CountDocuments(ctx, bson.M{"telegramId": actor, "createdAt": bson.M{"$gte": now.Add(-time.Minute)}})
	if err != nil {
		return "", time.Time{}, err
	}
	if n >= 5 {
		return "", time.Time{}, ErrConflict
	}
	token := randomToken()
	expiry := now.Add(10 * time.Minute)
	_, err = r.db.Collection("telegram_challenges").InsertOne(ctx, LinkChallenge{ID: digest(token), TelegramID: actor, Phone: phone, Role: role, CreatedAt: now, ExpiresAt: expiry})
	return token, expiry, err
}

// The caller has freshly verified the existing user's password. Contact+role+UID are checked again atomically.
func (r *TelegramRepo) Confirm(ctx context.Context, token, uid string) error {
	session, err := r.db.Client().StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		var ch LinkChallenge
		if e := r.db.Collection("telegram_challenges").FindOne(ctx, bson.M{"_id": digest(token), "used": false, "expiresAt": bson.M{"$gt": time.Now()}}).Decode(&ch); e != nil {
			if isNotFound(e) {
				return nil, ErrConflict
			}
			return nil, e
		}
		u, e := NewUserRepo(r.db).Get(ctx, uid)
		if e != nil {
			return nil, e
		}
		phone := u.VerifiedPhoneE164
		if !u.PhoneVerified() {
			phone, e = identity.NormalizePhone(u.Phone)
		}
		if e != nil || !u.Active() || phone != ch.Phone || u.Role != ch.Role {
			return nil, ErrForbidden
		}
		if u.Role == models.RoleSeller {
			if _, e = NewMarketRepo(r.db).GetBySeller(ctx, uid); errors.Is(e, ErrNotFound) {
				now := time.Now().UTC()
				_, e = r.db.Collection("markets").InsertOne(ctx, models.Market{ID: bson.NewObjectID().Hex(), SellerID: uid, Status: models.MarketDraft, Phone: ch.Phone, Email: u.Email, Fulfillment: models.FulfillmentPickup, CreatedAt: now, UpdatedAt: now})
			}
			if e != nil {
				return nil, e
			}
		}
		// A write on the same user serializes confirmation with profile/freeze changes.
		now := time.Now().UTC()
		verifiedAt := u.PhoneVerifiedAt
		if !u.PhoneVerified() {
			verifiedAt = &now
		}
		result, e := r.db.Collection("users").UpdateOne(ctx, bson.M{"_id": uid, "phone": u.Phone, "role": u.Role}, bson.M{"$set": bson.M{"telegramProofAt": now, "verifiedPhoneE164": ch.Phone, "phoneVerifiedAt": verifiedAt}})
		if isDuplicate(e) {
			return nil, ErrConflict
		}
		if e != nil {
			return nil, e
		}
		if result.MatchedCount != 1 {
			return nil, ErrConflict
		}

		var previous TelegramBinding
		e = r.db.Collection("telegram_bindings").FindOne(ctx, bson.M{"_id": ch.TelegramID}).Decode(&previous)
		if e == nil && previous.UserID != uid {
			return nil, ErrConflict
		}
		if e != nil && !isNotFound(e) {
			return nil, e
		}
		_, e = r.db.Collection("telegram_bindings").UpdateOne(ctx, bson.M{"_id": ch.TelegramID}, bson.M{"$set": bson.M{"userId": uid, "verifiedPhoneE164": ch.Phone, "confirmedAt": now, "botStarted": true, "blocked": false}}, options.UpdateOne().SetUpsert(true))
		if isDuplicate(e) {
			return nil, ErrConflict
		}
		if e != nil {
			return nil, e
		}
		result, e = r.db.Collection("telegram_challenges").UpdateOne(ctx, bson.M{"_id": ch.ID, "used": false}, bson.M{"$set": bson.M{"used": true}})
		if e != nil {
			return nil, e
		}
		if result.MatchedCount != 1 {
			return nil, ErrConflict
		}
		return nil, nil
	})
	return err
}
func (r *TelegramRepo) Inbox(ctx context.Context, uid string) ([]Notification, error) {
	out := []Notification{}
	cur, e := r.db.Collection("order_notifications").Find(ctx, bson.M{"userId": uid}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(100))
	if e != nil {
		return nil, e
	}
	e = cur.All(ctx, &out)
	return out, e
}
func (r *TelegramRepo) Claim(ctx context.Context) ([]NotificationJob, error) {
	now := time.Now().UTC()
	c := r.db.Collection("order_notifications")
	// Never replay a potentially sent message after worker loss.
	if _, e := c.UpdateMany(ctx, bson.M{"state": "sending", "leaseUntil": bson.M{"$lte": now}}, bson.M{"$set": bson.M{"state": "uncertain"}}); e != nil {
		return nil, e
	}
	if _, e := c.UpdateMany(ctx, bson.M{"state": "leased", "leaseUntil": bson.M{"$lte": now}}, bson.M{"$set": bson.M{"state": "retry", "nextAt": now}}); e != nil {
		return nil, e
	}
	jobs := []NotificationJob{}
	// Scan older intents first. A recipient's earlier unsent/uncertain revision blocks newer ones.
	cur, e := c.Find(ctx, bson.M{"state": bson.M{"$in": bson.A{"pending", "retry"}}, "nextAt": bson.M{"$lte": now}}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(100))
	if e != nil {
		return nil, e
	}
	var candidates []Notification
	if e = cur.All(ctx, &candidates); e != nil {
		return nil, e
	}
	for _, n := range candidates {
		if len(jobs) >= 10 {
			break
		}
		if n.Attempts >= 8 {
			_, _ = c.UpdateOne(ctx, bson.M{"_id": n.ID, "state": n.State}, bson.M{"$set": bson.M{"state": "failed"}})
			continue
		}
		earlier, e := c.CountDocuments(ctx, bson.M{"userId": n.UserID, "orderId": n.OrderID, "order.version": bson.M{"$lt": n.Order.Version}, "state": bson.M{"$in": bson.A{"pending", "retry", "leased", "sending", "uncertain"}}})
		if e != nil {
			return nil, e
		}
		if earlier > 0 {
			continue
		}
		var binding TelegramBinding
		e = r.db.Collection("telegram_bindings").FindOne(ctx, bson.M{"userId": n.UserID}).Decode(&binding)
		if isNotFound(e) {
			_, _ = c.UpdateOne(ctx, bson.M{"_id": n.ID, "state": n.State}, bson.M{"$set": bson.M{"state": "unlinked"}})
			continue
		}
		if e != nil {
			return nil, e
		}
		recipient, e := r.Identity(ctx, binding.TelegramID)
		if e != nil || recipient == nil || !binding.BotStarted || binding.Blocked {
			_, _ = c.UpdateOne(ctx, bson.M{"_id": n.ID, "state": n.State}, bson.M{"$set": bson.M{"state": "blocked"}})
			continue
		}
		lease := randomToken()
		var leased Notification
		e = c.FindOneAndUpdate(ctx, bson.M{"_id": n.ID, "state": n.State, "attempts": n.Attempts, "nextAt": bson.M{"$lte": now}}, bson.M{"$set": bson.M{"state": "leased", "leaseToken": lease, "leaseUntil": now.Add(time.Minute), "recipientTelegramId": binding.TelegramID}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&leased)
		if isNotFound(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		leased.Order.Project()
		jobs = append(jobs, NotificationJob{ID: n.ID, LeaseToken: lease, Attempts: n.Attempts, Kind: n.Kind, Recipient: recipient, Order: leased.Order})
	}
	return jobs, nil
}
func (r *TelegramRepo) Begin(ctx context.Context, id, lease string) (bool, error) {
	var n struct {
		Notification `bson:",inline"`
		Recipient    string `bson:"recipientTelegramId"`
	}
	e := r.db.Collection("order_notifications").FindOne(ctx, bson.M{"_id": id, "leaseToken": lease, "state": "leased"}).Decode(&n)
	if isNotFound(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	who, e := r.Identity(ctx, n.Recipient)
	owns := who != nil && (who.User.CanBuy && n.Order.CustomerID == who.User.ID || who.User.Role == "SELLER" && n.Order.SellerID == who.User.ID && n.Order.ShopID == who.ShopID)
	if e != nil || !owns || who.User.ID != n.UserID {
		return false, ErrForbidden
	}
	res, e := r.db.Collection("order_notifications").UpdateOne(ctx, bson.M{"_id": id, "leaseToken": lease, "state": "leased", "leaseUntil": bson.M{"$gt": time.Now()}}, bson.M{"$set": bson.M{"state": "sending"}, "$inc": bson.M{"attempts": 1}})
	if e != nil {
		return false, e
	}
	return res.MatchedCount == 1, nil
}
func (r *TelegramRepo) Ack(ctx context.Context, id, lease string, messageID int64) error {
	res, e := r.db.Collection("order_notifications").UpdateOne(ctx, bson.M{"_id": id, "leaseToken": lease, "state": bson.M{"$in": bson.A{"sending", "uncertain", "sent"}}, "$or": bson.A{bson.M{"messageId": bson.M{"$exists": false}}, bson.M{"messageId": messageID}}}, bson.M{"$set": bson.M{"state": "sent", "messageId": messageID}})
	if e != nil {
		return e
	}
	if res.MatchedCount == 0 {
		return ErrConflict
	}
	return nil
}
func (r *TelegramRepo) Fail(ctx context.Context, id, lease, state string, code int, next time.Time) error {
	if state != "retry" && state != "blocked" && state != "failed" && state != "uncertain" {
		return ErrForbidden
	}
	if state == "retry" && code != 429 {
		return ErrForbidden
	}
	if next.IsZero() || next.Before(time.Now()) {
		next = time.Now().Add(5 * time.Second)
	}
	if next.After(time.Now().Add(24 * time.Hour)) {
		next = time.Now().Add(24 * time.Hour)
	}
	res, e := r.db.Collection("order_notifications").UpdateOne(ctx, bson.M{"_id": id, "leaseToken": lease, "state": bson.M{"$in": bson.A{"leased", "sending"}}}, bson.M{"$set": bson.M{"state": state, "code": code, "nextAt": next}})
	if e != nil {
		return e
	}
	if res.MatchedCount == 0 {
		return ErrConflict
	}
	return nil
}

// Called only within the order transaction. Immutable snapshots and unique IDs deduplicate events.
func enqueueOrderEvents(ctx context.Context, db *mongo.Database, o *models.Order, kind string) error {
	recipients := []string{o.CustomerID}
	if kind == "order.created" {
		recipients = append(recipients, o.SellerID)
	}
	for _, uid := range recipients {
		id := digest(fmt.Sprintf("%s:%d:%s:%s", o.ID, o.Version, uid, kind))
		n := Notification{ID: id, UserID: uid, OrderID: o.ID, Title: "Заказ #" + o.Number + " — " + string(o.Status), Kind: kind, Order: *o, CreatedAt: time.Now().UTC(), State: "pending", NextAt: time.Now().UTC()}
		if _, e := db.Collection("order_notifications").InsertOne(ctx, n); e != nil {
			return e
		}
	}
	return nil
}
