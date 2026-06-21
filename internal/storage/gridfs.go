// Package storage stores and serves product images using MongoDB GridFS,
// replacing Cloud Storage.
package storage

import (
	"context"
	"errors"
	"io"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ErrNotFound is returned when an image with the given ID does not exist.
var ErrNotFound = errors.New("image not found")

// Store reads and writes image files in a GridFS bucket.
type Store struct {
	bucket *mongo.GridFSBucket
}

func NewStore(db *mongo.Database) *Store {
	return &Store{bucket: db.GridFSBucket()}
}

// Upload streams r into GridFS, recording the content type in file metadata,
// and returns the new file's hex ID.
func (s *Store) Upload(ctx context.Context, filename, contentType string, r io.Reader) (string, error) {
	opts := options.GridFSUpload().SetMetadata(bson.M{"contentType": contentType})
	id, err := s.bucket.UploadFromStream(ctx, filename, r, opts)
	if err != nil {
		return "", err
	}
	return id.Hex(), nil
}

// Open returns the stored content type and a reader for the file with the given
// hex ID. The caller must close the returned stream. Returns ErrNotFound for an
// unknown or malformed ID.
func (s *Store) Open(ctx context.Context, id string) (string, *mongo.GridFSDownloadStream, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return "", nil, ErrNotFound
	}
	stream, err := s.bucket.OpenDownloadStream(ctx, oid)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) || errors.Is(err, mongo.ErrFileNotFound) {
			return "", nil, ErrNotFound
		}
		return "", nil, err
	}

	contentType := "application/octet-stream"
	if meta := stream.GetFile().Metadata; meta != nil {
		var m struct {
			ContentType string `bson:"contentType"`
		}
		if err := bson.Unmarshal(meta, &m); err == nil && m.ContentType != "" {
			contentType = m.ContentType
		}
	}
	return contentType, stream, nil
}
