# Phone-only accounts — coordinated staging change, 24 November 2026

Targets audited before changes: BakeU.UZ `Amin/3ca0df0`, Go Backend
`main/7780a70`. Work is on `codex/phone-only-auth-20261124` in each repository.
No production access, deployment, account migration or data deletion occurred.

## Audit findings and resulting behavior

The website already sent a phone-only form, but Go embedded the legacy
email/password signup DTO and rejected it. Seller proof completion created a
`draft` market without map coordinates. A full unique `users.email` index also
allows only one user with a missing email; removing form fields alone cannot
make multiple passwordless accounts work.

The coordinated Go phone API now accepts only:

| Operation | Required request fields |
| --- | --- |
| Buyer registration | `purpose: register`, `role: customer`, `firstName`, `lastName`, `phone` |
| Seller registration | Buyer fields with `role: seller`, plus `marketName`, `address`, `lat`, `lng` |
| Login | `purpose: login`, `phone` |

Names are trimmed, 1–80 Unicode characters each; shop name 2–150 and address
3–250. Latitude/longitude must both be supplied, finite and within ±90/±180;
zero is valid. Customer/login requests never inherit seller fields. The stored
`name` remains first + last for compatibility. New users have no email field,
password field, generated fake email or unused password hash. Existing legacy
email/hash fields are retained, never rewritten.

1. `POST /api/auth/phone/start`: pending Mongo `phone_proofs` only, 201 with
   `id`, `expiresAt`, `telegramUrl`; **no user, market or session is created**.
2. Deep link `https://t.me/<bot>?start=goauth_<48-hex-token>` asks for Telegram's
   own-contact button in a private chat. Foreign/missing-owner/forwarded contacts
   are rejected by Node. The trusted Bot calls
   `POST /api/integrations/telegram/auth/phone/contact` with the separate service
   credential, actor header and `{id,phone}`. Contact phone must match the
   normalized requested phone; the browser cannot call this with its JWT.
3. Go generates a six-digit code, stores only its hash and returns it only to
   the Bot, once. Codes/proofs expire in ten minutes; five wrong guesses lock
   a proof. Normalized verified E.164 phones have a partial unique index.
4. `POST /api/auth/phone/verify {id,code}` consumes the proof, creates the user,
   binds the trusted Telegram actor and, for a seller, creates an **approved**
   market with its address/coordinates and an audit event, in one transaction.
   A duplicate phone/Telegram binding rolls everything back. There is no
   separate application or admin email approval for a newly verified seller.
5. Browser mode uses `X-BakeU-Session: browser`, an allowed exact Origin, in-memory
   access JWT and the existing Secure/HttpOnly refresh cookie. Native refresh
   mode and one-use signed Mini App launch tickets are unchanged. Login loads
   the same UID by proven phone; it never recreates accounts from editable phone.

Only newly completed registration changes seller behavior. Existing draft,
pending, rejected or suspended shops are not auto-approved at startup. Verified
buyers can use the existing `SELLER_SELF_SERVICE` flow to open their own shop
with the same UID and purchase history rather than register a second account.

Old users, IDs, orders, password hashes, unknown legacy fields and session
families are preserved. Legacy credential login remains for existing clients;
the new website shows phone/Telegram login only. Mutable legacy phone values
are not automatically marked verified or merged. Unverified legacy owners need
the existing authenticated ownership/link process and reviewed migration plan;
matching a profile phone is not proof of ownership. Existing SQLite identities
are not silently imported or replaced by new Go identities.

Old in-flight registration proofs from before protocol v2 must request a fresh
code (they lack structured names/shop coordinates). Existing accounts and
sessions are unaffected. No Mongo transaction/readiness bypass was introduced.

## Email index transition: explicit, staging-only

Normal server startup only **adds** `unique_nonempty_email`, preserving unique
actual nonempty email addresses. It never drops an index. If phone mode is on
while a full unique email index such as `email_1` remains, startup reports
`PHONE_EMAIL_INDEX_MIGRATION_REQUIRED` instead of letting signup partially fail.

The new `/phone-email-index` binary is included in the Go image. It reads
`MONGODB_URI`/`MONGODB_DB` from the private service environment and prints only
index-plan metadata. Its default mode is read-only:

```sh
/phone-email-index
```

After review, a verified staging backup/restore, paused staging auth writers and
confirmation of the actual **staging** target, the operator may explicitly run:

```sh
/phone-email-index --apply-staging --confirm-database YOUR_STAGING_DATABASE
```

Apply requires `RAILWAY_ENVIRONMENT_NAME=staging`, an exact database-name match
and a database name containing `staging`. Do not spoof these guards or rename a
production database to bypass them. If an existing staging copy uses another
name, prepare a separately named isolated staging restore before this rehearsal.
The CLI creates the replacement unique nonempty-email constraint **first**, then
removes only inspected full unique single-field email indexes. It never drops
collections, edits documents, changes verified-phone indexes or resets sessions.
Re-running is idempotent. Do not run the apply command on production.

