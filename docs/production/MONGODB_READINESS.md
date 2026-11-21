# BakeU: standalone → single-node replica set, preserving the existing volume

Prepared 22 November 2026 (Tashkent). **PLAN ONLY. No production setting, service,
volume, account or database has been modified by this work.** Do not deploy this
plan until the owner explicitly approves the maintenance window and a production
backup has been restored and verified in isolation.

## Confirmed production inputs and root cause

The owner supplied these Railway observations; they were not obtained through
this execution environment (the environment proxy rejects Railway HTTPS):

| Setting | Owner-confirmed value |
| --- | --- |
| Image | `ghcr.io/railwayapp-templates/mongo-ha/mongo:8.0` |
| Custom Start Command | `docker-entrypoint.sh mongod --ipv6 --bind_ip ::,0.0.0.0 --setParameter diagnosticDataCollectionEnabled=false` |
| Volume | Existing MongoDB volume, `/data/db`, 5 GB |
| Private DNS | `mongodb.railway.internal` |
| Root credential variables | MONGO_INITDB_ROOT_USERNAME and MONGO_INITDB_ROOT_PASSWORD exist; values were not requested |
| Replication variables | RS_ENABLED, RS_NAME, RS_KEY and RS_SEEDS are all absent |
| hello | setName=null, isreplicaset=null, isWritablePrimary=true, logicalSessionTimeoutMinutes=30 |
| Go readiness | MONGO_TRANSACTIONS_REQUIRED |
| Railway backup | 44 MB; **restore not yet verified** |

The confirmed configuration-level cause is standalone mode: the audited image's
wrapper enables replication only when RS_SEEDS is nonempty and RS_ENABLED is
not literal `false`. Missing RS_SEEDS selects standalone even though the image
name contains `ha`. Sessions on standalone do not provide multi-document
transactions. A larger volume changes capacity, not topology.

**Important correction to generic advice:** this image is not just the official
mongo image. Its `docker-entrypoint.sh` shim routes the owner's exact Start
Command through `mongo-wrapper`. The wrapper adds `--replSet` and a properly
owned keyfile when its replication environment is configured. Absence of a
literal --replSet or --auth in Custom Start Command alone does not prove the
effective mongod flags or disabled authentication. Do not append raw replication
flags or create a second manual initiation path around the wrapper.

Audited public template source:
https://github.com/railwayapp-templates/mongo-ha/tree/6de44bbd975e7540bc99dc7c9759700feb40bac8
Read `mongo-wrapper/src/config.rs`, `keyfile.rs`, `main.rs`, `rs.rs`,
`standalone_recovery.rs`, `docker-entrypoint.sh` and README environment contract.
The public image was also downloaded and its actual shim inspected. The locally
tested image resolved to MongoDB8.0.38, digest
`sha256:3acae065fa9c725197cfcc721fb3d2160b2972924b08bba71617215ffd8ca802`.
**This is not a claim that production runs that digest.** Record Railway's
currently deployed digest/build version first; the floating tag can move.
Rehearse the deployed digest before using it. Do not combine an unreviewed image
upgrade/FCV change with the topology change.

## Why transactions are required

Go uses transactions for verified phone registration/uniqueness and Telegram
binding/one-use proof, seller activation/moderation audit, multi-shop order and
cart/idempotency/history/inbox/outbox commit, order transitions and reviews.
Keeping a plain ping healthy while bypassing these transactions could leave
partial accounts/orders or notifications. `/healthz` remains liveness;
`/readyz` remains a transaction-topology gate. The current Go driver requires
MongoDB4.2+; this server's 8.0 series supports both replica-set and sharded
transactions when the corresponding topology, FCV and permissions are correct.

The old readiness code conflated hello errors with missing transaction support
and could accept a direct secondary based only on setName+sessions. The new
read-only probe distinguishes failures and requires a writable primary/mongos,
positive session support and a supported wire version. It does not prove write
permissions or run a transaction on production. A real transaction is tested only
on an isolated database. No health check writes application data.

## 1. Read-only preflight; save findings privately

In Railway record the project/environment, service ID, existing volume ID and
mount, deployed image digest/version, current Start Command and flags, private
DNS/port, backup ID/time/type and current client URI settings. Do not print URI,
root password, `.railway-mongo-auth-pin`, keyfiles, or complete environments.

Safe commands in the Mongo container:

