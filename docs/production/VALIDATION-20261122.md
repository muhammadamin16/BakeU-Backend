# Backend readiness and restore rehearsal — 22 November 2026

Starting main7ca3c31 includes merged production integration PR14 and Railway
configuration PR13. No old integration changes were duplicated. All changes are
source/testing tools on codex/mongodb-readiness-20261122, not a production rollout.

Executed validation:

- go test ./... against a disposable MongoDB8 replica set and a separate
  standalone topology; no integration tests intentionally skipped in those suites.
- go test -race ./... and go vet ./... against the same isolated test services.
- Node24 SQLite backup/export regression tests:2 passed, including committed WAL,
  source byte preservation, integrity, mode0600, output overwrite refusal and
  no password/ownership-proof export.
- Unit cases for standalone, uninitialized replica set, primary, direct secondary,
  mongos, missing/zero sessions, legacy isMaster shape, unsupported wire version,
  authorization/authentication/timeout and safe unknown failure classification.
- Command monitor verifies readiness sends only ping/hello/isMaster, without
  transaction/index/data writes.
- Real multi-collection transaction commit and intentional abort verify atomic
  behavior on a disposable replica set.

Reproducible Docker rehearsal (Docker, Node24, compiled Go server/probe required):

```sh
CGO_ENABLED=0 go build -o bin/server ./cmd/server
CGO_ENABLED=0 go build -o bin/mongo-readiness ./cmd/mongo-readiness
BAKEU_RUN_DISPOSABLE_REHEARSAL=true \
BAKEU_TEST_MONGO_IMAGE=ghcr.io/railwayapp-templates/mongo-ha/mongo@sha256:3acae065fa9c725197cfcc721fb3d2160b2972924b08bba71617215ffd8ca802 \
node scripts/rehearse-mongo-replica.mjs
```

Images must already be present; Node24 helper image is used for HTTP probes from
inside the isolated Docker network. The script does not accept a production URI,
existing volume or container. It uses fresh random resource names and fake
credentials in private files, publishes no database/API ports, and cleans up only
its own test resources. No source backup or credential enters Git/log output.

The actual pinned Railway image's default15s bootstrap and the owner's exact
Start Command were tested with a self-only RS_SEEDS and protected key/health
settings. The test used the same private-hostname shape and reused the original
fixture volume. A consistent authenticated full dump was restored on an isolated
copy with the same Mongo binary; document fingerprints, indexes and root
authentication matched before conversion. Anonymous reads were denied both
before and after activation. The wrapper then performed guarded
initialization itself: **no manual rs.initiate for the Railway image**.

Results: restoreVerified=true, authenticationVerified=true, anonymousReadsDenied=true,
originalVolumeReused=true, originalDocumentsPreserved=true,
indexesPreservedOnConversion=true, transactionCommitAndAbortVerified=true,
Go healthHTTP200 and readinessHTTP200. The synthetic archive was 1357 bytes and
had SHA-256 4dfa8a0175c9053baf28406a827fdd7c546e2656819ce5b82976aa9f38b524da.
Total lab orchestration took about48s; this is not predicted production downtime.
A separate official mongo:8.0 rehearsal also passed its guarded lab-only manual
bootstrap, backup restore, transactions and HTTP probes. It is an alternative,
not a reason to replace the owner's current image.

The owner-reported44 MB Railway backup is **not** the synthetic archive above.
It has not been obtained, checksummed or restore-tested here. Actual production
user counts/inventory, image digest, permissions/FCV, complete deployed logs,
real notification delivery and live E2E acceptance remain pending. Railway HTTPS
requests from this environment are blocked by the proxy before TLS (CONNECT403,
destination000). No production success is inferred.

See MONGODB_READINESS.md for the precise image-specific future change, backup
and downtime gates, authentication/private URI, alternatives and rollback.

The live-code read-only bot bridge preflight was also run against the new Go
handler with disposable replica and standalone databases: ready=true with
MONGO_TRANSACTIONS_READY, and ready=false with MONGO_STANDALONE respectively.
The --configured variant succeeded with bot account/order feature flags false;
no worker, lease or Telegram send was invoked. Current website/bot continuation
checks:88 integration tests,69 bot tests, build/lint and miniapp config passed;
see the companion website CONTINUATION-20261122.md for remaining live gates.

Publication: backend source pushed on codex/mongodb-readiness-20261122; the
companion website/bot branch has the same name. Draft PR creation was attempted
for both repositories but GitHub API returned Forbidden, so no new PR is claimed.
Review comparisons (not created PRs):
https://github.com/muhammadamin16/BakeU-Backend/compare/main...codex/mongodb-readiness-20261122
https://github.com/muhammadamin16/BakeU.UZ/compare/Amin...codex/mongodb-readiness-20261122
