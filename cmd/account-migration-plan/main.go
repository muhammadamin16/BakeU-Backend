package main

import (
	"bakeu/internal/migration"
	"encoding/json"
	"flag"
	"os"
)

func main() {
	source := flag.String("sqlite-inventory", "", "private SQLite inventory JSON")
	target := flag.String("mongo-inventory", "", "private Mongo inventory JSON")
	output := flag.String("plan", "", "new private plan JSON file (0600)")
	flag.Parse()
	if err := run(*source, *target, *output); err != nil {
		os.Stderr.WriteString("Dry-run failed. Check complete inventories and a new private plan file. No source data or credentials were logged.\n")
		os.Exit(1)
	}
}
func run(source, target, output string) error {
	if source == "" || target == "" || output == "" {
		return os.ErrInvalid
	}
	var legacy migration.SQLiteInventory
	var mongo migration.MongoInventory
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &legacy); err != nil {
		return err
	}
	raw, err = os.ReadFile(target)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &mongo); err != nil {
		return err
	}
	plan, err := migration.BuildPlan(legacy, mongo)
	if err != nil {
		return err
	}
	if err = migration.WritePrivateJSON(output, plan); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(plan.Summary)
}
