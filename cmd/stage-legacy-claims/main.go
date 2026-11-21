package main

import (
	"bakeu/internal/migration"
	"context"
	"encoding/json"
	"flag"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"os"
	"strings"
	"time"
)

func main() {
	p := flag.String("plan", "", "reviewed private dry-run plan")
	flag.Parse()
	if err := run(*p); err != nil {
		os.Stderr.WriteString("Staging claims refused or failed. Only explicit loopback disposable staging targets and intact plans are accepted; no credentials were logged.\n")
		os.Exit(1)
	}
}
func run(path string) error {
	uri, db := os.Getenv("MONGODB_URI"), os.Getenv("MONGODB_DB")
	if os.Getenv("BAKEU_ALLOW_STAGING_CLAIMS") != "true" || !(strings.HasPrefix(uri, "mongodb://127.0.0.1:") || strings.HasPrefix(uri, "mongodb://localhost:")) || !strings.HasPrefix(db, "bakeu_migration_staging_") {
		return os.ErrPermission
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var plan migration.Plan
	if err = json.Unmarshal(data, &plan); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)
	result, err := migration.StagePendingClaims(ctx, client.Database(db), plan)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
