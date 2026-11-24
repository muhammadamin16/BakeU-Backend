// Read-only by default. Apply is opt-in, restricted to an explicitly named
// staging database; never part of server startup or a production migration.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"bakeu/internal/config"
	"bakeu/internal/repository"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func stagingAllowed(environment, database, confirmation string) bool {
	return environment == "staging" && confirmation == database && strings.Contains(strings.ToLower(database), "staging")
}
func main() {
	apply := flag.Bool("apply-staging", false, "Apply the reviewed index-only plan to staging (default: read-only)")
	confirm := flag.String("confirm-database", "", "Exact database name, containing staging; required for apply")
	flag.Parse()
	fail := func(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
	cfg := config.Load()
	if cfg.ValidateMongoConfig() != nil {
		fail("MongoDB configuration is required; no changes made")
	}
	if *apply && !stagingAllowed(os.Getenv("RAILWAY_ENVIRONMENT_NAME"), cfg.MongoDB, *confirm) {
		fail("Apply requires RAILWAY_ENVIRONMENT_NAME=staging and exact confirmation of a database containing staging; no changes made")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		fail("MongoDB connection failed")
	}
	defer client.Disconnect(ctx)
	if *apply {
		if err := repository.ApplyStagingEmailIndexPlan(ctx, client.Database(cfg.MongoDB)); err != nil {
			fail("Staging index plan failed; inspect index metadata privately; no documents were changed")
		}
	}
	plan, err := repository.InspectEmailIndexes(ctx, client.Database(cfg.MongoDB))
	if err != nil {
		fail("Cannot inspect user index metadata")
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		ReadOnly bool                      `json:"readOnly"`
		Plan     repository.EmailIndexPlan `json:"plan"`
	}{!*apply, plan})
}
