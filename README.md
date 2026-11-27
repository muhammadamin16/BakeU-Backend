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
TEST_MONGODB_URI=mongodb://127.0.0.1:27017 npm test
```

The suite writes fixtures to a randomly named database on the disposable instance. Stop the disposable instance when finished. To connect the existing website, build it with `VITE_GO_API_URL=/api` and `VITE_GO_PHONE_ACCOUNTS=false`. Start its gateway in legacy mode with `WEB_GO_API_URL=http://127.0.0.1:3400/api` and a local `WEB_BOT_API_URL` such as `http://127.0.0.1:3401/api`, to avoid routing to production. Server account and write flows still require implementation.
