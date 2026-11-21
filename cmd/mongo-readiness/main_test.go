package main

import (
	"bakeu/internal/config"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestInvalidConfigurationDoesNotLeakURIOrCredentials(t *testing.T) {
	out := inspect(context.Background(), config.Config{MongoURI: "https://private-user:private-password@private-host/?private-query=1", MongoDB: "private-database"})
	raw, _ := json.Marshal(out)
	if !out.ReadOnly || out.TransactionWriteTested || out.Storage.Ready || out.Storage.Code != "MONGO_CONFIGURATION_INVALID" || strings.Contains(string(raw), "private-") {
		t.Fatalf("unsafe diagnostic: %s", raw)
	}
}
