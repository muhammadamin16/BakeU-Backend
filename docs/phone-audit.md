# Audit legacy MongoDB phone values

Run from the backend checkout against a restored snapshot with a MongoDB read-only credential. Set MONGODB_URI and MONGODB_DB explicitly in the environment, then run:

```sh
go run ./cmd/phone-audit
```

The command reads only the phone field in users. It emits aggregate JSON counts for all users, missing and malformed values, values that would change under normalization, and collisions after normalization. Collision groups count shared normalized values and usersInCollision counts all affected records. An incomplete scan exits with an error and emits no report.

The scanner does not apply a migration. A number stored in a legacy profile is insufficient evidence to claim the number. Before adding a unique canonical phone index, reconcile verified identities against the SQLite snapshot as well, isolate unresolved collisions and verify backup restoration. Keep the existing MongoDB IDs and source backups. Add the index and switch registration only after testing the verified write path against a restored copy.

## Compare SQLite and MongoDB snapshots

Run in a private terminal with Node.js 24 or newer and Bash. Pass a consistent SQLite backup created with SQLite's backup API or CLI `.backup`. A copy of the main file alone may omit committed WAL records.

```bash
set -o pipefail
node scripts/stream-sqlite-phones.mjs /path/to/backup.sqlite | go run ./cmd/phone-audit -compare-sqlite
```

The export streams raw phone values through the pipe. Do not run it on its own or capture its output in logs. The final report contains only counts for each source and the number of shared normalized values and affected accounts. Use the report only if the entire pipeline succeeds. A shared number is a candidate for review and does not establish that the two accounts have the same owner.
