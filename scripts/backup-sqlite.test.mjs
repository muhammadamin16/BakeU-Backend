import test from "node:test";
import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
import { mkdtemp, readFile, stat, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { backupSQLite } from "./backup-sqlite.mjs";

test("online backup includes committed WAL and never overwrites source/existing backups", async () => {
  const dir = await mkdtemp(join(tmpdir(), "bakeu-backup-test-")), source = join(dir, "live.sqlite"), output = join(dir, "snapshot.sqlite");
  const writer = new DatabaseSync(source);
  try {
    writer.exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE users(id TEXT PRIMARY KEY); INSERT INTO users VALUES('preserve-me');");
    const main = await readFile(source), wal = await readFile(source + "-wal");
    const report = await backupSQLite(source, output);
    assert.equal(report.integrityCheck, "ok"); assert.match(report.sha256, /^[a-f0-9]{64}$/);
    assert.deepEqual(await readFile(source), main); assert.deepEqual(await readFile(source + "-wal"), wal);
    assert.equal((await stat(output)).mode & 0o777, 0o600);
    const copy = new DatabaseSync(output, { readOnly: true });
    try { assert.equal(copy.prepare("SELECT id FROM users").get().id, "preserve-me"); } finally { copy.close(); }
    const originalBackup = await readFile(output);
    writer.exec("INSERT INTO users VALUES('newer-write');");
    await assert.rejects(backupSQLite(source, output)); await assert.rejects(backupSQLite(source, source));
    assert.deepEqual(await readFile(output), originalBackup); assert.equal(writer.prepare("SELECT count(*) n FROM users").get().n, 2);
  } finally { writer.close(); await rm(dir, { recursive: true, force: true }); }
});