This tool was tested only on disposable local data, including a legacy
root-constraint setup. No live staging index was changed. On an existing full
index, keep phone mode disabled during any reviewed staging tool/setup phase,
then coordinate its activation with the new website. Keep backend startup logs
and a read-only index inventory; do not bypass an index conflict with deletion.

After passwordless users exist, an old image that recreates a full unique email
index may fail. Roll back with a reviewed forward-compatible image; do not
restore over live users/orders or delete passwordless accounts to make an old
index fit. Production index rollout needs a separate approved plan.

## Staging service settings after review (not applied here)

- Go: `AUTH_PHONE_ACCOUNTS=true`, `AUTH_REFRESH_SESSIONS=true`,
  `SELLER_SELF_SERVICE=true`, correct `TELEGRAM_BOT_USERNAME`, existing distinct
  `TELEGRAM_BRIDGE_TOKEN`, exact website Origin in `AUTH_BROWSER_ORIGINS`.
  Keep the existing JWT secret, Mongo service/volume, and account/session data.
- Bot: `GO_ACCOUNTS_ENABLED=true`, `GO_BACKEND_URL` pointing only to the staging
  backend, matching protected bridge token, existing real Bot token. Preserve
  its SQLite volume/path. Contact/code handlers create no SQLite customer.
- Website build: `VITE_GO_API_URL=/api`, `VITE_ACCOUNT_API_URL=/api`,
  `VITE_GO_PHONE_ACCOUNTS=true`. Runtime gateway: `WEB_ACCOUNTS_MODE=go`,
  staging-only Go/Bot upstreams and the exact staging website Origin.
  No service credential belongs in a VITE variable.

Coordinate both repositories; an older website sending only combined `name`
is incompatible with this structured-name registration contract. All flags
remain opt-in in source examples; no remote setting was changed.

## Live acceptance gates and present limits

User-supplied staging endpoints:

- Website: `https://bakeu-website-staging-stading.up.railway.app/healthz`
- Go: `https://bakeu-backend-staging-stading.up.railway.app/readyz`
- Go catalog: same host `/api/products` and `/api/markets`
- Bot: `https://bakeu-bot-staging-stading.up.railway.app/health`

Read-only GET attempts on all five were stopped by this environment's proxy:
CONNECT 403, destination HTTP status 000. This does **not** establish a Railway
service failure. The owner reported Go readiness/catalog HTTP 200 for the
currently deployed version; it does not verify these undeployed changes.
No new code was deployed, so live staging acceptance remains pending review.

After an operator-approved coordinated staging rollout, require all health
routes 200 and actual transaction readiness, then:

1. On a staging-only Telegram test Bot/account, register a buyer and seller with
   no email/password. Before own-contact/code confirmation, verify users/markets
   counts remain unchanged; do not use real production customers as fixtures.
2. Refuse a foreign/forwarded contact, wrong phone/code, expired/replayed code and
   concurrent duplicate phone. Confirm only one user and one seller market.
3. Confirm seller shop is approved with the exact selected point, address and
   owner UID; no application/email step. Customer-to-seller promotion retains UID.
4. Log out/in by phone+Telegram. Reload/restart services and confirm UID, existing
   refresh session behavior and old order IDs/history remain. Verify legacy
   records/session families against the pre-change snapshot privately.
5. Check the website gateway uses staging upstreams, legacy registration cannot
   write SQLite in Go mode, service bridge routes are not public through it,
   and private Bot credentials/codes do not appear in public logs or bundles.

See `PHONE_ONLY_AUTH_VALIDATION.json` for executed local proof, distinct from
these still-pending live Railway/real Telegram gates. No merge or deploy is
performed automatically by this branch.

## Reproduce the disposable Go tests

Use Go 1.26.4 and a **new local-only** MongoDB 8 replica-set fixture, never an
existing service/volume or a production tunnel. For example, publish a fresh
fixture only on loopback port 18671, initialize `rs0` inside it, then set:

```sh
BAKEU_TEST_MONGO_URI='mongodb://127.0.0.1:18671/?replicaSet=rs0&directConnection=true' go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux go build ./cmd/server
CGO_ENABLED=0 GOOS=linux go build ./cmd/phone-email-index
```

Integration suites create and clean only fresh randomized test databases on
that supplied fixture. Without the explicit loopback test URI, Mongo suites
skip; a skipped suite is not evidence of a successful integration test.

The companion frontend's `npm run test:phone-contract` expects a Go server
started on loopback 18681 with a fresh `bakeu_phone_contract_staging_test` DB,
`AUTH_PHONE_ACCOUNTS=true`, `AUTH_REFRESH_SESSIONS=true`,
`TELEGRAM_BOT_USERNAME=BakeUuzBot`, `AUTH_BROWSER_ORIGINS=https://phone-contract.test`,
a fake JWT secret and the fixed fake bridge credential
`local-test-telegram-bridge-secret-at-least32`. Set `MONGODB_URI` only to the
new local replica fixture. Wait for `/readyz` 200 before running that test.
Never use that public fixture credential on staging/production.
