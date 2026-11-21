package repository

import (
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"strings"
	"testing"
)

func TestReadinessDistinguishesTopologyCapabilitiesAndPrimary(t *testing.T) {
	for _, tt := range []struct {
		name, hello, code string
		ready             bool
	}{
		{"standalone", `{"isWritablePrimary":true,"maxWireVersion":25}`, "MONGO_STANDALONE", false},
		{"uninitialized", `{"isreplicaset":true,"isWritablePrimary":false,"maxWireVersion":25}`, "MONGO_REPLICA_SET_UNINITIALIZED", false},
		{"replica primary", `{"setName":"private-set","isWritablePrimary":true,"logicalSessionTimeoutMinutes":30,"maxWireVersion":25}`, "MONGO_TRANSACTIONS_READY", true},
		{"replica secondary", `{"setName":"private-set","isWritablePrimary":false,"secondary":true,"logicalSessionTimeoutMinutes":30,"maxWireVersion":25}`, "MONGO_PRIMARY_UNAVAILABLE", false},
		{"missing sessions", `{"setName":"private-set","isWritablePrimary":true,"maxWireVersion":25}`, "MONGO_SESSIONS_UNAVAILABLE", false},
		{"zero sessions", `{"setName":"private-set","isWritablePrimary":true,"logicalSessionTimeoutMinutes":0,"maxWireVersion":25}`, "MONGO_SESSIONS_UNAVAILABLE", false},
		{"mongos", `{"msg":"isdbgrid","isWritablePrimary":true,"logicalSessionTimeoutMinutes":30,"maxWireVersion":25}`, "MONGO_TRANSACTIONS_READY", true},
		{"legacy ismaster", `{"setName":"private-set","ismaster":true,"logicalSessionTimeoutMinutes":30,"maxWireVersion":8}`, "MONGO_TRANSACTIONS_READY", true},
		{"too old for current driver", `{"setName":"private-set","ismaster":true,"logicalSessionTimeoutMinutes":30,"maxWireVersion":7}`, "MONGO_WIRE_VERSION_UNSUPPORTED", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var hello mongoHello
			if err := bson.UnmarshalExtJSON([]byte(tt.hello), false, &hello); err != nil {
				t.Fatal(err)
			}
			out := classifyHello(hello)
			if out.Code != tt.code || out.Ready != tt.ready {
				t.Fatalf("unexpected readiness: %+v", out)
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "private-set") {
				t.Fatal("private topology leaked")
			}
		})
	}
}
func TestCapabilityFailureIsNotMisreportedAsStandalone(t *testing.T) {
	for _, tt := range []struct {
		err  error
		code string
	}{
		{mongo.CommandError{Code: 13, Message: "private URI"}, "MONGO_PROBE_UNAUTHORIZED"},
		{mongo.CommandError{Code: 18, Message: "private password"}, "MONGO_AUTHENTICATION_FAILED"},
		{context.DeadlineExceeded, "MONGO_PROBE_TIMEOUT"},
		{errors.New("unknown private error"), "MONGO_CAPABILITY_CHECK_FAILED"},
	} {
		if got := MongoProbeErrorCode(tt.err); got != tt.code {
			t.Fatalf("got %s want %s", got, tt.code)
		}
	}
}
