// mongo-readiness is a read-only diagnostic, not the application initializer.
// It never calls mongoclient.New, creates indexes, starts sessions or writes data.
package main

import (
	"bakeu/internal/config"
	"bakeu/internal/repository"
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"os"
	"strings"
	"time"
)

type connectionSummary struct {
	DatabaseConfigured bool   `json:"databaseConfigured"`
	SRV                bool   `json:"srv"`
	ReplicaSetOption   bool   `json:"replicaSetOption"`
	DirectConnection   bool   `json:"directConnection"`
	TLS                bool   `json:"tls"`
	ReadPreference     string `json:"readPreference"`
}
type diagnostic struct {
	Connection             connectionSummary         `json:"connection"`
	Storage                repository.MongoReadiness `json:"storage"`
	ReadOnly               bool                      `json:"readOnly"`
	TransactionWriteTested bool                      `json:"transactionWriteTested"`
}

func inspect(ctx context.Context, cfg config.Config) diagnostic {
	out := diagnostic{ReadOnly: true, Storage: repository.MongoReadiness{Code: "MONGO_CONFIGURATION_INVALID", Topology: "unknown"}}
	if cfg.ValidateMongoConfig() != nil {
		return out
	}
	opts := options.Client().ApplyURI(cfg.MongoURI).SetServerSelectionTimeout(5 * time.Second).SetConnectTimeout(5 * time.Second)
	if opts.Validate() != nil {
		return out
	}
	out.Connection = connectionSummary{DatabaseConfigured: true, SRV: strings.HasPrefix(cfg.MongoURI, "mongodb+srv://"), ReplicaSetOption: opts.ReplicaSet != nil && *opts.ReplicaSet != "", DirectConnection: opts.Direct != nil && *opts.Direct, TLS: opts.TLSConfig != nil, ReadPreference: "primary"}
	if opts.ReadPreference != nil {
		out.Connection.ReadPreference = opts.ReadPreference.Mode().String()
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return out
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Disconnect(closeCtx)
	}()
	out.Storage, _ = repository.InspectMongoReadiness(ctx, client)
	return out
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := inspect(ctx, config.Load())
	// Only the fixed projection above is serialized. In particular no driver error,
	// URI, username, password, hostname, replica set name or personal data is logged.
	_ = json.NewEncoder(os.Stdout).Encode(out)
	if !out.Storage.Ready {
		os.Exit(1)
	}
}
