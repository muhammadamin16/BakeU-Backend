package repository

import (
	"bakeu/internal/models"
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"time"
)

// ActivateOwnMarket preserves the UID, buyer history, sessions and unique shop
// ownership. Only verified owners can activate a new/draft shop. An existing
// moderation decision is never bypassed by self-service.
func (r *MarketRepo) ActivateOwnMarket(ctx context.Context, uid, first, last, name, address, description string, lat, lng float64) (*models.Market, error) {
	db := r.markets.Database()
	session, err := db.Client().StartSession()
	if err != nil {
		return nil, err
	}
	defer session.EndSession(ctx)
	var result *models.Market
	_, err = session.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		u, e := NewUserRepo(db).Get(ctx, uid)
		if e != nil {
			return nil, e
		}
		if !u.Active() || !u.PhoneVerified() {
			return nil, ErrForbidden
		}
		var binding TelegramBinding
		if e = db.Collection("telegram_bindings").FindOne(ctx, bson.M{"userId": uid, "verifiedPhoneE164": u.VerifiedPhoneE164}).Decode(&binding); e != nil {
			if isNotFound(e) {
				return nil, ErrForbidden
			}
			return nil, e
		}
		if binding.ConfirmedAt.IsZero() {
			return nil, ErrForbidden
		}
		m, e := r.GetBySeller(ctx, uid)
		isNew := errors.Is(e, ErrNotFound)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		if m != nil && m.Status == models.MarketApproved {
			if u.Role != models.RoleSeller {
				return nil, ErrConflict
			}
			result = m
			return nil, nil
		}
		if m != nil && m.Status != models.MarketDraft {
			return nil, ErrForbidden
		}
		now := time.Now().UTC()
		if _, e = db.Collection("users").UpdateOne(ctx, bson.M{"_id": uid}, bson.M{"$set": bson.M{"role": models.RoleSeller, "name": first + " " + last, "firstName": first, "lastName": last, "shopOpenedAt": now}}); e != nil {
			return nil, e
		}
		if m == nil {
			m = &models.Market{ID: bson.NewObjectID().Hex(), SellerID: uid, CreatedAt: now, Fulfillment: models.FulfillmentPickup, Phone: u.VerifiedPhoneE164, Email: u.Email}
		}
		if !m.Fulfillment.Valid() {
			m.Fulfillment = models.FulfillmentPickup
		}
		m.Name, m.Address, m.Description, m.Latitude, m.Longitude, m.Status, m.UpdatedAt = name, address, description, &lat, &lng, models.MarketApproved, now
		m.Phone = u.VerifiedPhoneE164
		if isNew {
			_, e = r.markets.InsertOne(ctx, m)
		} else {
			changed, updateErr := r.markets.UpdateOne(ctx, bson.M{"_id": m.ID, "sellerId": uid, "status": models.MarketDraft},
				bson.M{"$set": bson.M{"name": name, "address": address, "description": description,
					"latitude": lat, "longitude": lng, "phone": u.VerifiedPhoneE164,
					"status": models.MarketApproved, "fulfillment": m.Fulfillment, "updatedAt": now}})
			e = updateErr
			if e == nil && changed.MatchedCount != 1 {
				e = ErrConflict
			}
		}
		if e != nil {
			return nil, e
		}
		_, e = db.Collection("market_events").InsertOne(ctx, bson.M{"_id": bson.NewObjectID().Hex(), "marketId": m.ID, "actorId": uid, "kind": "verified_self_service_activation", "at": now})
		result = m
		return nil, e
	})
	if isDuplicate(err) {
		return nil, ErrConflict
	}
	return result, err
}

func (r *MarketRepo) Moderate(ctx context.Context, id, actor string, expected, target models.MarketStatus, reason string) (*models.Market, error) {
	session, err := r.markets.Database().Client().StartSession()
	if err != nil {
		return nil, err
	}
	defer session.EndSession(ctx)
	var result *models.Market
	_, err = session.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		m, e := r.Get(ctx, id)
		if e != nil {
			return nil, e
		}
		if m.Status != expected {
			return nil, ErrConflict
		}
		previousUpdated := m.UpdatedAt
		m.Status, m.UpdatedAt = target, time.Now().UTC()
		m.RejectionReason = reason
		m.ReviewedAt = m.UpdatedAt
		changed, e := r.markets.UpdateOne(ctx, marketExpectedFilter(m, expected, previousUpdated),
			bson.M{"$set": bson.M{"status": target, "rejectionReason": reason, "updatedAt": m.UpdatedAt, "reviewedAt": m.ReviewedAt}})
		if e != nil {
			return nil, e
		}
		if changed.MatchedCount != 1 {
			return nil, ErrConflict
		}
		_, e = r.markets.Database().Collection("market_events").InsertOne(ctx, bson.M{"_id": bson.NewObjectID().Hex(), "marketId": id, "actorId": actor, "kind": "admin_moderation", "from": expected, "to": target, "reason": reason, "at": m.UpdatedAt})
		result = m
		return nil, e
	})
	return result, err
}

func (r *MarketRepo) UpdateExpected(ctx context.Context, m *models.Market, expected models.MarketStatus, updatedAt time.Time) error {
	changed, err := r.markets.UpdateOne(ctx, marketExpectedFilter(m, expected, updatedAt), bson.M{"$set": marketFields(m)})
	if err != nil {
		return err
	}
	if changed.MatchedCount != 1 {
		return ErrConflict
	}
	return nil
}

func marketExpectedFilter(m *models.Market, expected models.MarketStatus, updatedAt time.Time) bson.M {
	filter := bson.M{"_id": m.ID, "sellerId": m.SellerID, "status": expected, "updatedAt": updatedAt}
	if updatedAt.IsZero() {
		delete(filter, "updatedAt")
		filter["$or"] = bson.A{bson.M{"updatedAt": updatedAt}, bson.M{"updatedAt": bson.M{"$exists": false}}}
	}
	return filter
}
