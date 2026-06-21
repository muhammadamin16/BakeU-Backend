# Bakeu — The Seller Role

The **seller** is a baker who runs a *bakery storefront* (a `Market`) on Bakeu,
lists `Product`s, and fulfills incoming `Order`s. This document describes the
full seller lifecycle and every endpoint a seller touches, exactly as the
backend implements it today.

---

## 1. The two-layer identity

A seller is made of **two separate records**:

| Record | Collection | Created when | Holds |
|---|---|---|---|
| `User` (role `seller`) | `users` | at signup | login identity, name, phone, address |
| `Market` | `markets` | when they apply | the public bakery storefront + approval status |

> Having a `seller` user account does **not** mean you can sell. You can only
> list products and receive orders once your `Market` is **approved** by an
> admin. One seller = one market (`sellerId` is unique on the markets
> collection).

### Market status pipeline

```
            apply / re-apply
   (none) ─────────────────────▶ pending ──approve──▶ approved
                                   │  ▲                  (terminal for selling)
                                   │  │ re-apply
                                   └──┴──decline──▶ rejected ──┐
                                                               │ re-apply
                                                               └──▶ pending
```

- `pending` — submitted, waiting on an admin.
- `approved` — live; products become publicly visible and product/order
  management unlocks. **An approved market cannot be re-submitted** (409).
- `rejected` — declined with a `rejectionReason`; seller can edit details and
  re-submit, which resets it to `pending`.

---

## 2. End-to-end seller flow

```
1. SIGNUP            POST /api/auth/signup  {role:"seller"}      → token
2. APPLY FOR MARKET  POST /api/market       (multipart + logo)   → status: pending
        │
        ├─▶ admin gets an email with a secure review link
        │   admin opens it → Approve / Decline on an HTML page
        │   seller gets an email with the decision
        ▼
3. APPROVED          (market.status == "approved")
4. ADD PRODUCTS      POST /api/products                          → product
   ADD PHOTOS        POST /api/products/:id/image  (multipart)
   EDIT / REMOVE     PUT|DELETE /api/products/:id
5. RECEIVE ORDERS    GET /api/orders                             → {incoming:[...]}
6. FULFILL           PATCH /api/orders/:id/status                → status moves
```

---

## 3. Step-by-step detail

### Step 1 — Become a seller account

`POST /api/auth/signup` (public)

```json
{ "email":"ann@bake.com", "password":"secret123", "role":"seller", "name":"Ann" }
```

- `role` must be `"seller"` or `"customer"` (validated by `Role.Valid()`).
- Returns `{ token, user }`. Send the token as `Authorization: Bearer <token>`
  on every authenticated call afterward.
- `role` is fixed at signup — there is no role-switch endpoint.

How role enforcement works: seller routes are wrapped by
`AuthRequired` → `RequireRole(userRepo, RoleSeller)`. The middleware loads the
caller's profile, 403s if the role isn't `seller`, and caches the user on the
request context.

### Step 2 — Apply to open a bakery (Market)

`POST /api/market` (seller only) — **multipart/form-data**

| Field | Required | Notes |
|---|---|---|
| `name` | ✅ | bakery name |
| `description` | ✅ | |
| `phone` | ✅ | |
| `email` | ✅ | where decision emails are sent |
| `city` | ✅ | |
| `area` | — | neighborhood/district |
| `fulfillment` | ✅ | `pickup` \| `delivery` \| `both` |
| `logo` | ✅ | image file, stored in GridFS → served at `/api/images/:id` |

Behavior:
- No existing market → **creates** one with `status: pending`.
- Existing market is `pending` or `rejected` → **resubmits** (overwrites fields,
  clears `rejectionReason`, resets to `pending`).
- Existing market is `approved` → **409 Conflict** (`"your market is already approved"`).
- On submit, all configured admin emails get a notification with a secure review link.

`GET /api/market/mine` (seller only) — returns the caller's market in any
status, or `404 {"error":"no market yet"}` if they haven't applied. This is how
the app knows whether to show "Apply", "Pending review", "Rejected — reason…",
or the full seller dashboard.

### Step 3 — Admin review (email-driven, no app login)

This is **not** a seller action, but it gates everything the seller can do.

- The admin email contains a link: `/api/market/review/:id?token=<hmac>`.
- The token is an HMAC-SHA256 of the market ID keyed by `MarketReviewSecret` —
  unguessable, so the page needs no app auth.
