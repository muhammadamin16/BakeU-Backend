# Backend production activation gate

22 November continuation: the owner confirmed standalone MongoDB and absent
RS_SEEDS/RS_ENABLED/RS_NAME/RS_KEY in the mongo-ha image. Start with
[MONGODB_READINESS.md](MONGODB_READINESS.md): it contains the image-specific,
backup-first same-volume plan. The owner's44 MB backup is **not restore-verified**.
Do not enable phone cutover, self-service or a production migration yet.

The complete cross-service variable matrix, precise activation order, website
proxy setup, bot root-directory settings, manual verification and rollback are
maintained with the website/bot source:
https://github.com/muhammadamin16/BakeU.UZ/blob/codex/production-integration-audit-20261121/docs/production/ROLLOUT.md

Read [MIGRATION.md](MIGRATION.md) before any account cutover. No production data,
variables, deployments, volumes, migrations or protected branches were changed
in this audit. Source is a review candidate; live success is not asserted.

Backend root: `/`, existing Dockerfile, Railway-generated PORT, private
MONGODB_URI reference, MONGODB_DB=bakeu, existing protected JWT_SECRET. Use the
same independent TELEGRAM_BRIDGE_TOKEN as the bot. AUTH_BROWSER_ORIGINS must
contain the exact website HTTPS origin. The browser uses the website's /api
same-origin gateway, never a Mongo connection or service bridge credential.

New railway.json checks **/readyz**, which now verifies Mongo is transaction
capable. /healthz only establishes process liveness. An existing standalone Mongo
may pass ping and fail readiness; preserve its volume and arrange a backed-up,
approved topology fix rather than deleting/recreating data. Actual Railway
Mongo topology was unavailable and is not assumed.

Flags AUTH_PHONE_ACCOUNTS, AUTH_REFRESH_SESSIONS, SELLER_SELF_SERVICE default off.
Go capabilities must be deployed and checked before bot GO_ACCOUNTS_ENABLED and
TELEGRAM_ORDERS_ENABLED; website Go-enabled VITE build and WEB_ACCOUNTS_MODE=go
come after those checks. Stage existing-user recovery first: old SQLite passwords
are scrypt, Go passwords bcrypt, and profile phone matches are not identity proof.

SELLER_SELF_SERVICE is a deliberate optional change: active phone-verified Go
users with a confirmed same-phone Telegram binding may open only their own
new/draft shop while keeping the same UID and buyer history. It cannot override
pending/rejected/suspended shops. Admin moderation uses ADMIN_USER_IDS (exact Go
UIDs, not Telegram IDs or editable emails), verified phones, explicit reasons and
expected statuses. Self-purchase is rejected; a seller may buy at other shops.

Rollback may disable new self-service but must preserve identities, mappings,
orders and volumes. Do not switch new Go users back to independent SQLite
registration. Never restore an old dump over post-snapshot writes automatically.
Production deployment/migration needs separate explicit approval and a verified
backup restore. Railway access, logs, actual inventory and real Telegram delivery
remain human verification gates.
