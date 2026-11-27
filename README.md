# BakeU Marketplace API

Node.js 24, NestJS and the official MongoDB driver. This branch currently serves the public catalog, market coordinates and GridFS reads. Use an isolated development database.

```sh
npm ci
npm run build
MONGODB_URI=mongodb://127.0.0.1:27017 MONGODB_DB=bakeu_dev PORT=3400 npm start
```

`/health` checks the HTTP process, `/readyz` checks MongoDB, and `/openapi` serves the implemented routes.

Run the HTTP contract suite against a disposable local MongoDB:

```sh
TEST_MONGODB_URI=mongodb://127.0.0.1:27017 TEST_REPLICA_URI='mongodb://127.0.0.1:27018/?directConnection=true' npm test
```

The suite writes fixtures to a randomly named database on the disposable instance. Stop the disposable instance when finished. To connect the existing website, build it with `VITE_GO_API_URL=/api` and `VITE_GO_PHONE_ACCOUNTS=false`. Start its gateway in legacy mode with `WEB_GO_API_URL=http://127.0.0.1:3400/api` and a local `WEB_BOT_API_URL` such as `http://127.0.0.1:3401/api`, to avoid routing to production. Product and order writes still require implementation.


## Phone accounts on isolated staging

1. Provision a separate MongoDB replica set and database. Set the backend
   `MONGODB_URI`, `MONGODB_DB`, `JWT_SECRET`, `TELEGRAM_BOT_USERNAME`,
   `TELEGRAM_BRIDGE_TOKEN` and exact HTTPS `AUTH_BROWSER_ORIGINS`.
   Existing signed sessions require the existing signing key and compatible
   MongoDB documents. Fresh staging uses its own key.
2. Before enabling registration on copied data, inspect the users index list
   and verify optional-email uniqueness on a disposable restored copy. A
   non-sparse unique `email_1` blocks the second phone account without email.
   Coordinate a separate reviewed index migration in that case. Startup adds
   the partial verified-phone index and does not drop or rewrite existing
   indexes or infer ownership from editable phone values.
3. Set up a dedicated staging bot with a matching bridge token and private
   backend URL. Enable `GO_ACCOUNTS_ENABLED` on that bot, and configure its
   Mini App URL to the staging website. Keep orders integration disabled until
   checkout and outbox exist.
4. Build the website with `VITE_GO_API_URL=/api`, `VITE_ACCOUNT_API_URL=/api`
   and `VITE_GO_PHONE_ACCOUNTS=true`. Use `WEB_ACCOUNTS_MODE=go` with
   explicit staging-only bot and backend upstreams and the exact HTTPS origin.
   Use the dedicated bot to complete a browser smoke test before wider use.

Existing pending proofs and launch tickets may need to be requested again
after changing implementations. Legacy verified users and active session
families retain string identifiers. Unverified legacy account claims require
separate ownership verification. Product, order, chat and admin workflows
are not yet ready for staging acceptance.
