#!/usr/bin/env bash
# Run each foreground service in its own terminal.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
backend=$PWD
frontend=${BAKEU_FRONTEND_DIR:-"$backend/../BakeU.UZ"}
demo_dir=${BAKEU_LOCAL_STATE_DIR:-"$backend/../.bakeu-local-demo"}
mongo_port=${BAKEU_LOCAL_MONGO_PORT:-27017}
api_port=${BAKEU_LOCAL_API_PORT:-8080}
web_port=${BAKEU_LOCAL_WEB_PORT:-5173}
container=bakeu-dev-mongo
if [[ "$mongo_port" != 27017 ]]; then
  container="$container-$mongo_port"
fi
image=docker.io/library/mongo@sha256:479e85ced54ffb8a40dade1dcde2da2a9ae858515b9415e9cc54bfb8f0575d01

read_secret() {
  local file="$demo_dir/$1"
  mkdir -p "$demo_dir"
  if [[ ! -s "$file" ]]; then
    (umask 077; set -o noclobber; openssl rand -hex 32 > "$file")
  fi
  cat "$file"
}

prepare() {
  if docker container inspect "$container" >/dev/null 2>&1; then
    local expected="127.0.0.1:$mongo_port"
    [[ "$(docker port "$container" 27017)" == "$expected" ||
       "$(docker inspect --format '{{(index (index .HostConfig.PortBindings "27017/tcp") 0).HostIp}}:{{(index (index .HostConfig.PortBindings "27017/tcp") 0).HostPort}}' "$container")" == "$expected" ]] ||
      { echo "Existing $container does not publish MongoDB at $expected" >&2; return 1; }
    docker start "$container" >/dev/null
  else
    docker run --detach --name "$container" -p "127.0.0.1:$mongo_port:27017" "$image" \
      --replSet bakeu-dev --bind_ip_all >/dev/null
  fi
  for attempt in {1..30}; do
    if docker exec "$container" mongosh --quiet --eval 'quit(db.adminCommand({ping:1}).ok === 1 ? 0 : 1)' >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  docker exec "$container" mongosh --quiet --eval '
    try { rs.status(); }
    catch (error) {
      if (error.codeName !== "NotYetInitialized") throw error;
      rs.initiate({_id:"bakeu-dev",members:[{_id:0,host:"127.0.0.1:27017"}]});
    }' >/dev/null
  for attempt in {1..30}; do
    if docker exec "$container" mongosh --quiet --eval 'quit(db.hello().isWritablePrimary ? 0 : 1)'; then
      break
    fi
    sleep 1
  done
  docker exec "$container" mongosh --quiet --eval 'quit(db.hello().isWritablePrimary ? 0 : 1)'
  export BAKEU_DEMO_PASSWORD="$(read_secret demo-password)"
  go run -mod=readonly ./cmd/seed-local -mongo-port "$mongo_port" -images "$frontend/public/images/products"
  (cd "$frontend"; npm ci --no-audit --no-fund)
}

case "${1:-}" in
  prepare) prepare ;;
  api)
    export PORT="$api_port" BAKEU_LISTEN_HOST=127.0.0.1
    export MONGODB_DB=bakeu_local_demo
    export MONGODB_URI="mongodb://127.0.0.1:$mongo_port/?replicaSet=bakeu-dev&directConnection=true"
    export JWT_SECRET="$(read_secret jwt-secret)" TELEGRAM_BRIDGE_TOKEN="$(read_secret telegram-bridge-secret)"
    export ADMIN_USER_IDS=demo-buyer-01 AUTH_PHONE_ACCOUNTS=false AUTH_REFRESH_SESSIONS=false
    export RESEND_API_KEY= ADMIN_EMAILS= AUTH_BROWSER_ORIGINS= SELLER_SELF_SERVICE=false
    export TELEGRAM_SITE_URL="http://127.0.0.1:$web_port"
    exec go run -mod=readonly ./cmd/server
    ;;
  web)
    cd "$frontend"
    export VITE_GO_API_URL=/api VITE_GO_LOCAL_ACCOUNTS=true VITE_GO_PHONE_ACCOUNTS=false
    export VITE_BOT_API_PROXY_TARGET="http://127.0.0.1:$api_port"
    exec npm run dev -- --host 127.0.0.1 --port "$web_port" --strictPort
    ;;
  password) read_secret demo-password ;;
  bot)
    : "${BOT_TOKEN:?Supply a local test bot token through the environment}"
    cd "$frontend"
    : "${ADMIN_IDS:?Supply your numeric Telegram user ID through ADMIN_IDS}"
    export BOT_TOKEN ADMIN_IDS
    export TELEGRAM_ORDERS_ENABLED=true GO_ACCOUNTS_ENABLED=false API_HOST=127.0.0.1 API_PORT=3001
    export GO_BACKEND_URL="http://127.0.0.1:$api_port"
    export TELEGRAM_BRIDGE_TOKEN="$(read_secret telegram-bridge-secret)"
    export DATABASE_PATH="$demo_dir/telegram.sqlite"
    export SITE_URL="http://127.0.0.1:$web_port" USE_WEB_APP=false
    exec npm run bot:website
    ;;
  *) echo "Usage: $0 {prepare|api|web|password|bot}" >&2; exit 2 ;;
esac
