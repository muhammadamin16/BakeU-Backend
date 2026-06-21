package models

import "time"

// Role distinguishes the two kinds of Bakeu accounts.
type Role string

const (
	RoleSeller   Role = "seller"
	RoleCustomer Role = "customer"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleSeller || r == RoleCustomer
}

// User is a Bakeu account, stored in the users collection. The document _id is
// the app-generated user ID. PasswordHash is never serialized to JSON.
type User struct {
	UID          string    `bson:"_id" json:"uid"`
	Email        string    `bson:"email" json:"email"`
	PasswordHash string    `bson:"passwordHash" json:"-"`
	Role         Role      `bson:"role" json:"role"`
	Name         string    `bson:"name" json:"name"`
	Phone        string    `bson:"phone" json:"phone"`
	Address      string    `bson:"address" json:"address"`
	CreatedAt    time.Time `bson:"createdAt" json:"createdAt"`
}
