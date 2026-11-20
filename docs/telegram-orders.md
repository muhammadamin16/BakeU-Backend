# Telegram accounts and shared orders

This implements the bot bridge consumed by `BakeU.UZ/src/bot/orders`, using the existing Go users, markets, products, carts and orders in the same configured MongoDB database. No second account/order database, paid SMS, production data import or automatic merge is involved.

## Configuration

Keep the current `MONGODB_URI`, `MONGODB_DB`, `JWT_SECRET` and persistent data. Mongo must support transactions (Atlas/replica set, as the existing checkout already requires).

Backend environment:

```dotenv
TELEGRAM_BRIDGE_TOKEN=<new-random-service-secret-at-least-32-characters>
TELEGRAM_SITE_URL=https://bakeu-website-production.up.railway.app
```

`TELEGRAM_SITE_URL` must exactly match the bot's public `MINI_APP_URL` origin and path. Use `https://bakeu.uz` instead on both services if that is the actual frontend. This is the website URL, not the API/bot URL. No URL credentials or query parameters are allowed. The bridge token must differ from JWT_SECRET and BOT_TOKEN; never put it in a `VITE_` variable, source or chat.

Bot environment (same secret):

```dotenv
TELEGRAM_ORDERS_ENABLED=true
GO_BACKEND_URL=https://YOUR-GO-BACKEND.up.railway.app
TELEGRAM_BRIDGE_TOKEN=<same-service-secret>
```

A local operator can generate a secret with `openssl rand -hex 32` and place it directly in both services' protected variables. This document contains no real credentials. The bot is still deployed with Root Directory `src/bot`.

The website integration branch `codex/connect-telegram-backend` adds `/#/telegram/link?challenge=...`, `/#/account/orders` and `/#/shop/:id`. It discovers only the public Go API URL from the bot's `/api/telegram/config`; that response never contains the service token. If `VITE_GO_API_URL` is configured, it reuses the existing shared Go browser-session client. That mode requires the existing AUTH_REFRESH_SESSIONS=true, exact AUTH_BROWSER_ORIGINS and HTTPS same-site API or same-origin proxy. Otherwise the new pages can use a short-lived-in-memory normal Go access token and reauthenticate after reload; orders remain in Mongo. Do not use Node/SQLite/demo account tokens as Go credentials.

A user must already have a real Go account; a seller must have an existing market/application. Phone contact plus fresh authenticated account-password proof binds that UID. Legacy SQLite accounts are not silently copied/merged or granted Go access. Existing catalog/shop approval rules remain authoritative.

## Implemented endpoints

Normal Go JWT auth:

- GET `/api/orders/capabilities`: workflowVersion=2, history/idempotency/deliveryScheduling=true.
- GET `/api/orders`: own placed/incoming orders with persisted history and snapshots.
- POST `/api/orders`: authoritative multi-shop checkout. V2 requires Idempotency-Key; items contain productId/qty only. One delivery choice or `deliveries` keyed by actual shop IDs can specify independent schedules, pickup/delivery and optional paired GPS.
- PATCH `/api/orders/:id/status`: owned seller operation with expectedStatus, optional expectedVersion and Idempotency-Key. Existing five-state legacy requests remain compatible for legacy orders; new V2 orders require the guarded protocol.
- POST `/api/me/telegram/link/confirm`: challengeId + fresh password under the existing authenticated UID. Unique binding and one-time challenge consume commit together.
- GET `/api/me/notifications`: own durable website inbox, including unlinked/blocked/uncertain Telegram delivery.

Server-only prefix `/api/integrations/telegram`, Bearer service credential and (for actor routes) trusted `X-Telegram-User-Id` from private bot updates:

- GET `/me`; POST `/link/challenges` with own-contact phone and requestedRole CLIENT/SELLER.
- GET `/shop`, `/shop/products`, `/orders?audience=buyer|seller&view=all|active|history|status&page=0&limit=5`, `/orders/:id`.
- PATCH `/orders/:id/status`, expectedStatus/status plus Idempotency-Key.
- POST `/notifications/claim`, `/notifications/:id/begin`, `/notifications/:id/ack`, `/notifications/:id/fail`.

A browser Origin is refused on service routes. Normal JWTs cannot impersonate the bot; service credentials do not authorize browser account proof. Phone match alone never grants access. Challenges are random, stored by digest, expire in ten minutes, rate limited, one-use and constrained to the role/phone of the freshly proved account. There is no account enumeration at challenge creation: seller absence/wrong role is resolved at authenticated proof. No new account or role promotion happens during linking. Disabled/frozen/non-active users are refused by commerce/link operations.

## Order consistency

V2: pending → accepted → preparing → ready → delivering → delivered → completed. Pickup goes ready → completed. Pending may be rejected; accepted/preparing/ready may be cancelled. Terminal orders cannot reopen. Seller and shop ownership are checked afresh. Prices, titles, buyer contact and shop IDs come from Mongo, never browser totals. Delivery is validated in the future (at most 366 days), latitude/longitude must both be present and in range, and pickup strips private GPS/address. Old stored status/history is retained; legacy projections enrich missing old metadata without rewriting production documents.

