package repository

import (
	"errors"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ErrNotFound is returned when a requested document does not exist.
var ErrNotFound = errors.New("not found")

// ErrDuplicate is returned when an insert violates a unique index (e.g. an
// already-registered email or a second review for the same product).
var ErrDuplicate = errors.New("duplicate")

// isNotFound reports whether a Mongo error means the document is missing.
func isNotFound(err error) bool {
	return errors.Is(err, mongo.ErrNoDocuments)
}

// isDuplicate reports whether a Mongo error is a unique-index violation.
func isDuplicate(err error) bool {
	return mongo.IsDuplicateKeyError(err)
}
