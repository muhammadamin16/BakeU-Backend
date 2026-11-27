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
  await db.collection('users').updateOne({ _id: signedIn.user.uid }, { $set: { freezeUntil: new Date(Date.now() + 60000) } });
  assert.equal((await me(token)).status, 403);
  const rotatedCookie = restored.headers.get('set-cookie').split(';')[0];
  assert.equal((await post('/auth/refresh', {}, { ...browser, cookie: rotatedCookie })).status, 403);
});
