# Bakeu — Bakery Marketplace API

A Go backend for **Bakeu**, a marketplace for bakery goods (bread, cookies, etc.).
Bakers (sellers) list products with photos; customers browse, add to cart, order, and
review.

## Stack

- **Go** + [gin](https://github.com/gin-gonic/gin) — HTTP API
- **MongoDB** — database (Atlas free M0 tier, or local)
- **Custom JWT auth** — email/password signup/login, bcrypt-hashed passwords
- **GridFS** — product photos stored in MongoDB
- **Cloud Run** — deployment

## Project layout

```
cmd/server/main.go          entrypoint
internal/
  config/                   env config
  mongoclient/              MongoDB connection + index setup
  auth/                     JWT sign/verify
  middleware/               CORS, auth (JWT verification), role checks
  models/                   data structs (bson-tagged)
  repository/               MongoDB data access
  storage/                  GridFS image store
  handlers/                 gin route handlers
```

## Setup

1. **Install Go** (1.26+): `brew install go`
2. **MongoDB**: either a free [Atlas M0 cluster](https://www.mongodb.com/atlas) or local
   (`brew install mongodb-community` / `docker run -p 27017:27017 mongo`).
   - For Atlas: create a DB user, and under **Network Access** allow your IP (or
     `0.0.0.0/0` for Cloud Run, since M0 has no VPC peering).
3. **Configure env**: `cp .env.example .env` and fill in `MONGODB_URI`, `MONGODB_DB`,
   and a long random `JWT_SECRET`.

## Run locally

```bash
export $(grep -v '^#' .env | xargs)   # load .env
go run ./cmd/server
curl http://localhost:8080/healthz
```

## Deploy to Cloud Run

```bash
gcloud run deploy bakeu \
  --source . \
  --region <region> \
  --allow-unauthenticated \
  --set-env-vars MONGODB_DB=bakeu \
  --set-secrets MONGODB_URI=bakeu-mongo-uri:latest,JWT_SECRET=bakeu-jwt-secret:latest
```

(Store `MONGODB_URI` and `JWT_SECRET` in Secret Manager rather than plain env vars.)

## Auth

Sign up or log in to receive a JWT, then send it as `Authorization: Bearer <token>` on
authenticated routes.

```bash
# signup (role is "seller" or "customer")
curl -X POST localhost:8080/api/auth/signup -H 'Content-Type: application/json' \
  -d '{"email":"a@b.com","password":"secret123","role":"seller","name":"Ann"}'
# login
curl -X POST localhost:8080/api/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"a@b.com","password":"secret123"}'
```

## API (v1)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/healthz` | — | health check |
| POST | `/api/auth/signup` | — | create account, returns `{token, user}` |
| POST | `/api/auth/login` | — | log in, returns `{token, user}` |
| GET | `/api/me` | yes | current user profile |
| POST | `/api/profile` | yes | update profile (name, phone, address) |
| GET | `/api/products` | — | browse/search products |
| GET | `/api/products/:id` | — | product detail |
| POST | `/api/products` | seller | create product |
| PUT | `/api/products/:id` | seller | update product |
| DELETE | `/api/products/:id` | seller | delete product |
| POST | `/api/products/:id/image` | seller | upload product image (multipart `image`) |
| GET | `/api/images/:id` | — | fetch a stored product image |
| GET | `/api/cart` | yes | view cart |
| POST | `/api/cart/items` | yes | add/update cart item |
| DELETE | `/api/cart/items/:productId` | yes | remove cart item |
| POST | `/api/orders` | customer | checkout cart into an order |
| GET | `/api/orders` | yes | list orders (customer's own / seller's incoming) |
| PATCH | `/api/orders/:id/status` | seller | update order status |
| GET | `/api/products/:id/reviews` | — | list reviews |
| POST | `/api/products/:id/reviews` | customer | add a review |
