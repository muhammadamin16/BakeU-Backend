package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var ErrInvalidSession = errors.New("invalid or expired session")

const sessionLifetime = 30 * 24 * time.Hour
const maxRotations = 8192

type SessionRepo struct {
	sessions *mongo.Collection
}

type Session struct {
	ID     string `bson:"_id"`
	UserID string `bson:"userId"`
}

func NewSessionRepo(db *mongo.Database) *SessionRepo {
	return &SessionRepo{sessions: db.Collection("auth_sessions")}
}

func (r *SessionRepo) Create(ctx context.Context, userID string) (Session, string, error) {
	session := Session{ID: bson.NewObjectID().Hex(), UserID: userID}
	token, hash := newRefreshToken(session.ID)
	_, err := r.sessions.InsertOne(ctx, bson.M{
		"_id": session.ID, "userId": userID, "current": hash,
		"expiresAt": time.Now().Add(sessionLifetime), "rotations": 0,
	})
	return session, token, err
}

func activeSession(id string) bson.M {
	return bson.M{"_id": id, "expiresAt": bson.M{"$gt": time.Now()}, "revokedAt": bson.M{"$exists": false}}
}

func (r *SessionRepo) Active(ctx context.Context, id, userID string) (bool, error) {
	filter := activeSession(id)
	filter["userId"] = userID
	err := r.sessions.FindOne(ctx, filter, options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	return err == nil, err
}

func (r *SessionRepo) Rotate(ctx context.Context, token string) (Session, string, error) {
	id, hash, ok := splitRefreshToken(token)
	if !ok {
		return Session{}, "", ErrInvalidSession
	}
	next, nextHash := newRefreshToken(id)
	filter := activeSession(id)
	filter["current"] = hash
	filter["rotations"] = bson.M{"$lt": maxRotations}
	var session Session
	err := r.sessions.FindOneAndUpdate(ctx, filter, bson.M{
		"$set": bson.M{"current": nextHash}, "$push": bson.M{"spent": hash},
		"$inc": bson.M{"rotations": 1},
	}).Decode(&session)
	if errors.Is(err, mongo.ErrNoDocuments) {
		// Only a previously issued token can revoke the family. An unknown
		// secret with a valid session ID has no effect.
		replay := activeSession(id)
		replay["spent"] = hash
		_, err = r.sessions.UpdateOne(ctx, replay, bson.M{"$set": bson.M{"revokedAt": time.Now()}})
		if err != nil {
			return Session{}, "", err
		}
		return Session{}, "", ErrInvalidSession
	}
	return session, next, err
}

func (r *SessionRepo) Revoke(ctx context.Context, token string) error {
	id, hash, ok := splitRefreshToken(token)
	if !ok {
		return nil
	}
	filter := activeSession(id)
	filter["$or"] = bson.A{bson.M{"current": hash}, bson.M{"spent": hash}}
	_, err := r.sessions.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"revokedAt": time.Now()}})
	return err
}

func (r *SessionRepo) RevokeAll(ctx context.Context, userID string) error {
	_, err := r.sessions.UpdateMany(ctx, bson.M{"userId": userID, "revokedAt": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"revokedAt": time.Now()}})
	return err
}

func newRefreshToken(id string) (string, string) {
	var secret [32]byte
	rand.Read(secret[:])
	token := id + "." + hex.EncodeToString(secret[:])
	return token, refreshHash(token)
}

func refreshHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func splitRefreshToken(token string) (string, string, bool) {
	if len(token) != 89 {
		return "", "", false
	}
	id, secret, ok := strings.Cut(token, ".")
	if !ok || len(secret) != 64 {
		return "", "", false
	}
	if _, err := bson.ObjectIDFromHex(id); err != nil {
		return "", "", false
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return "", "", false
	}
	return id, refreshHash(token), true
}