```sh
mongod --version
cat /proc/1/comm
command -v mongo-wrapper
sed -n '1,35p' /usr/local/bin/docker-entrypoint.sh
df -h /data/db /tmp
```

In the already authenticated Mongo shell, project only needed fields:

```javascript
const h = db.adminCommand({ hello: 1 });
printjson({setName:h.setName ?? null,isreplicaset:h.isreplicaset ?? null,
  isWritablePrimary:h.isWritablePrimary ?? h.ismaster ?? null,
  msg:h.msg ?? null,logicalSessionTimeoutMinutes:h.logicalSessionTimeoutMinutes ?? null,
  maxWireVersion:h.maxWireVersion ?? null});
const s = db.getSiblingDB('bakeu').runCommand({dbStats:1,scale:1048576});
printjson({ok:s.ok,collections:s.collections,objects:s.objects,
  dataMB:s.dataSize,storageMB:s.storageSize,indexMB:s.indexSize});
const auth = db.adminCommand({connectionStatus:1});
printjson({authenticated:auth.authInfo.authenticatedUsers.length>0,
  rootRole:auth.authInfo.authenticatedUserRoles.some(r=>r.role==='root'&&r.db==='admin')});
```

A shell may run hello without proving its identity. Verify the existing root
credential really authenticates, privately; a stored credential pin can outrank a
changed environment variable. If credentials are inconsistent, stop and resolve
that separately without resetting the existing user or revealing the pin.
An optional privileged read of `getParameter:1,featureCompatibilityVersion:1`
should project only its version; inability to read it does not imply standalone.
Do not raise/lower FCV manually as part of this plan.

Measure disk headroom: the 44 MB backup is not the live dataset size. Replication
adds an oplog plus journal/checkpoint needs; default oplog allocation can be
substantial on a 5 GB volume. Choose capacity/oplog policy based on measured
usage and the restore rehearsal. Do not place a backup archive on `/data/db` or
consume the remaining volume to diagnose it. Do not set an arbitrary oplog size
just to make startup pass.

## 2. Backups and isolated restore — mandatory before activation

1. Identify whether the existing 44 MB Railway backup is a physical volume
   snapshot or a logical Mongo archive. Record its ID/time; size alone does not
   establish integrity or restore success. Keep that backup and the volume.
2. Arrange an approved maintenance window that pauses **all application writers**
   and background consumers that write data. Do not stop Mongo before a logical
   dump; the server must remain available. Nothing has been paused by this audit.
3. Capture a final consistent Mongo backup after writer quiescence. Standalone
   cannot use `mongodump --oplog`; do not pretend an online standalone dump with
   concurrent writes is a global point-in-time snapshot. Use a provider-supported
   consistent snapshot or a full logical dump while writers are paused. Include
   relevant admin authentication/roles as well as bakeu data and index metadata.
4. Put exports/configs in a restricted directory outside `/data/db`, encrypt and
   move them to approved backup storage **before** redeploying the container.
   Preserve the live SQLite volume `/data/bakeu.sqlite`; take its consistent
   online backup through SQLite's backup API, never a raw copy of a live WAL DB.
5. Compute SHA-256 on actual downloadable backup artifacts, and record size/time,
   provenance, permissions and restore operator. Hashes do not replace restore.
6. Restore into a **new isolated environment/volume** using the same Mongo binary
   version/digest. Do not click a Railway Restore action that overwrites the
   production volume. If Railway does not support restore-to-isolated-target for
   that backup type, use an approved logical archive and an isolated restore.
7. The clone must not inherit production seeds, URLs or worker/bot tokens. Start
   with RS_SEEDS unset and no production connectivity. Verify document counts,
   stable IDs, required indexes, authentication/roles, Mongo integrity/logs and
   the SQLite `PRAGMA integrity_check`. Run the existing inventory/planner on
   restored copies; compare aggregates/references. Never publish backup content,
   hashes of passwords, session secrets or personal inventory records to Git.
8. Rehearse replica activation **on that clone**, using its own private hostname,
   not production's seed. Record observed downtime, primary election, backend
   readiness and actual transaction commit/abort. Keep writes isolated.

For a reviewed logical-dump procedure, use `mongodump --config=<private-config>`
with a new archive path and gzip. The config contains the authenticated local
URI and must be 0600, outside Git and terminal output; do not pass a password in
command-line arguments or paste the config into support chats. Do not overwrite
an existing archive. A physical snapshot is not a mongorestore archive.

