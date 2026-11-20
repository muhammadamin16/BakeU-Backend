package repository

import (
	"context"
	"time"

	"bakeu/internal/models"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type TelegramLoginTicket struct {
	ID         string    `bson:"_id"`
	TelegramID string    `bson:"telegramId"`
	UserID     string    `bson:"userId"`
	ExpiresAt  time.Time `bson:"expiresAt"`
	Used       bool      `bson:"used"`
}

// The service caller must have verified Telegram's signed, recent initData.
// A Telegram profile alone never creates or matches a Go user by editable phone.
func (r *TelegramRepo) NewLoginTicket(ctx context.Context, actor string) (string, string, error) {
	who, err := r.Identity(ctx, actor)
	if err != nil {
		return "", "", err
	}
	if who == nil {
		return "", "", nil
	}
	token := randomToken()
	_, err = r.db.Collection("telegram_login_tickets").InsertOne(ctx, TelegramLoginTicket{ID: digest(token), TelegramID: actor, UserID: who.User.ID, ExpiresAt: time.Now().Add(time.Minute)})
	return token, who.User.ID, err
}

func (r *TelegramRepo) ConsumeLoginTicket(ctx context.Context, token string) (*models.User, error) {
	session, err := r.db.Client().StartSession()
	if err != nil {
		return nil, err
	}
	defer session.EndSession(ctx)
	var result *models.User
	_, err = session.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		var ticket TelegramLoginTicket
		filter := bson.M{"_id": digest(token), "used": false, "expiresAt": bson.M{"$gt": time.Now()}}
		if e := r.db.Collection("telegram_login_tickets").FindOne(ctx, filter).Decode(&ticket); e != nil {
			if isNotFound(e) {
				return nil, ErrConflict
			}
			return nil, e
		}
		who, e := r.Identity(ctx, ticket.TelegramID)
		if e != nil {
			return nil, e
		}
		if who == nil || who.User.ID != ticket.UserID {
			return nil, ErrForbidden
		}
		u, e := NewUserRepo(r.db).Get(ctx, ticket.UserID)
		if e != nil {
			return nil, e
		}
		if _, e = r.db.Collection("users").UpdateOne(ctx, bson.M{"_id": u.UID}, bson.M{"$set": bson.M{"telegramProofAt": time.Now().UTC()}}); e != nil {
			return nil, e
		}
		// Serialize with binding changes too; an old launch cannot restore a removed binding.
		updated, e := r.db.Collection("telegram_bindings").UpdateOne(ctx, bson.M{"_id": ticket.TelegramID, "userId": u.UID, "verifiedPhoneE164": u.VerifiedPhoneE164}, bson.M{"$set": bson.M{"lastLoginAt": time.Now().UTC()}})
		if e != nil {
			return nil, e
		}
		if updated.MatchedCount != 1 {
			return nil, ErrForbidden
		}
		consumed, e := r.db.Collection("telegram_login_tickets").UpdateOne(ctx, filter, bson.M{"$set": bson.M{"used": true}})
		if e != nil {
			return nil, e
		}
		if consumed.MatchedCount != 1 {
			return nil, ErrConflict
		}
		result = u
		return nil, nil
	})
	return result, err
}
