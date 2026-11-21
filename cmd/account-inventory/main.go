package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"time"

	"bakeu/internal/migration"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func main() {
	output := flag.String("output", "", "new private JSON inventory file (0600)")
	flag.Parse()
	if err := run(*output); err != nil {
		os.Stderr.WriteString("Read-only Mongo inventory failed. Check explicit snapshot connection, read-only access and a new private output path; connection details were not logged.\n")
		os.Exit(1)
	}
}
func run(output string) error {
	uri, database := os.Getenv("MONGODB_URI"), os.Getenv("MONGODB_DB")
	if uri == "" || database == "" {
		return os.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)
	inventory, err := migration.InventoryMongo(ctx, client.Database(database))
	if err != nil {
		return err
	}
	if err = migration.WritePrivateJSON(output, inventory); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"users": len(inventory.Users), "references": len(inventory.References), "source": "mongo-read-only"})
}
