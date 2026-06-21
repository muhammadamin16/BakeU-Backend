package repository

import (
	"errors"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ErrNotFound is returned when a requested document does not exist.
var ErrNotFound = errors.New("not found")

// isNotFound reports whether a Mongo error means the document is missing.
func isNotFound(err error) bool {
	return errors.Is(err, mongo.ErrNoDocuments)
}
