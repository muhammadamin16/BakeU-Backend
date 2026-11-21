# BakeU Railway staging

Select the staging Environment named stading. Confirm that mongo is the intended PRIMARY member of rs0 and that the old MongoDB-1_Wh service is left untouched. Choose its data volume and backup before any deployment. Use Railway's private address discovery to verify the advertised member hostname from the Backend service.

| Service | Variable | Example | Required | Purpose |
| --- | --- | --- | --- | --- |
| Go | MONGODB_URI | mongodb://<user>:<encoded-password>@mongo.railway.internal:27017/?replicaSet=rs0&authSource=<auth-db> | Yes | Connect to the staging replica set with a database-scoped credential |
| Go | MONGODB_DB | bakeu_staging | Yes for this runbook | Select the intended data namespace |
| Go | JWT_SECRET | protected staging secret | Yes | Sign sessions |
| Go | AUTH_REFRESH_SESSIONS, AUTH_PHONE_ACCOUNTS | true | For integrated flow | Enable cookie refresh and verified registration |
| Go | AUTH_BROWSER_ORIGINS | https://<staging-website> | For browser cookies | Exact HTTPS origin without path |
| Go, Bot | TELEGRAM_BRIDGE_TOKEN | separate protected staging secret | For Telegram | Same service credential on both services |
| Go | TELEGRAM_SITE_URL, TELEGRAM_BOT_USERNAME | staging website and test bot username | For Telegram | Account proof URLs and bot contact flow |
| Bot | BOT_TOKEN | protected test bot token | Yes | Isolated Telegram delivery |
| Bot | GO_BACKEND_URL, MINI_APP_URL, ALLOWED_ORIGINS | staging Go URL and website HTTPS origin | For integration | Internal API routing and trusted frontend |
| Bot | GO_ACCOUNTS_ENABLED, TELEGRAM_ORDERS_ENABLED | true | For integration | Verified accounts and orders |
| Bot | DATABASE_PATH, API_HOST | /app/data/bakeu.sqlite, 0.0.0.0 | Yes | Persistent staging volume and Railway listener |
| Website | VITE_GO_API_URL, VITE_GO_PHONE_ACCOUNTS | /api behind a same-origin proxy, true | For integration | Shared Go API and verified account flow |
| Website | VITE_ACCOUNT_API_URL | https://<staging-bot>/api | For Telegram paths | Bot HTTP API |

In Railway UI open captivating-reverence → Variables in stading. Set MONGODB_URI to a protected reference if the mongo service exposes the correct replica-set URI. Otherwise set it as a protected value. Do not use the untouched old database or localhost. Percent-encode credentials as URI components and use the actual authenticating database in authSource. Do not paste the URI into a terminal transcript.

The backend Root Directory is / in BakeU-Backend with its Dockerfile and /readyz health check. Bot Root Directory is src/bot in BakeU.UZ, Dockerfile and /health. The frontend uses npm ci and npm run build at repo root, with its existing static host. Railway injects PORT. For cookie auth use same-site HTTPS domains or a same-origin proxy and point the frontend at the final URL.

Only after approval start the staging deployment of Go first, then Website and Bot. In PowerShell set the public staging Go URL and run:

```powershell
$Backend = "https://<staging-go-domain>"
Invoke-RestMethod "$Backend/readyz"
Invoke-RestMethod "$Backend/api/markets"
```

An empty URI or wrong scheme stops startup before any connection. For a connection failure check private DNS, the member name reported by rs0, primary status, authSource, permissions and TLS settings in staging. For index failures inspect duplicate counts on a restored copy. Keep detailed credential-bearing diagnostics out of deployment logs. After readiness, run the verified registration and checkout scenario with the test bot. Keep earlier build SHAs and configuration for rollback, and reconcile any newer writes before restoring a database snapshot.
