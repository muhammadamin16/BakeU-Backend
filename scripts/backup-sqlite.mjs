// Consistent online SQLite backup, including committed WAL data. This does not
// copy/delete the live SQLite/WAL files or mutate the source application's data.
import { DatabaseSync, backup } from "node:sqlite";
import { open, readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { createHash } from "node:crypto";
import { parseArgs } from "node:util";
import { fileURLToPath } from "node:url";

export async function backupSQLite(source, output) {
  if (!source || !output || resolve(source) === resolve(output)) throw Error("invalid backup paths");
  const db = new DatabaseSync(source, { readOnly: true });
  try {
    // Reserve a NEW private file. Never let the backup API overwrite an existing
    // backup, source database, hard link or symlink supplied as destination.
    const file = await open(output, "wx", 0o600); await file.close();
    await backup(db, output);
  } finally { db.close(); }
  const copy = new DatabaseSync(output, { readOnly: true });
  try {
    if (copy.prepare("PRAGMA integrity_check").all().some((row) => Object.values(row)[0] !== "ok")) throw Error("backup integrity check failed");
  } finally { copy.close(); }
  const bytes = await readFile(output);
  return { integrityCheck: "ok", bytes: bytes.length, sha256: createHash("sha256").update(bytes).digest("hex") };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const { values } = parseArgs({ options: { source: { type: "string" }, output: { type: "string" } } });
    console.log(JSON.stringify(await backupSQLite(values.source, values.output)));
  } catch {
    console.error("SQLite backup failed. Check read-only source access and a NEW private destination. No original data or existing backup was deleted; retain any incomplete output privately and retry to a new path.");
    process.exitCode = 1;
  }
}
