// Explicitly disposable Docker rehearsal. Never accepts a production URI,
// existing container or existing volume. All resources have a fresh owned prefix.
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { randomBytes, createHash } from "node:crypto";
import { mkdtemp, writeFile, readFile, chmod, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import assert from "node:assert/strict";
const exec = promisify(execFile);
if (process.env.BAKEU_RUN_DISPOSABLE_REHEARSAL !== "true") {
  console.error("Refused: explicit BAKEU_RUN_DISPOSABLE_REHEARSAL=true is required."); process.exit(1);
}
const image = process.env.BAKEU_TEST_MONGO_IMAGE || "mongo:8.0";
const templateMode = image.startsWith("ghcr.io/railwayapp-templates/mongo-ha/mongo:") || image.startsWith("ghcr.io/railwayapp-templates/mongo-ha/mongo@");
const probe = resolve(process.env.BAKEU_TEST_READINESS_BINARY || "bin/mongo-readiness");
const server = resolve(process.env.BAKEU_TEST_SERVER_BINARY || "bin/server");
const startedAt = Date.now();
const labHost = "mongodb.railway.internal";
const prefix = "bakeu-rehearsal-" + randomBytes(8).toString("hex");
const network = prefix + "-net", source = prefix + "-source", restored = prefix + "-restored", app = prefix + "-api";
const volume = prefix + "-data", keys = prefix + "-keys";
let stage = "preflight", dir, networkCreated = false;
const volumes = [], containers = new Set();
async function docker(args, allowFailure = false) {
  try { return (await exec("docker", args, { encoding: "utf8", timeout: 90000, maxBuffer: 2 * 1024 * 1024 })).stdout.trim(); }
  catch (error) { if (allowFailure) return (error.stdout || "").trim(); throw Error("Docker rehearsal step failed"); }
}
async function waitFor(check) {
  const until = Date.now() + 60000;
  while (Date.now() < until) { try { if (await check()) return; } catch { /* no raw driver output */ } await new Promise((r) => setTimeout(r, 300)); }
  throw Error("Disposable service readiness timed out");
}
const auth = "const c=new Mongo('mongodb://127.0.0.1:27017/?directConnection=true'); const a=c.getDB('admin'); const auth=a.auth(process.env.MONGO_INITDB_ROOT_USERNAME,process.env.MONGO_INITDB_ROOT_PASSWORD); if(auth!==1&&auth?.ok!==1)throw Error('fixture authentication failed');";
async function shell(container, script) { return docker(["exec", container, "mongosh", "--quiet", "--eval", auth + script]); }
async function anonymousReadsDenied() {
  return (await docker(["exec", source, "mongosh", "--quiet", "--eval", "const c=new Mongo('mongodb://127.0.0.1:27017/?directConnection=true');try{c.getDB('bakeu').users.countDocuments({});print(false)}catch(e){print(e.code===13)}"])).endsWith("true");
}
async function startMongo(name, args, authenticated = true, replica = false) {
  const mounts = name === source ? ["--mount", `type=volume,source=${volume},target=/data/db`] : [];
  await docker(["run", "-d", "--name", name, "--network", network, ...(name === source ? ["--network-alias", labHost] : []),
    ...(authenticated ? ["--env-file", join(dir, replica && templateMode ? "replica.env" : "mongo.env")] : []), ...mounts,
    "--mount", `type=volume,source=${keys},target=/keys,readonly`, "--mount", `type=bind,source=${dir},target=/private`,
    "--entrypoint", templateMode && name === restored ? "docker-entrypoint-upstream.sh" : "docker-entrypoint.sh", image, "mongod",
    ...(templateMode && name === source ? ["--ipv6", "--bind_ip", "::,0.0.0.0", "--setParameter", "diagnosticDataCollectionEnabled=false"] : ["--bind_ip_all"]), ...args]);
  containers.add(name);
}
async function snapshot(container) {
  return JSON.parse(await shell(container, `const d=c.getDB('bakeu');const crypto=require('node:crypto');const out={};for(const n of ['users','markets','products','orders','auth_sessions']){const coll=d.getCollection(n);out[n]={count:coll.countDocuments({}),documents:crypto.createHash('sha256').update(EJSON.stringify(coll.find().sort({_id:1}).toArray())).digest('hex'),indexes:coll.getIndexes().map(i=>({key:i.key,unique:i.unique===true})).sort((a,b)=>JSON.stringify(a).localeCompare(JSON.stringify(b)))};}print(JSON.stringify(out));`));
}
try {
  await docker(["image", "inspect", image]); // use an explicitly present image; no implicit pull/upgrade
  await readFile(probe); await readFile(server);
  dir = await mkdtemp(join(tmpdir(), prefix + "-"));
  const password = randomBytes(32).toString("hex");
  const mongoEnv = `MONGO_INITDB_ROOT_USERNAME=bakeu_rehearsal\nMONGO_INITDB_ROOT_PASSWORD=${password}\nRAILWAY_PRIVATE_DOMAIN=${labHost}\nDATA_DIR=/data/db\n`;
  await writeFile(join(dir, "mongo.env"), mongoEnv, { mode: 0o600 });
  await writeFile(join(dir, "replica.env"), mongoEnv + `RS_ENABLED=true\nRS_SEEDS=${labHost}:27017\nRS_NAME=rs0\nRS_KEY=${password}\nHEALTH_API_PASSWORD=${password}\n`, { mode: 0o600 });
  await writeFile(join(dir, "keyfile"), randomBytes(756).toString("base64"), { mode: 0o600 });
  await writeFile(join(dir, "dump.yml"), `uri: "mongodb://bakeu_rehearsal:${password}@127.0.0.1:27017/?authSource=admin&directConnection=true"\n`, { mode: 0o600 });
  await docker(["network", "create", "--internal", network]); networkCreated = true;
  for (const name of [volume, keys]) { await docker(["volume", "create", name]); volumes.push(name); }
  await docker(["run", "--rm", "--mount", `type=volume,source=${keys},target=/keys`, "--mount", `type=bind,source=${dir},target=/private,readonly`, "--entrypoint", "sh", image,
    "-c", "cp /private/keyfile /keys/keyfile && chown mongodb:mongodb /keys/keyfile && chmod 400 /keys/keyfile"]);
  stage = "authenticated_standalone";
  await startMongo(source, templateMode ? [] : ["--auth"]);
  await waitFor(async () => (await shell(source, "print(c.getDB('admin').runCommand({ping:1}).ok)")).endsWith("1"));
  assert.equal(await anonymousReadsDenied(), true);
  await shell(source, "const d=c.getDB('bakeu');for(const n of ['users','markets','products','orders','auth_sessions'])d.getCollection(n).insertOne({_id:'fixture-'+n,marker:'must-survive',email:n+'@test.invalid'});d.users.createIndex({email:1},{unique:true});");
  const before = await snapshot(source);
  stage = "consistent_backup";
  // There are no other clients/writers in this isolated network. Full dump also
  // preserves the fixture admin account; --oplog is NOT used on standalone.
  await docker(["exec", "--user", `${process.getuid()}:${process.getgid()}`, source, "mongodump", "--config=/private/dump.yml", "--archive=/private/backup.gz", "--gzip"]);
  await chmod(join(dir, "backup.gz"), 0o600);
  const backup = await readFile(join(dir, "backup.gz")), sha256 = createHash("sha256").update(backup).digest("hex");
  assert.ok(backup.length > 0);
  stage = "isolated_restore";
  await startMongo(restored, [], false);
  await waitFor(async () => (await docker(["exec", restored, "mongosh", "--quiet", "--eval", "print(db.adminCommand({ping:1}).ok)"])).endsWith("1"));
  await docker(["exec", restored, "mongorestore", "--archive=/private/backup.gz", "--gzip"]);
  // The restored container has no init-root environment; supply fixture auth
  // only for this check. No password is present in command arguments or output.
  await docker(["cp", join(dir, "mongo.env"), `${restored}:/private/check.env`]);
  const restoredSnapshot = JSON.parse(await docker(["exec", restored, "sh", "-c", ". /private/check.env; export MONGO_INITDB_ROOT_USERNAME MONGO_INITDB_ROOT_PASSWORD; exec mongosh --quiet --eval \"$1\"", "sh", auth + "const d=c.getDB('bakeu');const crypto=require('node:crypto');const out={};for(const n of ['users','markets','products','orders','auth_sessions']){const coll=d.getCollection(n);out[n]={count:coll.countDocuments({}),documents:crypto.createHash('sha256').update(EJSON.stringify(coll.find().sort({_id:1}).toArray())).digest('hex'),indexes:coll.getIndexes().map(i=>({key:i.key,unique:i.unique===true})).sort((a,b)=>JSON.stringify(a).localeCompare(JSON.stringify(b)))};}print(JSON.stringify(out));"]));
  assert.deepEqual(restoredSnapshot, before);
  stage = "same_volume_replica_conversion";
  await docker(["stop", source]); await docker(["rm", "-v", source]); containers.delete(source);
  await startMongo(source, templateMode ? [] : ["--auth", "--replSet", "rs0", "--keyFile", "/keys/keyfile"], true, true);
  if (!templateMode) {
    await waitFor(async () => (await shell(source, "print(c.getDB('admin').runCommand({hello:1}).isreplicaset===true)")).endsWith("true"));
    // Deliberately gated lab-only initialization, never a generic entrypoint.
    await shell(source, "const h=c.getDB('admin').runCommand({hello:1});if(!h.isreplicaset||h.setName)throw Error('not an uninitialized fixture');let empty=false;try{c.getDB('admin').runCommand({replSetGetConfig:1})}catch(e){if(e.code===94)empty=true;else throw e;}if(!empty)throw Error('existing config must not be replaced');c.getDB('admin').runCommand({replSetInitiate:{_id:'rs0',members:[{_id:0,host:'mongodb.railway.internal:27017'}]}});");
  }
  await waitFor(async () => (await shell(source, "const h=c.getDB('admin').runCommand({hello:1});print(h.setName==='rs0'&&h.isWritablePrimary===true)")).endsWith("true"));
  assert.deepEqual(await snapshot(source), before);
  assert.equal(await anonymousReadsDenied(), true);
  const uri = `mongodb://bakeu_rehearsal:${password}@${labHost}:27017/?replicaSet=rs0&authSource=admin`;
  await writeFile(join(dir, "go.env"), `MONGODB_URI=${uri}\nMONGODB_DB=bakeu\nJWT_SECRET=${randomBytes(32).toString("hex")}\nPORT=8080\nGIN_MODE=release\n`, { mode: 0o600 });
  stage = "go_readonly_readiness";
  const report = JSON.parse(await docker(["run", "--rm", "--network", network, "--env-file", join(dir, "go.env"), "--mount", `type=bind,source=${probe},target=/mongo-readiness,readonly`, "--entrypoint", "/mongo-readiness", image]));
  assert.equal(report.storage.ready, true); assert.equal(report.transactionWriteTested, false);
  stage = "transaction_commit_abort";
  await shell(source, "const d=c.getDB('bakeu_tx_probe_lab');d.createCollection('orders');d.createCollection('notifications');const s=c.startSession();const t=s.getDatabase('bakeu_tx_probe_lab');s.startTransaction();t.orders.insertOne({_id:'committed'});t.notifications.insertOne({_id:'committed'});s.commitTransaction();s.startTransaction();t.orders.insertOne({_id:'aborted'});t.notifications.insertOne({_id:'aborted'});s.abortTransaction();s.endSession();if(d.orders.countDocuments({})!==1||d.notifications.countDocuments({})!==1)throw Error('partial transaction');");
  stage = "go_api_health";
  await docker(["run", "-d", "--name", app, "--network", network, "--env-file", join(dir, "go.env"), "--mount", `type=bind,source=${server},target=/bakeu-server,readonly`, "--entrypoint", "/bakeu-server", image]); containers.add(app);
  // Probe from inside the isolated network: no published database/API ports.
  const httpStatus = async (path) => Number(await docker(["run", "--rm", "--network", network, "--entrypoint", "node", "node:24-bookworm-slim", "--input-type=module", "-e", `console.log((await fetch('http://${app}:8080${path}',{signal:AbortSignal.timeout(3000)})).status)`]));
  await waitFor(async () => await httpStatus("/readyz") === 200);
  assert.equal(await httpStatus("/healthz"), 200);
  stage = "final_data_preservation";
  const after = await snapshot(source);
  for (const name of Object.keys(before)) { assert.equal(after[name].count, before[name].count); assert.equal(after[name].documents, before[name].documents); }
  console.log(JSON.stringify({ ok: true, dataSource: "disposable_fixtures_only", image, templateWrapperTested: templateMode, manualReplicaInitiation: !templateMode, backupBytes: backup.length, backupSHA256: sha256, restoreVerified: true, authenticationVerified: true, anonymousReadsDenied: true, originalVolumeReused: true, originalDocumentsPreserved: true, indexesPreservedOnConversion: true, transactionCommitAndAbortVerified: true, goHealthHTTP: 200, goReadinessHTTP: 200, elapsedMs: Date.now() - startedAt, productionTouched: false }));
} catch { console.error(JSON.stringify({ ok: false, stage, dataSource: "disposable_fixtures_only", productionTouched: false })); process.exitCode = 1; }
finally {
  for (const name of containers) await docker(["rm", "-f", "-v", name], true);
  for (const name of volumes) await docker(["volume", "rm", name], true);
  if (networkCreated) await docker(["network", "rm", network], true);
  if (dir) await rm(dir, { recursive: true, force: true });
}
