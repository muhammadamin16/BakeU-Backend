// Pipe directly to phone-audit -compare-sqlite on a private machine.
import { DatabaseSync } from "node:sqlite";
import { once } from "node:events";

const [snapshot] = process.argv.slice(2);
if (!snapshot) {
  console.error("Pass a path to a consistent backup of the bot SQLite database.");
  process.exit(1);
}

try {
  const db = new DatabaseSync(snapshot, { readOnly: true });
  try {
    for (const { phone } of db.prepare("SELECT phone FROM users").iterate()) {
      if (!process.stdout.write(JSON.stringify(phone) + "\n")) {
        await once(process.stdout, "drain");
      }
    }
  } finally {
    db.close();
  }
} catch {
  console.error("Unable to read phone values from the SQLite snapshot.");
  process.exitCode = 1;
}