For SQLite, the reviewed tool `scripts/backup-sqlite.mjs` (Node24+) creates a
**new**0600 backup including committed WAL, validates integrity and outputs only
size/SHA-256. It opens the source read-only, refuses overwrite and never deletes
the source or an existing backup. Make that audited tool available in the bot
runtime or run its equivalent SQLite backup API there; DATABASE_PATH is
`/data/bakeu.sqlite`. Example, after choosing a private destination outside /data:

```sh
node /private/backup-sqlite.mjs --source /data/bakeu.sqlite --output /private/bakeu-snapshot.sqlite
```

Run the existing backend SQLite/Mongo inventory and offline migration planner on
verified copies as documented in MIGRATION.md. No real production inventory has
been provided: users, sellers, shops, products, orders, sessions, conflicts and
re-verification counts remain **unknown**, not zero. The known44 MB backup is not
an account count. Existing scrypt/OTP-only legacy identities are not auto-merged
with Go bcrypt accounts; a matching editable phone is not ownership proof.

## 3. Reviewed future Railway change for this image

**Do not apply now.** Saving/applying these variables and redeploying enables the
wrapper to initiate a set, even if nobody types rs.initiate. Preconditions:
verified production restore + reconciliation, clone rehearsal using the deployed
digest, stable private DNS reachable from Go and Mongo itself, sufficient disk,
unchanged valid root credentials, owner approval and an agreed outage window.

Keep the **same MongoDB service, same volume ID, same `/data/db` mount and size**.
Do not redeploy a fresh template over it, reset data, create a replacement service
as an accidental shortcut, or add/scale other members. Preserve the deployed
image digest and the verified shim-based Custom Start Command exactly:

```text
docker-entrypoint.sh mongod --ipv6 --bind_ip ::,0.0.0.0 --setParameter diagnosticDataCollectionEnabled=false
```

Prepare these variables together, using Railway's protected reference picker:

| MongoDB variable | Reviewed intended value | Meaning |
| --- | --- | --- |
| RS_ENABLED | `true` | Enable wrapper replication mode |
| RS_NAME | `rs0` | New set name; keep immutable after initiation |
| RS_SEEDS | `mongodb.railway.internal:27017` | This existing node only; no fresh peers or public proxy |
| RS_KEY | Protected reference to the **unchanged** existing MONGO_INITDB_ROOT_PASSWORD | Template-supported shared key derivation; never print the resolved value |
| HEALTH_API_PASSWORD | Protected reference to the same unchanged root password | Protect wrapper's mutating health API |
| HEALTH_API_USERNAME | `railway` (default) | Health API basic-auth username |
| MONGO_PORT | `27017` (default) | Must match existing effective port and seed |
| HEALTH_PORT | `8080` (default) | Private wrapper HTTP health port, not the Mongo wire port |
| DATA_DIR | `/data/db` | Must agree with the existing persistent volume |
| RAILWAY_PRIVATE_DOMAIN | Preserve Railway-generated value; verify `mongodb.railway.internal` | Do not substitute localhost/public hostname |
| MONGO_INITDB_ROOT_USERNAME/PASSWORD | **Preserve existing protected values** | No root reset, reinitialization or password rotation |

Keep default bootstrap dwell (15s), current environment, disk, TLS policy and
private networking. Do not use the 1s acceleration sometimes used by tests in
production. The wrapper derives/chowns a400 keyfile under `/run/mongo-ha`, adopts
its proven credential pin and initializes only after its guarded bootstrap
checks. No manual keyfile command, raw --keyFile override or manual rs.initiate
is needed for this supported path. If init does not progress, stop activation
and inspect safe logs/configuration; do not force rs.reconfig or repeat initiation.
Do not toggle RS_ENABLED=false to recover after replication has begun (see rollback).

The outage is the graceful stop/start + wrapper/bootstrap/election + client
reconnect/redeploy + validation, not just the15s dwell. Measure it on the restored
clone; agree a maximum outage before the window. If exceeded, keep maintenance
and all auth/migration flags off while investigating. No duration is guaranteed.
A single-member set provides transactions, **not high availability**.

## 4. Backend URI and acceptance after approved conversion

Once hello confirms setName=rs0 and a writable primary with sessions, use the
existing authenticated **private** credential and authSource, adding the exact
set name without exposing it in logs:

