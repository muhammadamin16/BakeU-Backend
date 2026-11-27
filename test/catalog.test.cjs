const assert = require('node:assert/strict');
const { randomUUID } = require('node:crypto');
const { once } = require('node:events');
const { test } = require('node:test');
const { GridFSBucket, MongoClient } = require('mongodb');

test('catalog HTTP contracts against isolated MongoDB', async (t) => {
  const uri = process.env.TEST_MONGODB_URI;
  assert.match(uri ?? '', /^mongodb:\/\/127\.0\.0\.1:\d+\/?$/, 'Use a disposable local MongoDB instance');
  process.env.MONGODB_URI = uri;
  process.env.MONGODB_DB = 'bakeu_contract_' + randomUUID().replaceAll('-', '');
  const client = new MongoClient(uri, { serverSelectionTimeoutMS: 5000 });
  const db = client.db(process.env.MONGODB_DB);
  t.after(() => client.close());

  const bucket = new GridFSBucket(db);
  async function image(visibility) {
    const stream = bucket.openUploadStream('test.png', { metadata: { contentType: 'image/png', visibility } });
    const uploaded = once(stream, 'finish');
    stream.end(Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jR1cAAAAASUVORK5CYII=', 'base64'));
    await uploaded;
    return '/api/images/' + stream.id.toHexString();
  }
  const photos = [await image('public'), await image('public')];
  const privatePhoto = await image('private');
  await db.collection('markets').insertMany([
    { _id: 'old-shop', sellerId: 'old-user', name: 'Bakery', status: 'approved', latitude: 0, longitude: 0, phone: 'private' },
    { _id: 'suspended-shop', sellerId: 'suspended-user', status: 'suspended' },
  ]);
  await db.collection('products').insertMany([
    { _id: 'first', title: 'Cake (large)', sellerId: 'old-user', available: true, category: 'cake', price: 500, imageURLs: photos,
      variants: [{ name: 'Large', price: 900 }], unknownLegacy: 'preserve' },
    { _id: 'second', title: 'Bread', sellerId: 'old-user', available: true, price: 200 },
    { _id: 'archived', sellerId: 'old-user', deletedAt: new Date() },
    { _id: 'draft', sellerId: 'old-user', draft: true },
    { _id: 'hidden', sellerId: 'suspended-user' },
  ]);
  const { NestFactory } = await import('@nestjs/core');
  const { AppModule } = await import('../dist/app.js');
  const app = await NestFactory.create(AppModule, { logger: false, abortOnError: false });
  t.after(() => app.close());
  app.setGlobalPrefix('api', { exclude: ['health', 'readyz'] });
  await app.listen(0, '127.0.0.1');
  const start = await app.getUrl();
  const get = path => fetch(start + path);
  assert.equal((await get('/readyz')).status, 200);
  const market = await (await get('/api/markets/old-shop')).json();
  assert.deepEqual([market.id, market.lat, market.lng], ['old-shop', 0, 0]);
  assert.equal(market.phone, undefined);

  const page = await (await get('/api/products?limit=1')).json();
  assert.equal(page.next, 'first');
  assert.deepEqual(page.items[0].imageURLs, photos);
  const product = await (await get('/api/products/first')).json();
  assert.match(product.variants[0].id, /^[a-f0-9]{24}$/);
  assert.equal(page.items[0].variants[0].id, product.variants[0].id);
  assert.equal(product.unknownLegacy, undefined);
  assert.equal((await db.collection('products').findOne({ _id: 'first' })).unknownLegacy, 'preserve');
  assert.deepEqual((await (await get('/api/products?limit=1&after=first')).json()).items.map(p => p.id), ['second']);
  assert.deepEqual((await (await get('/api/products?q=%28large%29')).json()).map(p => p.id), ['first']);
  assert.equal((await get('/api/products?limit=-1')).status, 400);
  for (const id of ['archived', 'draft', 'hidden']) assert.equal((await get('/api/products/' + id)).status, 404);
  for (const path of photos) {
    const response = await get(path);
    assert.equal(response.status, 200);
    assert.equal(response.headers.get('content-type'), 'image/png');
    assert.ok((await response.arrayBuffer()).byteLength > 0);
  }
  assert.equal((await get(privatePhoto)).status, 404);
});
