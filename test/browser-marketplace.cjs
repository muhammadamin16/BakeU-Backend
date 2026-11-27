const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { createHmac, randomBytes, randomUUID } = require('node:crypto');
const { once } = require('node:events');
const { mkdtemp } = require('node:fs/promises');
const { tmpdir } = require('node:os');
const { resolve } = require('node:path');
const { pathToFileURL } = require('node:url');
const { setTimeout: sleep } = require('node:timers/promises');
const { chromium } = require('playwright-core');
const sharp = require('sharp');

const root = resolve(__dirname, '..');
const site = resolve(root, '../BakeU.UZ');
const uri = process.env.TEST_REPLICA_URI;
assert.match(uri ?? '', /^mongodb:\/\/127\.0\.0\.1:\d+\/\?directConnection=true$/);
const key = randomBytes(32).toString('hex');
const bridgeToken = randomBytes(32).toString('hex');
const api = 'http://127.0.0.1:3847';
const web = 'http://localhost:3848';

async function ready(url) {
  for (let attempt = 0; attempt < 100; attempt++) {
    try { if ((await fetch(url)).ok) return; } catch {}
    await sleep(100);
  }
  throw new Error('Service not ready: ' + url);
}

const children = [];
function start(script, cwd, env) {
  const child = spawn(process.execPath, [script], { cwd, env: { ...process.env, ...env }, stdio: ['ignore', 'ignore', 'inherit'] });
  children.push(child);
  return child;
}
async function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, 'exit');
  child.kill();
  await exited;
}
async function main() {
  const db = 'bakeu_browser_' + randomUUID().replaceAll('-', '');
  function startApi(adminUserIds = '') {
    return start('dist/main.js', root, {
      PORT: '3847', MONGODB_URI: uri, MONGODB_DB: db, JWT_SECRET: key, TELEGRAM_BRIDGE_TOKEN: bridgeToken,
      TELEGRAM_BOT_USERNAME: 'BakeuTestBot', AUTH_BROWSER_ORIGINS: web, ADMIN_USER_IDS: adminUserIds,
    });
  }
  let backend = startApi();
  start('scripts/website-server.mjs', site, { PORT: '3848', WEB_ACCOUNTS_MODE: 'go',
    WEB_GO_API_URL: api + '/api', WEB_BOT_API_URL: 'http://127.0.0.1:3849/api', WEB_ALLOWED_ORIGINS: web });
  const botToken = '11111:disposable-browser-fixture';
  const { createApiServer } = await import(pathToFileURL(resolve(site, 'src/bot/services/httpServer.js')).href);
  const bot = createApiServer({ auth: {}, repo: {}, config: { token: botToken, adminIds: [], origins: [web] },
    ordersSettings: { enabled: false, accountsEnabled: true, baseUrl: api, token: bridgeToken } });
  await new Promise(resolve => bot.listen(3849, '127.0.0.1', resolve));
  await Promise.all([ready(api + '/readyz'), ready(web + '/healthz')]);
  const sellerDir = await mkdtemp(resolve(tmpdir(), 'bakeu-seller-'));
  const buyerDir = await mkdtemp(resolve(tmpdir(), 'bakeu-buyer-'));
  const contexts = [];
  async function browser(directory) {
    const context = await chromium.launchPersistentContext(directory, {
      executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium', args: ['--no-sandbox'], headless: true,
      viewport: { width: 1200, height: 900 },
    });
    contexts.push(context);
    context.setDefaultTimeout(10000);
    return context;
  }
  try {
    let sellerBrowser = await browser(sellerDir);
    const buyerBrowser = await browser(buyerDir);
    const mode = { origin: web, 'x-bakeu-session': 'browser' };
    async function register(context, role, phone, actor, viaUI = true) {
      const page = viaUI ? await context.newPage() : null;
      let started;
      if (page) {
        await page.route('https://tile.openstreetmap.org/**', route => route.abort());
        await page.goto(web + '/#/register');
        await page.locator('.go-phone-auth select').selectOption(role);
        await page.getByRole('button', { name: 'Продолжить' }).click();
        await page.getByLabel('Имя', { exact: true }).fill('Browser');
        await page.getByLabel('Фамилия', { exact: true }).fill(role);
        await page.locator('#go-auth-phone').fill(phone.slice(4));
        if (role === 'seller') {
          await page.getByLabel('Название магазина').fill('Browser bakery');
          await page.getByLabel('Адрес магазина').fill('Tashkent');
          await page.getByRole('button', { name: 'Выбрать на карте' }).click();
          await page.locator('.bakeu-location-map-canvas').click({ position: { x: 190, y: 100 } });
        }
        const waiting = page.waitForResponse(res => res.url().endsWith('/api/auth/phone/start') && res.request().method() === 'POST');
        await page.getByRole('button', { name: 'Получить код в Telegram' }).click();
        started = await waiting;
      } else {
        started = await context.request.post(web + '/api/auth/phone/start', {
          headers: mode, data: { purpose: 'register', role, phone, firstName: 'Browser', lastName: role,
            ...(role === 'seller' && { marketName: 'Second browser bakery', address: 'Tashkent', lat: 0, lng: 0 }) },
        });
      }
      assert.equal(started.status(), 201, await started.text());
      const { id } = await started.json();
      const proof = await fetch(api + '/api/integrations/telegram/auth/phone/contact', {
        method: 'POST', headers: { 'content-type': 'application/json', authorization: 'Bearer ' + bridgeToken, 'x-telegram-user-id': actor },
        body: JSON.stringify({ id, phone }),
      });
      assert.equal(proof.status, 200);
      const { code } = await proof.json();
      let verified;
      if (page) {
        await page.getByLabel('Код из Telegram').fill(code);
        const waiting = page.waitForResponse(res => res.url().endsWith('/api/auth/phone/verify') && res.request().method() === 'POST');
        await page.getByRole('button', { name: 'Подтвердить и войти' }).click();
        verified = await waiting;
      } else {
        verified = await context.request.post(web + '/api/auth/phone/verify', { headers: mode, data: { id, code } });
      }
      assert.equal(verified.status(), 200, await verified.text());
      const result = await verified.json();
      assert.equal((await context.cookies()).find(cookie => cookie.name === '__Host-bakeu-refresh')?.httpOnly, true);
      await page?.close();
      return result;
    }
    const seller = await register(sellerBrowser, 'seller', '+998901234571', '301');
    const buyer = await register(buyerBrowser, 'customer', '+998901234572', '302');
    const sellerHeaders = { authorization: 'Bearer ' + seller.token };
    const shopResponse = await sellerBrowser.request.get(web + '/api/market/mine', { headers: sellerHeaders });
    assert.equal(shopResponse.status(), 200);
    const shop = await shopResponse.json();
    const productEditor = await sellerBrowser.newPage();
    await productEditor.goto(web + '/#/account/orders');
    const edit = productEditor.getByRole('group', { name: 'Добавить товар' });
    await edit.getByLabel('Название', { exact: true }).fill('Browser three photos');
    await edit.getByLabel('Цена', { exact: true }).fill('100');
    await edit.locator('select').first().selectOption({ index: 1 });
    const creating = productEditor.waitForResponse(res => res.url().endsWith('/api/products') && res.request().method() === 'POST');
    await edit.getByRole('button', { name: 'Сохранить товар' }).click();
    const created = await creating;
    assert.equal(created.status(), 201);
    const { id } = await created.json();
    const files = [];
    for (const background of ['red', 'blue', 'green']) {
      const buffer = await sharp({ create: { width: 32, height: 32, channels: 3, background } }).png().toBuffer();
      files.push({ name: background + '.png', mimeType: 'image/png', buffer });
    }
    const uploading = productEditor.waitForResponse(res => res.url().endsWith('/api/products/' + id + '/image'));
    await productEditor.getByLabel('Загрузить фото').setInputFiles(files);
    const uploaded = await uploading;
    assert.equal(uploaded.status(), 201);
    const photos = (await uploaded.json()).imageURLs;
    const publishing = productEditor.waitForResponse(res => res.url().endsWith('/api/products/' + id) && res.request().method() === 'PUT');
    await productEditor.getByRole('button', { name: 'Опубликовать товар' }).click();
    assert.equal((await publishing).status(), 200);
    const secondSellerBrowser = await browser(await mkdtemp(resolve(tmpdir(), 'bakeu-second-seller-')));
    const other = await register(secondSellerBrowser, 'seller', '+998901234573', '303', false);
    const otherHeaders = { authorization: 'Bearer ' + other.token };
    const otherProduct = await secondSellerBrowser.request.post(web + '/api/products', { headers: otherHeaders, data: { title: 'Second shop buns', price: 23 } });
    assert.equal(otherProduct.status(), 201);
    const { id: otherId } = await otherProduct.json();
    const otherForm = new FormData();
    for (const file of files.slice(0, 2)) otherForm.append('image', new Blob([file.buffer], { type: file.mimeType }), file.name);
    const otherPhotos = await fetch(web + '/api/products/' + otherId + '/image', { method: 'POST', headers: otherHeaders, body: otherForm });
    assert.equal(otherPhotos.status, 201);
    assert.equal((await secondSellerBrowser.request.put(web + '/api/products/' + otherId, { headers: otherHeaders,
      data: { publish: true } })).status(), 200);

    await sellerBrowser.close();
    await stop(backend);
    backend = startApi(buyer.user.uid);
    await ready(api + '/readyz');
    sellerBrowser = await browser(sellerDir);
    const sellerPage = await sellerBrowser.newPage();
    await sellerPage.goto(web + '/#/messages');
    await sellerPage.getByRole('heading', { name: 'Сообщения' }).waitFor();
    const buyerPage = await buyerBrowser.newPage();
    await buyerPage.goto(web + '/#/product/' + id);
    const gallery = buyerPage.locator('.photo-carousel');
    await gallery.locator('.photo-carousel__count').getByText('1 / 3').waitFor();
    assert.equal(await gallery.locator('.photo-carousel__thumbs button').count(), 3);
    const cover = gallery.locator('.photo-carousel__image img');
    await cover.evaluate(image => image.decode());
    await gallery.locator('.photo-carousel__arrow.is-next').click();
    assert.equal(await cover.getAttribute('src'), photos[1]);
    await gallery.locator('.photo-carousel__thumbs button').nth(2).click();
    assert.equal(await cover.getAttribute('src'), photos[2]);
    await gallery.locator('.photo-carousel__thumbs button').nth(2).press('ArrowLeft');
    // Arrows are scoped to the picture frame; keyboard works from its navigation buttons.
    await gallery.locator('.photo-carousel__arrow.is-next').press('ArrowLeft');
    assert.equal(await cover.getAttribute('src'), photos[1]);
    await cover.evaluate(image => {
      const start = new Touch({ identifier: 1, target: image, clientX: 100, clientY: 10 });
      const end = new Touch({ identifier: 1, target: image, clientX: 20, clientY: 10 });
      image.dispatchEvent(new TouchEvent('touchstart', { bubbles: true, touches: [start] }));
      image.dispatchEvent(new TouchEvent('touchend', { bubbles: true, changedTouches: [end] }));
    });
    assert.equal(await cover.getAttribute('src'), photos[2]);

    await buyerPage.goto(web + '/#/admin?section=products');
    const product = buyerPage.getByRole('link', { name: 'Browser three photos' });
    await product.waitFor();
    assert.equal(await product.locator('img').getAttribute('src'), photos[0]);
    const adminRow = buyerPage.getByRole('row').filter({ has: product });
    await adminRow.getByText('Фотографии товара', { exact: true }).click();
    const adminPhotos = adminRow.locator('details img');
    await adminPhotos.nth(2).waitFor();
    assert.equal(await adminPhotos.count(), 3);
    await buyerPage.goto(web + '/#/shop/' + shop.id);
    const follow = buyerPage.getByRole('button', { name: 'Подписаться', exact: true });
    await follow.click();
    await buyerPage.getByText('Подписчики: 1').waitFor();
    await buyerPage.getByRole('button', { name: 'Отписаться', exact: true }).click();
    await buyerPage.getByText('Подписчики: 0').waitFor();

    const sellerSocket = sellerPage.waitForResponse(response =>
      response.url().includes('/api/chat/socket') && response.request().method() === 'GET' && response.ok());
    await sellerPage.reload();
    await sellerSocket;
    await buyerPage.getByRole('link', { name: 'Написать магазину' }).click();
    await buyerPage.getByRole('textbox', { name: 'Сообщение' }).fill('Hello from buyer browser');
    await buyerPage.getByRole('button', { name: 'Отправить' }).click();
    await sellerPage.getByRole('button', { name: /Browser customer/ }).click();
    await sellerPage.getByRole('log').getByText('Hello from buyer browser').waitFor();
    await sellerPage.getByRole('textbox', { name: 'Сообщение' }).fill('Reply from seller browser');
    await sellerPage.getByRole('button', { name: 'Отправить' }).click();
    await buyerPage.getByRole('log').getByText('Reply from seller browser').waitFor();
    await buyerPage.goto(web + '/#/account/orders?checkout=1');
    await buyerPage.getByRole('spinbutton', { name: 'Количество: Browser three photos' }).fill('2');
    await buyerPage.getByRole('spinbutton', { name: 'Количество: Second shop buns' }).fill('3');
    const deliveries = buyerPage.getByRole('group', { name: 'Получение заказа' });
    assert.equal(await deliveries.count(), 2);
    for (const group of await deliveries.all()) {
      await group.getByLabel('Способ получения').selectOption('pickup');
      await group.getByLabel('Дата (Ташкент)').fill(new Date(Date.now() + 2 * 86400000).toISOString().slice(0, 10));
      await group.getByLabel('Время').fill('12:00');
      await group.getByLabel('Комментарий').fill('Browser pickup');
    }
    const ordering = buyerPage.waitForResponse(res => res.url().endsWith('/api/orders') && res.request().method() === 'POST');
    await buyerPage.getByRole('button', { name: 'Оформить заказ', exact: true }).click();
    const checkout = await ordering;
    assert.equal(checkout.status(), 201);
    const orders = (await checkout.json()).orders;
    assert.equal(orders.length, 2);
    const incoming = orders.find(order => order.sellerId === seller.user.uid);
    await sellerPage.goto(web + '/#/account/orders');
    const card = sellerPage.getByRole('article').filter({ hasText: 'Заказ #' + incoming.number });
    await card.getByRole('button', { name: 'Принят', exact: true }).click();
    const buyerOrder = buyerPage.getByRole('article').filter({ hasText: 'Заказ #' + incoming.number });
    await buyerOrder.getByText('Принят', { exact: true }).first().waitFor();
    const mini = await browser(await mkdtemp(resolve(tmpdir(), 'bakeu-mini-')));
    const init = new URLSearchParams({ auth_date: String(Math.floor(Date.now() / 1000)),
      query_id: 'browser-fixture', user: JSON.stringify({ id: 302, first_name: 'Browser' }) });
    const check = [...init].sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => k + '=' + v).join('\n');
    const secret = createHmac('sha256', 'WebAppData').update(botToken).digest();
    init.set('hash', createHmac('sha256', secret).update(check).digest('hex'));
    await mini.addInitScript(initData => { window.Telegram = { WebApp: { initData, ready() {}, expand() {} } }; }, init.toString());
    const miniPage = await mini.newPage();
    const launch = miniPage.waitForResponse(response => response.url().endsWith('/api/auth/phone/telegram') && response.ok());
    await miniPage.goto(web + '/#/product/' + id);
    await launch;
    await miniPage.locator('.photo-carousel__count').getByText('1 / 3').waitFor();
    assert.equal(await miniPage.locator('.photo-carousel__thumbs button').count(), 3);
    await miniPage.locator('.photo-carousel__arrow.is-next').click();
    assert.equal(await miniPage.locator('.photo-carousel__image img').getAttribute('src'), photos[1]);
    await miniPage.goto(web + '/#/admin/telegram?section=products');
    await miniPage.getByRole('link', { name: 'Browser three photos' }).waitFor();
    console.log('PASS Chromium: Website and signed Mini App fixture use the same catalog and Admin.');
    console.log('PASS Chromium: buyer and seller registration forms with Telegram bridge fixture, persistent session after browser/backend restart, three-photo carousel, admin cover, follow count, two-shop checkout and seller acceptance, and live chat in two independent browser processes.');
  } finally {
    await Promise.allSettled(contexts.map(context => context.close()));
    await new Promise(resolve => bot.close(resolve));
  }
}
main().finally(() => Promise.all(children.map(stop))).catch(error => { console.error(error); process.exitCode = 1; });
