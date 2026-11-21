package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// MongoReadiness contains only whitelisted capability metadata. Never include
// hello hosts, member addresses, set names, URIs, credentials or raw error text.
// Advertised support is not proof of transaction write permissions or FCV;
// exercise a real transaction only on an explicitly disposable database.
type MongoReadiness struct {
	Ready             bool   `json:"ready"`
	Code              string `json:"code"`
	DatabaseConnected bool   `json:"databaseConnected"`
	Topology          string `json:"topology"`
	WritablePrimary   bool   `json:"writablePrimary"`
	SessionsAvailable bool   `json:"sessionsAvailable"`
	MaxWireVersion    int32  `json:"maxWireVersion"`
}

type mongoHello struct {
	SetName         string `bson:"setName"`
	Message         string `bson:"msg"`
	IsReplicaSet    bool   `bson:"isreplicaset"`
	WritablePrimary *bool  `bson:"isWritablePrimary"`
	LegacyPrimary   *bool  `bson:"ismaster"`
	Sessions        *int32 `bson:"logicalSessionTimeoutMinutes"`
	MaxWireVersion  int32  `bson:"maxWireVersion"`
}

func classifyHello(hello mongoHello) MongoReadiness {
	out := MongoReadiness{DatabaseConnected: true, Topology: "standalone", Code: "MONGO_STANDALONE", MaxWireVersion: hello.MaxWireVersion}
	primary := hello.WritablePrimary
	if primary == nil {
		primary = hello.LegacyPrimary
	}
	out.WritablePrimary = primary != nil && *primary
	out.SessionsAvailable = hello.Sessions != nil && *hello.Sessions > 0
	switch {
	case hello.Message == "isdbgrid":
		out.Topology = "sharded"
	case hello.SetName != "":
		out.Topology = "replica_set"
	case hello.IsReplicaSet:
		out.Topology = "uninitialized_replica_set"
		out.Code = "MONGO_REPLICA_SET_UNINITIALIZED"
		return out
	default:
		return out
	}
	switch {
	case !out.WritablePrimary:
		out.Code = "MONGO_PRIMARY_UNAVAILABLE"
	case !out.SessionsAvailable:
		out.Code = "MONGO_SESSIONS_UNAVAILABLE"
	// mongo-driver/v2 v2.5.0 requires wire version >=8 (MongoDB4.2).
	// This also covers replica and sharded multi-document transaction minimums.
	case hello.MaxWireVersion < 8:
		out.Code = "MONGO_WIRE_VERSION_UNSUPPORTED"
	default:
		out.Ready = true
		out.Code = "MONGO_TRANSACTIONS_READY"
	}
	return out
}

func MongoProbeErrorCode(err error) string {
	var command mongo.CommandError
	if errors.As(err, &command) {
		switch command.Code {
		case 13:
			return "MONGO_PROBE_UNAUTHORIZED"
		case 18:
			return "MONGO_AUTHENTICATION_FAILED"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || mongo.IsTimeout(err) {
		return "MONGO_PROBE_TIMEOUT"
	}
	if mongo.IsNetworkError(err) {
		return "MONGO_NETWORK_UNAVAILABLE"
	}
	return "MONGO_CAPABILITY_CHECK_FAILED"
}

// InspectMongoReadiness never opens a transaction, creates indexes or writes
// data. It deliberately uses the app's existing client/options and primary read
// preference, so a wrong URI/replicaSet or unreachable advertised member fails.
func InspectMongoReadiness(ctx context.Context, client *mongo.Client) (MongoReadiness, error) {
	out := MongoReadiness{Topology: "unknown"}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		out.Code = MongoProbeErrorCode(err)
		return out, err
	}
	out.DatabaseConnected = true
	var hello mongoHello
	opts := options.RunCmd().SetReadPreference(readpref.Primary())
	err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}, opts).Decode(&hello)
	// MongoDB4.2 supports transactions and the current driver, but predates hello.
	// Retry only CommandNotFound; authorization/network errors are never hidden.
	var command mongo.CommandError
	if errors.As(err, &command) && command.Code == 59 {
		err = client.Database("admin").RunCommand(ctx, bson.D{{Key: "isMaster", Value: 1}}, opts).Decode(&hello)
	}
	if err != nil {
		out.Code = MongoProbeErrorCode(err)
		return out, err
	}
	return classifyHello(hello), nil
}
