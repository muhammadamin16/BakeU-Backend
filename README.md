# BakeU Marketplace API

NestJS, strict TypeScript, Node.js 24, the official MongoDB driver and Socket.IO.
The server keeps the string application IDs and existing Go-era MongoDB
collections for users, markets, products, orders, carts, favorites and refresh
families. GridFS file IDs remain ObjectIds.

## Local development and tests

Use disposable MongoDB installations. Account proofs, checkout, chat writes and
administrative updates require a writable replica set or mongos.

```sh
npm ci
npm run build
MONGODB_URI='mongodb://127.0.0.1:27018/?directConnection=true' MONGODB_DB=bakeu_dev PORT=3400 npm start
```

`/health` checks the HTTP process. `/readyz` checks MongoDB and replica-set
readiness for configured phone accounts. `/openapi` exposes the implemented
routes.

```sh
TEST_MONGODB_URI=mongodb://127.0.0.1:27017 \
TEST_REPLICA_URI='mongodb://127.0.0.1:27018/?directConnection=true' npm test

# After building the sibling BakeU.UZ checkout with the staging flags below.
TEST_REPLICA_URI='mongodb://127.0.0.1:27018/?directConnection=true' npm run test:browser
```

The tests write fixtures into randomly named databases on disposable loopback
instances. The Chromium test uses ports 3847–3849, separate browser profiles
and the real local bot HTTP bridge with signed fixture initData.
Set `CHROMIUM_PATH` if Chromium is installed outside `/usr/bin/chromium`.

## Railway Staging

1. Use two reviewed feature branches and a separate staging project. Provision
   a writable MongoDB replica set and isolated database. For legacy rehearsal,
   restore a verified backup into that isolated database and inspect index
   definitions, per-collection counts and GridFS integrity before enabling
   writes. A non-sparse unique `email_1` blocks the second phone-only account
   without email. Resolve it through a separately reviewed optional-email
   index migration. Startup adds the partial verified-phone index and does
   not drop or rewrite existing indexes or infer ownership from editable
   phone values.
2. Deploy this repo using its root Dockerfile and Railway configuration.
   Set `MONGODB_URI`, `MONGODB_DB`, `JWT_SECRET`,
   `TELEGRAM_BOT_USERNAME`, `TELEGRAM_BRIDGE_TOKEN`, and exact HTTPS
   `AUTH_BROWSER_ORIGINS`. Set `ADMIN_USER_IDS` to the verified staging
   administrator's immutable UID. Check `/readyz`. To rehearse legacy
   active sessions, preserve the original signing key in the securely isolated
   rehearsal and seed or retain corresponding session fixtures. Fresh staging
   uses a separate key.
3. In BakeU.UZ, build the website using `VITE_GO_API_URL=/api`,
   `VITE_ACCOUNT_API_URL=/api` and `VITE_GO_PHONE_ACCOUNTS=true`.
   Set `WEB_ACCOUNTS_MODE=go`, explicit staging-only
   `WEB_GO_API_URL` and `WEB_BOT_API_URL`, and the exact HTTPS
   `WEB_ALLOWED_ORIGINS`. Keep the API on one instance for this setup because
   Socket.IO polling connections are instance-local.
4. Run the separate BakeU.UZ `src/bot` service with a dedicated staging
   `BOT_TOKEN`, separate SQLite path/volume, staging `MINI_APP_URL`,
   `GO_ACCOUNTS_ENABLED=true`, `TELEGRAM_ORDERS_ENABLED=true`, the same
   `TELEGRAM_BRIDGE_TOKEN`, and an HTTPS staging API in `GO_BACKEND_URL`.
   Run one polling bot per token. Check the bot health endpoint and verify a
   real own-contact flow and Telegram order notification with staging users.
   A Telegram send with an unknown result stays uncertain for operator
   review. It is never marked delivered automatically.
5. Verify buyer and seller registration, both admin entry routes, a three-photo
   product, a two-shop order, chat and account freeze before accepting staging.

Pending legacy OTPs and Telegram launch tickets may need to be requested
again across the implementation change. Signed legacy users and active refresh
families retain string IDs. An unverified legacy account claim still requires
a separate ownership verification procedure. Plan and subscription data can
be viewed in Admin. Paid checkout requires a selected payment provider and
sandbox credentials. There is currently no payment activation flow.

### Admin deep-link acceptance

In the sibling Website checkout, build the usual staging bundle and two extra
bundles with Vite. Use fresh output directories for the extra bundles.

```sh
VITE_GO_API_URL=/api VITE_ACCOUNT_API_URL=/api VITE_GO_PHONE_ACCOUNTS=true npm run build
VITE_GO_API_URL=/api VITE_GO_PHONE_ACCOUNTS=false npm run build -- --outDir /tmp/bakeu-admin-off
VITE_GO_API_URL='' VITE_GO_PHONE_ACCOUNTS=false npm run build -- --outDir /tmp/bakeu-admin-unconfigured
```

From the Backend checkout, run the browser suite with the normal disposable
`TEST_REPLICA_URI` and both `TEST_ADMIN_OFF_DIST=/tmp/bakeu-admin-off` and
`TEST_ADMIN_UNCONFIGURED_DIST=/tmp/bakeu-admin-unconfigured`. The Admin cases
use two additional local gateways on ports 3851 and 3852. They do not contact
Railway, a live Bot or a payment provider.
