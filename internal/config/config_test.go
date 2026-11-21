package config

import (
	"strings"
	"testing"
)

func TestMongoConfigurationDoesNotFallBackToLocalhostOrLeakURI(t *testing.T) {
	t.Setenv("MONGODB_URI", "")
	t.Setenv("MONGODB_DB", "bakeu_config_test")
	if cfg := Load(); cfg.MongoURI != "" || cfg.ValidateMongoConfig() == nil {
		t.Fatal("empty configuration must fail without dialing localhost")
	}
	t.Setenv("MONGODB_URI", "invalid://secret-marker")
	err := Load().ValidateMongoConfig()
	if err == nil || strings.Contains(err.Error(), "secret-marker") {
		t.Fatal("invalid URI must fail without exposing its contents")
	}
	for _, uri := range []string{"mongodb://127.0.0.1:27017", "mongodb+srv://staging.invalid"} {
		t.Setenv("MONGODB_URI", uri)
		if err := Load().ValidateMongoConfig(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MONGODB_DB", " ")
	if err := Load().ValidateMongoConfig(); err == nil {
		t.Fatal("an unset database must not silently select a default namespace")
	}

}
