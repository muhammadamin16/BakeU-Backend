# Audit legacy MongoDB phone values

Run from the backend checkout against a restored snapshot with a MongoDB read-only credential. Set MONGODB_URI and MONGODB_DB explicitly in the environment, then run:

```sh
go run ./cmd/phone-audit
```

The command reads only the phone field in users. It emits aggregate JSON counts for all users, missing and malformed values, values that would change under normalization, and collisions after normalization. Collision groups count shared normalized values and usersInCollision counts all affected records. An incomplete scan exits with an error and emits no report.

The scanner does not apply a migration. A number stored in a legacy profile is insufficient evidence to claim the number. Before adding a unique canonical phone index, reconcile verified identities against the SQLite snapshot as well, isolate unresolved collisions and verify backup restoration. Keep the existing MongoDB IDs and source backups. Add the index and switch registration only after testing the verified write path against a restored copy.