```text
mongodb://<existing-user>:<URI-encoded-existing-password>@mongodb.railway.internal:27017/?authSource=<existing-auth-db>&replicaSet=rs0
```

Root accounts from MONGO_INITDB_ROOT_* authenticate against admin; preserve a
scoped application's actual authSource if it uses a different user. Do not
rotate JWT_SECRET or BOT_TOKEN, change MONGODB_DB=bakeu, or create a public DB
endpoint. Direct private data-node discovery is valid for this single node; an
existing directConnection=true is not itself a lack of transaction support and
may be retained only after testing against this primary. Do not point the URI
at a nonexistent HAProxy edge. Driver options cannot add transactions to standalone.

Re-run the projected hello and an authenticated read. New backend images include
`/mongo-readiness`, a **read-only** command using the service's existing protected
MONGODB_URI and MONGODB_DB; it never invokes application initialization, creates
indexes or writes data. Invoke it through supported direct command execution
(or an approved restricted diagnostic runtime if distroless exec is unavailable).
It emits no hostname, set name, URI, password or customer data. Expected result:
ready=true, topology=replica_set, writablePrimary=true, sessionsAvailable=true.
Then check Go /healthz AND /readyz. Do not change the health path to bypass503.

A successful capability probe does not prove FCV/write privileges. Run the actual
transaction test on the isolated clone/disposable database with the application's
intended credential, then reconcile preserved production data with backups and
read-only inventories. No production test writes are authorized by this plan.
Do not enable Go phone cutover or any account migration on readiness alone.

## 5. Rollback without losing data

- Before applying anything: retain deployment digest/start command/variables,
  encrypted Mongo+SQLite backups, original volumes and verified restore evidence.
- If a preflight fails, change nothing. If boot fails before any replica
  initialization/write, preserve the volume and inspect exactly what happened;
  revert only a reviewed startup/config change when its state is understood.
- After rs initialization or new writes, prefer repairing the replica set and URI
  while staying in maintenance. **Do not blindly unset RS_SEEDS, set
  RS_ENABLED=false, remove --replSet or boot another mongod over the volume.**
- The audited wrapper's standalone-recovery path replays oplog and may drop local
  replica metadata and preimage collections. That is a stateful conversion, not
  a harmless flag rollback, and is not authorized here.
- If a restore is required, restore verified backups to a separate isolated
  volume/environment first. Compare/reconcile post-backup writes and only switch
  application routing under a separate approved recovery plan. Never overwrite
  the live volume automatically or discard either copy. A new snapshot before
  recovery preserves evidence and later writes.
- Do not turn legacy SQLite signup back on after new Go identities exist; preserve
  Go UIDs, mappings, orders and both volumes. Keep uncertain Telegram sends
  uncertain; do not bulk reset/resend notification records.

## Alternatives if the existing image cannot be safely adopted

| Option | Data safety / tradeoff |
| --- | --- |
| Existing mongo-ha wrapper, same pinned image/service/volume | Preferred after restore rehearsal; guarded keyfile/bootstrap; preserves original data; one node is not HA |
| Separate Railway replica-set deployment with new dedicated volume(s) | Only with reviewed backup restore and controlled cutover; original service/volume retained; extra capacity/cost/DNS and reconciliation work |
| Explicitly managed official Mongo replica set | Operator owns keyfile storage/permissions, stable hostnames, primary election and backups; not a blind Start Command append |
| Multi-member Railway replica set | Separate persistent volume and stable identity per member; three voting data members for one-node-failure tolerance; do not scale one volume across replicas |
| MongoDB Atlas | Optional managed alternative after cost/region/private-network/data-migration review; not an automatic replacement |

## Local evidence and limits

The committed `scripts/rehearse-mongo-replica.mjs` creates only fresh,
uniquely named disposable Docker networks/volumes, fake credentials and fixture
records. It cannot accept a production URI or existing volume. It makes an
authenticated standalone backup, verifies an isolated restore including indexes
and authentication, reuses the original **test** volume, boots the actual Railway
image through the owner's Start Command and its native wrapper, and checks real
commit/abort plus Go /healthz and /readyz. Fixture counts and checksums must never
be reported as production inventory or the owner's44 MB backup verification.

**Next single operator action:** verify the existing44 MB backup by restoring it
into an isolated target (or obtain a consistent logical backup if its snapshot
format cannot be restored there). Keep production MongoDB and flags unchanged.
