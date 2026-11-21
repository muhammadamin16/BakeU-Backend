import { DatabaseSync } from "node:sqlite";
import { parseArgs } from "node:util";
import { readFile, open, stat } from "node:fs/promises";
import { resolve } from "node:path";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";

export async function exportSQLiteInventory(snapshot, output, namespace = "bakeu-bot") {
  if (!snapshot || !output || resolve(snapshot) === resolve(output) || !/^[a-zA-Z0-9_-]{1,80}$/.test(namespace)) throw Error("invalid inventory arguments");
  const wal = await stat(snapshot + "-wal").catch(() => null);
  if (wal?.size) throw Error("use a consistent SQLite backup without a live WAL");
  const before = await readFile(snapshot);
  const db = new DatabaseSync(snapshot, { readOnly: true });
  let inventory;
  try {
    db.exec("PRAGMA query_only=ON; BEGIN;");
    if (db.prepare("PRAGMA integrity_check").all().some((row) => Object.values(row)[0] !== "ok")) throw Error("SQLite integrity check failed");
    const tables = db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").all().map((r) => r.name);
    const columns = new Set(db.prepare("PRAGMA table_info(users)").all().map((r) => r.name));
    if (!["id", "email", "phone", "role", "profile", "created_at", "password_hash", "salt"].every((key) => columns.has(key))) throw Error("unsupported legacy schema");
    const counts = {};
    for (const name of ["users", "applications", "sessions", "pending_registration", "telegram_users", "tickets", "replies", "outbox", "miniapp_sessions", "bot_state"]) {
      counts[name] = tables.includes(name) ? db.prepare(`SELECT COUNT(*) n FROM "${name}"`).get().n : 0;
    }
    const users = db.prepare("SELECT id,email,phone,role,profile,created_at,CASE WHEN length(password_hash)=0 THEN 'none' WHEN length(password_hash)=128 AND password_hash NOT GLOB '*[^0-9a-f]*' AND length(salt)=32 THEN 'node-scrypt' ELSE 'unknown' END password_algorithm FROM users ORDER BY id").all().map((row) => {
      let profile;
      try { profile = JSON.parse(row.profile); if (!profile || typeof profile !== "object" || Array.isArray(profile)) profile = null; } catch { profile = null; }
      const text = (key) => typeof profile?.[key] === "string" ? profile[key] : "";
      return { sourceId: row.id, email: row.email, phone: row.phone, role: row.role, firstName: text("firstName"), lastName: text("lastName"), shopName: text("company"), address: text("location"), lat: typeof profile?.lat === "number" ? profile.lat : null, lng: typeof profile?.lng === "number" ? profile.lng : null, createdAt: row.created_at, profileValid: profile !== null, passwordAlgorithm: row.password_algorithm, phoneOwnershipEvidence: "not-retained" };
    });
    const applications = tables.includes("applications") ? db.prepare("SELECT id,user_id,shop,address,status FROM applications ORDER BY id").all().map((r) => ({ sourceId: r.id, userId: r.user_id, shopName: r.shop, address: r.address, status: r.status })) : [];
    const foreignKeyViolations = db.prepare("PRAGMA foreign_key_check").all().length;
    db.exec("ROLLBACK");
    inventory = { schemaVersion: 1, namespace, capturedAt: new Date().toISOString(), sqliteSha256: createHash("sha256").update(before).digest("hex"), integrityCheck: "ok", foreignKeyViolations, counts, users, applications };
  } finally { db.close(); }
  const after = await readFile(snapshot);
  if (!before.equals(after)) throw Error("snapshot changed during inventory");
  const file = await open(output, "wx", 0o600);
  try { await file.writeFile(JSON.stringify(inventory) + "\n"); } finally { await file.close(); }
  return { users: inventory.users.length, applications: inventory.applications.length, foreignKeyViolations: inventory.foreignKeyViolations };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const { values } = parseArgs({ options: { snapshot: { type: "string" }, output: { type: "string" }, namespace: { type: "string", default: "bakeu-bot" } } });
    console.log(JSON.stringify(await exportSQLiteInventory(values.snapshot, values.output, values.namespace)));
  } catch { console.error("Inventory failed. Use a verified consistent SQLite backup, a new private output file and the supported schema. No credentials or source rows were logged."); process.exitCode = 1; }
}
