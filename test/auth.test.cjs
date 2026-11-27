const assert = require('node:assert/strict');
const { createHmac, createHash, randomBytes, randomUUID } = require('node:crypto');
const { test } = require('node:test');
const { MongoClient } = require('mongodb');

test('phone verification and durable sessions on a MongoDB replica set', async t => {
  const uri = process.env.TEST_REPLICA_URI;
  assert.match(uri ?? '', /^mongodb:\/\/127\.0\.0\.1:\d+\/\?directConnection=true$/);
  process.env.MONGODB_URI = uri;
  process.env.MONGODB_DB = 'bakeu_auth_' + randomUUID().replaceAll('-', '');
  process.env.JWT_SECRET = randomBytes(32).toString('hex');
  process.env.TELEGRAM_BRIDGE_TOKEN = randomBytes(32).toString('hex');
  process.env.TELEGRAM_BOT_USERNAME = 'BakeuTestBot';
  process.env.AUTH_BROWSER_ORIGINS = 'https://bakeu.test';
  const client = new MongoClient(uri);
  t.after(() => client.close());
  const db = client.db(process.env.MONGODB_DB);
  assert.equal((await db.command({ hello: 1 })).isWritablePrimary, true);
  assert.ok((await db.command({ hello: 1 })).setName);
  const { NestFactory } = await import('@nestjs/core');
  const { AppModule } = await import('../dist/app.js');
  let app, base;
  async function boot() {
    app = await NestFactory.create(AppModule, { logger: false, abortOnError: false });
    app.setGlobalPrefix('api');
    await app.listen(0, '127.0.0.1');
    base = await app.getUrl();
  }
  t.after(() => app?.close());
  await boot();
  const browser = { 'x-bakeu-session': 'browser', origin: 'https://bakeu.test' };
  const post = (path, body, headers = browser) => fetch(base + '/api' + path, {
    method: 'POST', body: JSON.stringify(body), headers: { 'content-type': 'application/json', ...headers },
  });
  const me = token => fetch(base + '/api/me', { headers: { authorization: 'Bearer ' + token } });
  const contactPath = '/integrations/telegram/auth/phone/contact';
  const bridge = actor => ({ authorization: 'Bearer ' + process.env.TELEGRAM_BRIDGE_TOKEN, 'x-telegram-user-id': actor });
  const seller = { purpose: 'register', role: 'seller', phone: '+998901234567', firstName: 'Test', lastName: 'Seller',
    marketName: 'Bakery', address: 'Local address', lat: 0, lng: 0 };
  async function challenge(draft) {
    const response = await post('/auth/phone/start', draft);
    assert.equal(response.status, 201);
    return (await response.json()).id;
  }
  async function code(id, phone, actor) {
    const response = await post(contactPath, { id, phone }, bridge(actor));
    assert.equal(response.status, 200);
    return (await response.json()).code;
  }
  const expiredProof = await challenge({ ...seller, phone: '+998901234570' });
  const expiredCode = await code(expiredProof, '+998901234570', '99');
  await db.collection('phone_proofs').updateOne({ _id: expiredProof }, { $set: { expiresAt: new Date(Date.now() - 1000) } });
  assert.equal((await post('/auth/phone/verify', { id: expiredProof, code: expiredCode })).status, 401);
  const first = await challenge(seller);
  assert.equal(await db.collection('users').countDocuments(), 0);
  assert.equal(await db.collection('markets').countDocuments(), 0);
  assert.equal((await post(contactPath, { id: first, phone: seller.phone })).status, 401);
  assert.equal((await post(contactPath, { id: first, phone: '+998902222222' }, bridge('100'))).status, 401);
  const second = await challenge(seller);
  const codes = await Promise.all([code(first, seller.phone, '100'), code(second, seller.phone, '100')]);
  const responses = await Promise.all([first, second].map((id, i) => post('/auth/phone/verify', { id, code: codes[i] })));
  assert.deepEqual(responses.map(r => r.status).sort(), [200, 409]);
  const response = responses.find(r => r.ok);
  const signedIn = await response.json();
  const cookieHeader = response.headers.get('set-cookie');
  assert.match(cookieHeader, /Max-Age=\d+; Path=\/; HttpOnly; Secure; SameSite=Strict/);
  const cookie = cookieHeader.split(';')[0];
  assert.equal(signedIn.refreshToken, undefined);
  assert.equal(await db.collection('users').countDocuments(), 1);
  const shop = await db.collection('markets').findOne();
  assert.deepEqual([shop.status, shop.sellerId, shop.latitude, shop.longitude], ['approved', signedIn.user.uid, 0, 0]);
  const winner = responses.indexOf(response);
  assert.equal((await post('/auth/phone/verify', { id: [first, second][winner], code: codes[winner] })).status, 401);
  const buyer = { ...seller, role: 'customer', phone: '+998901234568' };
  const buyerProof = await challenge(buyer);
  const buyerCode = await code(buyerProof, buyer.phone, '200');
  const buyerResponse = await post('/auth/phone/verify', { id: buyerProof, code: buyerCode });
  assert.equal(buyerResponse.status, 200);
  assert.equal((await buyerResponse.json()).user.role, 'customer');
  assert.equal(await db.collection('markets').countDocuments(), 1);

  const launched = await (await post('/integrations/telegram/auth/launch', {}, bridge('100'))).json();
  assert.equal(launched.uid, signedIn.user.uid);
  assert.equal((await post('/auth/phone/telegram', { ticket: launched.ticket })).status, 200);
  assert.equal((await post('/auth/phone/telegram', { ticket: launched.ticket })).status, 401);

  const legacyUID = 'legacy-string-uid';
  const familyId = randomBytes(12).toString('hex');
  const old = familyId + '.' + randomBytes(32).toString('hex');
  await db.collection('users').insertOne({ _id: legacyUID, role: 'customer', phone: '+998901112233', unknown: 'retain' });
  await db.collection('auth_sessions').insertOne({ _id: familyId, userId: legacyUID,
    current: createHash('sha256').update(old).digest('hex'), expiresAt: new Date(Date.now() + 3600000), rotations: 0 });
  const encode = v => Buffer.from(JSON.stringify(v)).toString('base64url');
  const payload = encode({ alg: 'HS256' }) + '.' + encode({ sub: legacyUID, sid: familyId, exp: Math.floor(Date.now() / 1000) + 300 });
  const legacyJWT = payload + '.' + createHmac('sha256', process.env.JWT_SECRET).update(payload).digest('base64url');

  await app.close();
  await boot();
  assert.equal((await me(legacyJWT)).status, 200);
  const restoredLegacy = await post('/auth/refresh', { refreshToken: old }, { 'x-bakeu-session': 'refresh' });
  assert.equal((await restoredLegacy.json()).user.uid, legacyUID);
  assert.equal((await db.collection('users').findOne({ _id: legacyUID })).unknown, 'retain');
  assert.equal((await post('/auth/refresh', {}, { ...browser, origin: 'https://wrong.test', cookie })).status, 403);
  const restored = await post('/auth/refresh', {}, { ...browser, cookie });
  assert.equal(restored.status, 200);
  const { token } = await restored.json();
  const sharp = require('sharp');
  const signed = { authorization: 'Bearer ' + token };
  const create = await post('/products', { title: 'Cake', price: 45.25, available: true, sellerId: 'forged' }, signed);
  assert.equal(create.status, 201);
  const item = await create.json();
  assert.equal(item.sellerId, signedIn.user.uid);
  assert.equal(item.draft, true);
  await db.collection('products').updateOne({ _id: item.id }, { $set: { legacyField: 'retained' } });
  const path = '/api/products/' + item.id;
  const write = (body, method = 'PUT') => fetch(base + path, {
    method, headers: { ...signed, 'content-type': 'application/json' }, ...(body && { body: JSON.stringify(body) }),
  });
  assert.equal((await write({ publish: true })).status, 400);
  const invalid = new FormData();
  invalid.append('image', new Blob(['not an image'], { type: 'image/png' }), 'invalid.png');
  assert.equal((await fetch(base + path + '/image', { method: 'POST', headers: signed, body: invalid })).status, 400);
  const form = new FormData();
  for (const background of ['red', 'blue', 'green']) {
    const photo = await sharp({ create: { width: 32, height: 32, channels: 3, background } }).png().toBuffer();
    form.append('image', new Blob([photo], { type: 'image/png' }), background + '.png');
  }
  const uploaded = await fetch(base + path + '/image', { method: 'POST', headers: signed, body: form });
  assert.equal(uploaded.status, 201);
  const images = (await uploaded.json()).imageURLs;
  assert.equal(images.length, 3);
  assert.equal((await fetch(base + images[0])).status, 404);
  assert.equal((await fetch(base + '/api/me/products/' + item.id + '/images/' + images[0].split('/').at(-1), { headers: signed })).status, 200);
  const published = await write({ publish: true, imageURLs: [...images].reverse() });
  assert.equal(published.status, 200);
  assert.deepEqual((await (await fetch(base + path)).json()).imageURLs, [...images].reverse());
  assert.equal((await fetch(base + images[0])).status, 200);
  assert.equal((await write(undefined, 'DELETE')).status, 204);
  assert.equal((await fetch(base + path)).status, 404);
  assert.equal((await write({ title: 'Try to resurrect' })).status, 404);
  assert.equal((await fetch(base + images[0])).status, 200);
  assert.equal((await db.collection('products').findOne({ _id: item.id })).legacyField, 'retained');

  const secondSeller = 'seller-fixture';
  const secondShop = 'shop-fixture';
  await db.collection('users').insertOne({ _id: secondSeller, role: 'seller', status: 'active' });
  await db.collection('markets').insertOne({ _id: secondShop, sellerId: secondSeller, name: 'Second shop', status: 'approved' });
  await db.collection('products').insertMany([
    { _id: 'from-first', sellerId: signedIn.user.uid, title: 'Bread', price: 43.20, available: true },
    { _id: 'from-second', sellerId: secondSeller, title: 'Cake', price: 9.45, available: true },
  ]);
  const orderLines = [{ productId: 'from-first', qty: 2 }, { productId: 'from-second', qty: 3 }];
  const buyerTicket = await (await post('/integrations/telegram/auth/launch', {}, bridge('200'))).json();
  const buyerSession = await (await post('/auth/phone/telegram', { ticket: buyerTicket.ticket })).json();
  const buying = { authorization: 'Bearer ' + buyerSession.token };
  const read = (path, headers = buying) => fetch(base + '/api' + path, { headers });
  assert.equal((await fetch(base + '/api/favorites/from-first', { method: 'PUT', headers: buying })).status, 200);
  assert.deepEqual((await (await read('/favorites')).json()).productIds, ['from-first']);
  const sellerCart = await (await read('/cart', signed)).json();
  assert.equal((await post('/cart/items', { ...orderLines[0], cartRevision: sellerCart.revision }, signed)).status, 400);
  assert.equal((await post('/cart/items', { ...orderLines[1], cartRevision: sellerCart.revision }, signed)).status, 200);
  const initial = await (await read('/cart')).json();
  const saved = await post('/cart/items', { ...orderLines[0], cartRevision: initial.revision }, buying);
  assert.equal(saved.status, 200);
  assert.equal((await post('/cart/items', { ...orderLines[1], cartRevision: initial.revision }, buying)).status, 409);
  const cart = await saved.json();
  const fulfillment = { scheduledAt: new Date(Date.now() + 86400000).toISOString(), fulfillment: 'pickup' };
  const headers = { ...buying, 'idempotency-key': 'checkout-test-key' };
  const orderBody = { cartRevision: cart.revision, total: 1, items: orderLines, deliveries: {
    [shop._id]: fulfillment, [secondShop]: { ...fulfillment, fulfillment: 'delivery', deliveryAddress: 'Test address', comment: 'Call first' },
  } };
  const placed = await post('/orders', orderBody, headers);
  assert.equal(placed.status, 201);
  const orderResult = await placed.json();
  assert.deepEqual(orderResult.orders.map(order => order.total), [86.4, 28.35]);
  assert.deepEqual(await (await post('/orders', orderBody, headers)).json(), orderResult);
  assert.equal(await db.collection('orders').countDocuments(), 2);
  assert.equal(await db.collection('telegram_order_outbox').countDocuments(), 2);
  assert.equal((await (await read('/cart')).json()).items.length, 0);
  const [order] = orderResult.orders;
  const statusPath = base + '/api/orders/' + order.id + '/status';
  const transition = await fetch(statusPath, { method: 'PATCH', headers: { ...signed,
    'content-type': 'application/json', 'idempotency-key': 'order-test-status' },
    body: JSON.stringify({ status: 'accepted', expectedStatus: 'pending', expectedVersion: 1 }) });
  assert.equal(transition.status, 200);
  const history = await (await read('/orders')).json();
  assert.equal(history.placed.length, 2);
  assert.equal(history.incoming.length, 0);
  assert.deepEqual(history.placed.find(value => value.id === order.id).history.map(x => x.status), ['pending', 'accepted']);
  assert.equal(order.customerId, buyerSession.user.uid);
  const claimed = await post('/integrations/telegram/notifications/claim', { limit: 10 }, bridge('100'));
  const { jobs } = await claimed.json();
  assert.equal(jobs.length, 2);
  const job = jobs.find(item => item.kind === 'order.status_changed');
  const receiptPath = '/integrations/telegram/notifications/' + job.id;
  assert.deepEqual(await (await post(receiptPath + '/begin', { leaseToken: job.leaseToken }, bridge('100'))).json(), { started: true });
  assert.deepEqual(await (await post(receiptPath + '/ack', { leaseToken: job.leaseToken, messageId: 123 }, bridge('100'))).json(), { ok: true });
  assert.equal((await (await read('/me/notifications')).json()).items[0].telegramState, 'sent');

  const dialog = await post('/chats', { shopId: shop._id }, buying);
  assert.equal(dialog.status, 201);
  const chat = await dialog.json();
  assert.equal((await post('/chats/' + chat.id + '/messages', { text: 'Order question' }, buying)).status, 201);
  const message = (await (await read('/chats/' + chat.id + '/messages', signed)).json()).items[0];
  assert.equal(message.text, 'Order question');
  assert.equal((await (await read('/chats', signed)).json()).items[0].unread, 1);
  assert.equal((await fetch(base + '/api/chats/' + chat.id + '/read', {
    method: 'PUT', headers: { ...signed, 'content-type': 'application/json' }, body: JSON.stringify({ through: message.sequence }),
  })).status, 204);
  assert.equal((await (await read('/chats', signed)).json()).items[0].unread, 0);
  assert.equal((await read('/chats/' + chat.id + '/messages', { authorization: 'Bearer ' + legacyJWT })).status, 404);
  const follow = method => fetch(base + '/api/markets/' + shop._id + '/follow', { method, headers: buying });
  assert.deepEqual(await (await follow('PUT')).json(), { followers: 1, following: true });
  assert.deepEqual(await (await follow('PUT')).json(), { followers: 1, following: true });
  assert.deepEqual(await (await follow('DELETE')).json(), { followers: 0, following: false });
  const review = () => post('/products/from-first/reviews', { rating: 5, comment: 'Fresh bread' }, buying);
  assert.equal((await review()).status, 403);
  await db.collection('orders').updateOne({ _id: order.id }, { $set: { status: 'completed' } });
  assert.equal((await review()).status, 201);
  assert.equal((await review()).status, 400);
  const salesDate = new Date(Date.now() + 5 * 3600000).toISOString().slice(0, 10);
  const sellerReport = await (await read('/seller/dashboard?period=day', signed)).json();
  assert.equal(sellerReport.revenue.total, 86.4);
  assert.equal((await read('/admin/overview')).status, 403);
  process.env.ADMIN_USER_IDS = buyerSession.user.uid;
  const overview = await (await read('/admin/overview')).json();
  assert.equal(overview.customers, 2);
  assert.equal(overview.sellers, 2);
  assert.equal(overview.orders, 2);
  const adminReport = await (await read('/admin/sales?period=day&date=' + salesDate)).json();
  assert.equal(adminReport.totals.revenue, sellerReport.revenue.total);
  await db.collection('users').updateOne({ _id: signedIn.user.uid }, { $set: { passwordHash: 'sentinel-hash' } });
  const adminUser = await (await read('/admin/users/' + signedIn.user.uid)).json();
  assert.equal(adminUser.passwordHash, undefined);
  const freeze = await fetch(base + '/api/admin/users/' + signedIn.user.uid + '/status', {
    method: 'PATCH', headers: { ...buying, 'content-type': 'application/json' },
    body: JSON.stringify({ action: 'freeze', freezeUntil: new Date(Date.now() + 60000).toISOString(), secret: 'ignore' }),
  });
  assert.equal(freeze.status, 200);
  const audit = await (await read('/admin/records/audit')).json();
  assert.equal(audit.total, 1);
  assert.equal(audit.items[0].kind, 'user.freeze');
  assert.equal(audit.items[0].data.secret, undefined);
  assert.equal((await me(token)).status, 403);
  const rotatedCookie = restored.headers.get('set-cookie').split(';')[0];
  assert.equal((await post('/auth/refresh', {}, { ...browser, cookie: rotatedCookie })).status, 403);
});