Order, status history, idempotency response, website inbox and notification intents commit in one transaction. Checkout clears the server cart in that same transaction. A conflicting body under a reused key or stale status/version is 409. Dashboard/review queries include completed as well as delivered without double counting an order.

The outbox is in `order_notifications`, in the same database. Unique event IDs derive from order/revision/recipient/kind. Leases and begin are CAS guarded. Retries honor known 429 rejection and stop after eight attempts; 403/other 4xx preserve blocked/failed inbox entries. A send timeout/5xx, lost response or expired sending lease becomes uncertain and is never blindly replayed. ACK retries are idempotent, including a matching late ACK. Telegram does not provide exactly-once send semantics; uncertain delivery remains reviewable in the website inbox. A Telegram failure never deletes or rolls back an order.

## Verification

`go test ./...` passes with `BAKEU_TEST_MONGO_URI=mongodb://127.0.0.1:<disposable-port>/?replicaSet=rs0`. Integration tests refuse non-loopback URIs and use/drop only generated `bakeu_*_test_*` databases. They exercise actual Mongo transactions, three identities/two sellers, contact/password proof, binding conflicts/replay, authoritative prices/GPS/pickup, multi-seller checkout replay, stale/foreign/concurrent status writes, history after router recreation, durable notifications, lease loss/late ACK and compatibility with the earlier five-state history API.

A separate headless Chromium run used real Go HTTP endpoints and a disposable MongoDB replica set: buyer/seller browser proof, two-shop checkout, seller website acceptance, buyer live progress, bot bridge reads, native location payload and worker receipt dedup all passed. Only Telegram delivery was simulated; no production bot messages or production DB operations were performed. Website/bot suite: 59 tests; existing Go shared-session suite: 3 tests; website build/scoped lint passed. Live Railway environment variables, domains and a real Telegram token must still be checked after deployment is explicitly authorized.
# Verified phone identity

Telegram confirmation now claims `users.verifiedPhoneE164` and `phoneVerifiedAt`
in the same transaction as binding the authenticated UID and consuming its
challenge. A partial unique index excludes unverified legacy profiles and
prevents two UIDs from claiming one number. `users.phone` remains an editable
contact; profile changes cannot change or replace the proven identity.

An older binding without a matching verified identity must repeat the existing
own-contact + account-password proof. Startup does not backfill phone ownership
or merge accounts. A conflicting proof rolls back its user, binding and challenge
changes together. Existing production data is never inferred or migrated.

## Confirmed registration and login (opt-in)

Deploy both services first, then configure `AUTH_PHONE_ACCOUNTS=true` and
`TELEGRAM_BOT_USERNAME` on Go, `GO_ACCOUNTS_ENABLED=true` on the bot, and
`VITE_GO_PHONE_ACCOUNTS=true` with `VITE_GO_API_URL` on the website. The bot and
Go share the existing protected bridge credential. Browser mode also requires
refresh sessions and an exact allowed HTTPS origin/same-site proxy as above.
Keep these flags false until the coordinated release is reviewed.

`POST /api/auth/phone/start` takes `purpose: register|login` and `phone`.
Registration also takes a real email, password (8–72 bytes), name, role
(`customer|seller`), optional address and seller `marketName`. It returns only a
challenge ID, expiry and Telegram launch URL. No user is created yet.
The launch payload fits Telegram's 64-character limit.

The private bot checks `contact.user_id === from.id` and sends the owned phone
and challenge to service-only `POST /api/integrations/telegram/auth/phone/contact`.
Go issues a six-digit code once, with a ten-minute expiry and five attempts.
Only the authenticated bot gets the plaintext code; Mongo stores only its digest.
`POST /api/auth/phone/verify` with `{id, code}` consumes the proof, creates/loads
the Go UID and binds Telegram in a single transaction, then issues the existing
Go access/refresh session. Login looks up the proven E.164 field only, never the
editable contact. A duplicate email/verified phone/binding rolls back the whole
registration. A seller receives a private `draft` market, not automatic approval.
The existing multipart `POST /api/market` submits its logo/GPS/details for review.

When enabled, old unverified `/api/auth/signup` returns 409 with the phone-proof
route. Existing email/password login remains available to prove and link old Go
accounts; no SQLite user is copied or merged. Frozen accounts cannot log in,
refresh, read private account routes or execute role-based actions.

Verified locally: concurrent phone claims and registrations, transaction rollback,
password/contact separation, own-contact bot checks, hashed code storage,
one-use/replay/attempt limits, shared secure-cookie refresh after router/browser
reload, seller draft submission with logo and GPS, and private pending markets.
The browser used real Go and a disposable MongoDB replica set; Telegram transport
was simulated. Production variables, live Telegram delivery and legacy-data
migration still require a separately reviewed activation.

