# Legacy account inventory and migration gates

22 November status: the owner reports a44 MB Railway Mongo backup whose restore
has not been verified, and standalone Mongo with no RS_SEEDS. See
[MONGODB_READINESS.md](MONGODB_READINESS.md). Actual production inventories,
backup checksums and restored counts are still unavailable. Do not use the
synthetic report below as production evidence or activate identity migration.

No production import or automatic claim is shipped or executed. The commands below
read **consistent backup copies**; output contains personal data, is created 0600,
and must stay outside Git, logs, frontend assets and shared support messages.
Node 24+ is required for SQLite; Go version follows go.mod.

## Read-only dry run

1. Record deployed commits, flags and volume IDs. Obtain a verified Mongo dump
   with a consistent snapshot and a SQLite online backup (`sqlite3 .backup` or
   the SQLite backup API); do not copy only a live WAL database file. Keep the
   original volumes. Record SHA-256, time, owner, encryption and restore location.
2. Restore both backups into an isolated environment and test the restore.
   Use read-only Mongo credentials for inventory; do not run the application
   server against a production snapshot to inventory it (startup creates indexes).
3. Run from the backend repository; substitute local private paths and snapshot
   connection variables through protected environment settings, never command logs:

   ```sh
   node scripts/export-sqlite-inventory.mjs --snapshot /private/backup.sqlite --output /private/sqlite.json
   go run ./cmd/account-inventory -output /private/mongo.json
   go run ./cmd/account-migration-plan -sqlite-inventory /private/sqlite.json -mongo-inventory /private/mongo.json -plan /private/plan.json
   ```

   `MONGODB_URI` and `MONGODB_DB` must be explicit for inventory. No Mongo writes,
   collection creation, index creation, hashes or session secrets are exported.
   SQLite uses readOnly + query_only, validates integrity and source bytes,
   refuses a nonempty WAL and refuses overwriting output. It never deletes a file.
4. Review the aggregate summary and private per-source entries. Count SQLite
   applications, existing Mongo ownership/session/cart/favorite/order/review/
   notification/binding references and orphaned references. Fixes require separate
   approval; orphan findings do not authorize deletion or reassignment.

## Field and identity mapping

| SQLite | Mongo destination after independent proof | Rule |
| --- | --- | --- |
| users.id | legacy_identity_mappings `{namespace, sourceId, targetUID}` | Keep original IDs; never rewrite existing Mongo references |
| client / seller role | customer / seller proposal | No privilege is assigned by inventory or staging |
| firstName + lastName | users.name | Untrusted profile data, validated again |
| phone | verifiedPhoneE164 / phoneVerifiedAt | Only a NEW Go own-contact proof establishes verification |
| email | users.email | Placeholder `@accounts.bakeu.invalid` needs a real address |
| company, location, GPS | owner’s market draft | Preserve private inventory; validate address/GPS again; never auto-approve an imported shop |
| applications.user_id | original source ownership reference | Retain status and IDs; conflicts need review |
| scrypt hash/salt | **no copy** | Go uses bcrypt; set a new Go password after proof |
| sessions, pending challenges, miniapp sessions | **no copy** | New revocable Go session; legacy tokens are not Go credentials |

A matching email, editable phone, verified target phone, Telegram chat or seller
name is a **candidate**, not a merge authorization. Conflicting contacts require
proof of BOTH accounts. Completed legacy phone challenges were deleted and do
not provide durable per-account proof. Passwordless/unknown-hash legacy records
therefore require manual review. A random stored hash is not proof that a person
can supply its password.

## Secure claiming and idempotency boundary

`hold_for_claim` means a quarantine record, **not a usable migrated account**.
It still requires recent authenticated ownership of the specific legacy UID
(e.g. existing compatible scrypt password verified by the legacy service), fresh
Go own-contact proof and, for any existing target, explicit proof of that target.
No public automatic legacy-claim endpoint is enabled. An OTP-only legacy record
with missing historical evidence must not be claimed just by its current profile
phone. The owner/support must resolve it under a reviewed procedure.

After explicit production approval, implement/review the claim execution against
actual conflicts: immutable source/target uniqueness, short-lived one-use proof
bound to both UIDs, audit actor/time, transactionally inserted mapping, no role
promotion from untrusted source fields, no order/shop reassignment without proof,
and replay returning the same mapping. Re-inventory and reconcile counts after
execution. Do not enable production cutover until impacted existing users have an
agreed recovery path. The current PR prepares identification and safe staging,
not an unreviewed irreversible account merge.

The optional quarantine rehearsal command is deliberately **staging only**:

```sh
BAKEU_ALLOW_STAGING_CLAIMS=true go run ./cmd/stage-legacy-claims -plan /private/plan.json
```

It requires an explicit loopback Mongo URI and a database named
`bakeu_migration_staging_*`. The intact hashed plan is required. Only eligible
pending claim rows are inserted with `$setOnInsert`; reruns preserve existing or
claimed rows. It never writes users, passwords, roles, shops, sessions or bindings
and refuses database `bakeu`. This command is not a production migration switch.

## Executed synthetic report — 21 November 2026

This report is **synthetic disposable data**, not production counts. Executed
SQLite exporter → live Mongo inventory → offline planner → staging twice:

| Result | Count |
| --- | ---: |
| SQLite source users / applications | 4 / 1 |
| Existing Mongo users | 1 |
| Verified accounts created / automatic merges | 0 / 0 |
| Existing mapping skipped | 1 |
| Contact conflict review | 1 |
| Requires independent ownership verification | 1 |
| Manual review | 1 |
| SQLite orphan applications / Mongo orphan references | 1 / 1 |
| First staging inserted / second staging inserted | 1 / 0 |
| Second staging existing preserved | 1 |

The original snapshot remained byte-identical. Rejected overwrite, altered-plan,
production namespace and incompatible password-proof cases have regression tests.
Actual production counts, duplicates, restore verification and identity mappings
remain unavailable until the owner supplies access to verified backup copies.