- `GET` renders an HTML page with Approve / Decline buttons (GET only displays,
  so email link-prefetchers can't accidentally mutate).
- `POST` applies the decision:
  - **Approve** → `status: approved`, products go live.
  - **Decline** → `status: rejected` + the typed `reason`.
- Either way the seller is emailed the outcome (approval 🎉 or the reason).
- Deciding on a non-pending market returns 409 (idempotent guard).

### Step 4 — Manage products (requires approved market)

Every write below first calls `requireApproved()`, which checks
`IsApprovedSeller`. If the market isn't approved →
`403 {"error":"your bakery must be approved first"}`.

| Method | Path | Guard | Purpose |
|---|---|---|---|
| `POST` | `/api/products` | seller + approved | create a listing |
| `PUT` | `/api/products/:id` | seller + approved + **owner** | edit |
| `DELETE` | `/api/products/:id` | seller + **owner** | remove (no approval check) |
| `POST` | `/api/products/:id/image` | seller + approved + **owner** | upload a photo |

Create/Update JSON body:
```json
{ "title":"Sourdough", "description":"...", "price":4.5, "category":"bread", "available":true }
```
- Validation: `title` required, `price > 0`.
- `sellerId` is taken from the token, never the body.
- New products start with empty `imageURLs`, `ratingAvg:0`, `ratingCount:0`.

Image upload: `multipart/form-data` with an `image` file. Stored in GridFS;
its URL (`/api/images/<id>`) is appended to the product's `imageURLs`.

**Ownership** is enforced by `loadOwned()`: the product's `sellerId` must equal
the caller's UID, else `403 "you do not own this product"`.

**Visibility rule:** the public `GET /api/products` only returns products whose
seller is in `ApprovedSellerIDs()`. So if a market is rejected/pending (or later
un-approved), that seller's products vanish from public browse even though the
documents still exist.

### Step 5 — Receive & read orders

Orders are created by **customers** at checkout (`POST /api/orders`). A cart
spanning multiple bakeries is split into **one `Order` per seller**, and each
order item is a **price/title snapshot** so later edits don't rewrite history.

`GET /api/orders` (any authenticated user) returns both sides:
```json
{
  "placed":   [ ...orders where you are the customer ],
  "incoming": [ ...orders where you are the seller ]
}
```
A seller reads the `incoming` array for orders to fulfill.

### Step 6 — Fulfill orders (update status)

`PATCH /api/orders/:id/status` (seller only, **owner only**)

```json
{ "status": "preparing" }
```

Valid statuses (`ValidOrderStatus`):

```
pending → preparing → out_for_delivery → delivered
                    ↘ cancelled
```

- Any value from the set is accepted (the code validates membership, **not**
  transition order — moving backward or skipping steps is allowed by the API).
- The order's `sellerId` must match the caller, else `403 "you do not own this order"`.
- New orders begin at `pending`.

---

## 4. Complete seller endpoint reference

| Method | Path | Auth | Extra guard |
|---|---|---|---|
| POST | `/api/auth/signup` | public | role must be `seller` |
| POST | `/api/auth/login` | public | — |
| GET | `/api/me` | authed | — |
| POST | `/api/profile` | authed | update name/phone/address |
| POST | `/api/market` | seller | apply / resubmit (multipart + logo) |
| GET | `/api/market/mine` | seller | — |
| POST | `/api/products` | seller | market approved |
| PUT | `/api/products/:id` | seller | approved + owner |
| DELETE | `/api/products/:id` | seller | owner |
| POST | `/api/products/:id/image` | seller | approved + owner (multipart `image`) |
| GET | `/api/orders` | authed | reads `incoming` |
| PATCH | `/api/orders/:id/status` | seller | owner |

Admin-side (HMAC token, not seller auth): `GET|POST /api/market/review/:id`.

---

## 5. Data shapes (seller-relevant fields)

**Market**
```
id, sellerId(unique), name, description, phone, email, city, area,
fulfillment(pickup|delivery|both), logoURL, status(pending|approved|rejected),
rejectionReason, createdAt, updatedAt, reviewedAt
```

**Product**
```
id, sellerId, title, description, price, category, imageURLs[],
available, ratingAvg, ratingCount, createdAt, updatedAt
```

**Order** (one per seller)
```
id, customerId, sellerId, items[]{productId,title,price,qty},
total, status, deliveryAddress, createdAt
```

---

## 6. Notes / current gaps worth knowing

- **No order-status transition rules** — the API lets a seller set any valid
  status in any order (e.g. `delivered` → `pending`). Tighten if needed.
- **Approval can be revoked implicitly** — if an approved market were ever set
  back to non-approved, that seller's products disappear from public browse and
  product writes lock again, but existing orders are untouched.
- **No "out of stock" auto-handling** — checkout rejects items where
  `available == false`, but sellers must flip `available` manually.
- **Fulfillment is informational** — `pickup`/`delivery`/`both` is stored on the
  market but not enforced against the order's `deliveryAddress`.
