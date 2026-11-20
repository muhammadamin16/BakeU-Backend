package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/models"
)

// UserRepo provides access to the users collection.
type UserRepo struct {
	users *mongo.Collection
}

func NewUserRepo(db *mongo.Database) *UserRepo {
	return &UserRepo{users: db.Collection("users")}
}

// Create assigns a new ID and inserts the user. Returns ErrDuplicate when the
// email is already registered (unique index on email).
func (r *UserRepo) Create(ctx context.Context, u *models.User) error {
	u.UID = bson.NewObjectID().Hex()
	_, err := r.users.InsertOne(ctx, u)
	if isDuplicate(err) {
		return ErrDuplicate
	}
	return err
}

// Get loads a user by ID. Returns ErrNotFound if none exists.
func (r *UserRepo) Get(ctx context.Context, uid string) (*models.User, error) {
	var user models.User
	if err := r.users.FindOne(ctx, bson.M{"_id": uid}).Decode(&user); err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &user, nil
}

// NamesByID returns a map of user ID to display name for the given IDs. Missing
// users are simply absent from the map. An empty input yields an empty map.
func (r *UserRepo) NamesByID(ctx context.Context, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	cur, err := r.users.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	var users []models.User
	if err := cur.All(ctx, &users); err != nil {
		return nil, err
	}
	for _, u := range users {
		names[u.UID] = u.Name
	}
	return names, nil
}

// GetByEmail loads a user by email. Returns ErrNotFound if none exists.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	if err := r.users.FindOne(ctx, bson.M{"email": email}).Decode(&user); err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &user, nil
}

// UpdateProfile sets only the editable fields and returns the updated user.
// It preserves identity and authentication fields, including fields written by
// newer versions of the service.
func (r *UserRepo) UpdateProfile(ctx context.Context, uid, name, phone, address string) (*models.User, error) {
	var user models.User
	err := r.users.FindOneAndUpdate(ctx, bson.M{"_id": uid},
		bson.M{"$set": bson.M{"name": name, "phone": phone, "address": address}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&user)
	if isNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}
