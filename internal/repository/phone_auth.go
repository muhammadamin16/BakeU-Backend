package repository

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"time"

	"bakeu/internal/models"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// PhoneProof is a pending operation, never a second user/session store. Only the
// bot receives its one-time code after receiving a Telegram-owned contact.
type PhoneProof struct {
	ID         string         `bson:"_id"`
	Purpose    string         `bson:"purpose"`
	Phone      string         `bson:"phone"`
	Draft      *models.User   `bson:"draft,omitempty"`
	Market     *models.Market `bson:"market,omitempty"`
	Version    int            `bson:"version,omitempty"`
	TelegramID string         `bson:"telegramId,omitempty"`
	CodeHash   string         `bson:"codeHash,omitempty"`
	Attempts   int            `bson:"attempts"`
	Used       bool           `bson:"used"`
	CreatedAt  time.Time      `bson:"createdAt"`
	ExpiresAt  time.Time      `bson:"expiresAt"`
}

func (r *TelegramRepo) StartPhoneProof(ctx context.Context, purpose, phone string, draft *models.User, market *models.Market) (string, time.Time, error) {
	now := time.Now().UTC()
	n, err := r.db.Collection("phone_proofs").CountDocuments(ctx, bson.M{"phone": phone, "createdAt": bson.M{"$gte": now.Add(-time.Hour)}})
	if err != nil {
		return "", time.Time{}, err
	}
	if n >= 5 {
		return "", time.Time{}, ErrConflict
	}
	// 48 hex characters + "goauth_" fits Telegram's 64-character start limit.
	token := randomToken()[:48]
	p := PhoneProof{ID: digest(token), Purpose: purpose, Phone: phone, Draft: draft, Market: market, Version: 2, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	_, err = r.db.Collection("phone_proofs").InsertOne(ctx, p)
	return token, p.ExpiresAt, err
}

// IssuePhoneCode requires the same trusted private actor and own-contact check
// as the orders bridge. An issued code is never returned a second time.
func (r *TelegramRepo) IssuePhoneCode(ctx context.Context, token, actor, phone string) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	result, err := r.db.Collection("phone_proofs").UpdateOne(ctx,
		bson.M{"_id": digest(token), "phone": phone, "codeHash": bson.M{"$exists": false}, "used": false, "expiresAt": bson.M{"$gt": time.Now()}},
		bson.M{"$set": bson.M{"telegramId": actor, "codeHash": digest(token + ":" + code)}})
	if err != nil {
		return "", err
	}
	if result.MatchedCount != 1 {
		return "", ErrConflict
	}
	return code, nil
}

// FinishPhoneProof atomically consumes the challenge, creates or loads the one
// Go user and binds Telegram. Unique indexes resolve concurrent ownership.
func (r *TelegramRepo) FinishPhoneProof(ctx context.Context, token, code string) (*models.User, error) {
	proofs := r.db.Collection("phone_proofs")
	filter := bson.M{"_id": digest(token), "used": false, "attempts": bson.M{"$lt": 5}, "expiresAt": bson.M{"$gt": time.Now()}, "codeHash": bson.M{"$exists": true}}
	var p PhoneProof
	if err := proofs.FindOne(ctx, filter).Decode(&p); err != nil {
		if isNotFound(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	actual := digest(token + ":" + code)
	if subtle.ConstantTimeCompare([]byte(actual), []byte(p.CodeHash)) != 1 {
		_, err := proofs.UpdateOne(ctx, filter, bson.M{"$inc": bson.M{"attempts": 1}})
		if err != nil {
			return nil, err
		}
		return nil, ErrForbidden
	}
	session, err := r.db.Client().StartSession()
	if err != nil {
		return nil, err
	}
	defer session.EndSession(ctx)
	var result *models.User
	_, err = session.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		// Read again inside the transaction: another verifier may have consumed it.
		if err := proofs.FindOne(ctx, filter).Decode(&p); err != nil {
			if isNotFound(err) {
				return nil, ErrConflict
			}
			return nil, err
		}
		now := time.Now().UTC()
		var u *models.User
		if p.Purpose == "register" {
			if p.Version != 2 || p.Draft == nil || !p.Draft.Role.Valid() || p.Draft.FirstName == "" || p.Draft.LastName == "" || (p.Draft.Role == models.RoleSeller && (p.Market == nil || p.Market.Latitude == nil || p.Market.Longitude == nil)) {
				return nil, ErrForbidden
			}
			copy := *p.Draft
			u = &copy
			u.UID = bson.NewObjectID().Hex()
			u.VerifiedPhoneE164, u.PhoneVerifiedAt, u.CreatedAt = p.Phone, &now, now
			if _, err := r.db.Collection("users").InsertOne(ctx, u); err != nil {
				return nil, err
			}
			if u.Role == models.RoleSeller {
				m := *p.Market
				m.ID, m.SellerID, m.Phone = bson.NewObjectID().Hex(), u.UID, p.Phone
				m.Status, m.Fulfillment = models.MarketApproved, models.FulfillmentPickup
				m.CreatedAt, m.UpdatedAt, m.ReviewedAt = now, now, now
				if _, err := r.db.Collection("markets").InsertOne(ctx, m); err != nil {
					return nil, err
				}
				if _, err := r.db.Collection("market_events").InsertOne(ctx, bson.M{"_id": bson.NewObjectID().Hex(), "marketId": m.ID, "actorId": u.UID, "kind": "verified_phone_registration", "at": now}); err != nil {
					return nil, err
				}
			}
		} else if p.Purpose == "login" {
			u = &models.User{}
			if err := r.db.Collection("users").FindOne(ctx, bson.M{"verifiedPhoneE164": p.Phone}).Decode(u); err != nil {
				if isNotFound(err) {
					return nil, ErrForbidden
				}
				return nil, err
			}
			if !u.Active() || !u.PhoneVerified() {
				return nil, ErrForbidden
			}
			// Serialize with freeze/profile changes and keep the verified identity immutable.
			if _, err := r.db.Collection("users").UpdateOne(ctx, bson.M{"_id": u.UID}, bson.M{"$set": bson.M{"telegramProofAt": now}}); err != nil {
				return nil, err
			}
		} else {
			return nil, ErrForbidden
		}
		var previous TelegramBinding
		e := r.db.Collection("telegram_bindings").FindOne(ctx, bson.M{"_id": p.TelegramID}).Decode(&previous)
		if e == nil && previous.UserID != u.UID {
			return nil, ErrConflict
		}
		if e != nil && !isNotFound(e) {
			return nil, e
		}
		_, e = r.db.Collection("telegram_bindings").UpdateOne(ctx, bson.M{"_id": p.TelegramID}, bson.M{"$set": bson.M{"userId": u.UID, "verifiedPhoneE164": p.Phone, "confirmedAt": now, "botStarted": true, "blocked": false}}, options.UpdateOne().SetUpsert(true))
		if e != nil {
			return nil, e
		}
		consumed, e := proofs.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"used": true}, "$unset": bson.M{"draft": "", "market": "", "codeHash": ""}})
		if e != nil {
			return nil, e
		}
		if consumed.MatchedCount != 1 {
			return nil, ErrConflict
		}
		result = u
		return nil, nil
	})
	if isDuplicate(err) {
		return nil, ErrConflict
	}
	return result, err
}
