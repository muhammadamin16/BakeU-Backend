import {
  BadRequestException, Body, ConflictException, Controller, Delete, ForbiddenException, Get,
  Headers, HttpCode, NotFoundException, Param, Patch, Post, Put, Query, Req, ServiceUnavailableException,
} from '@nestjs/common';
import { createHash, randomUUID } from 'node:crypto';
import { ObjectId, type ClientSession, type Collection, type Filter } from 'mongodb';
import { activeStatuses, salesReport } from './analytics.js';
import { Auth } from './auth.js';
import { publicProduct, type Product } from './catalog.js';
import { Database } from './database.js';
import { priceInMinorUnits } from './seller-products.js';

type Input = Record<string, unknown>;
type Request = { headers: Record<string, string | undefined> };
type Line = { productId: string; variantId: string; qty: number };
type Cart = { _id: string; items: Line[]; revision: string };
type Favorites = { _id: string; productIds: string[]; revision: string };
type Shop = { _id: string; sellerId: string; name: string; fulfillment?: string; status: string; address?: string };
type Snapshot = Line & { title: string; variantName?: string; price: number; imageURL?: string };
type Event = { status: string; at: Date; actorId: string; version: number };
type Order = {
  _id: string; sellerId: string; customerId: string; shopId: string; shopName: string;
  buyerName: string; buyerPhone: string; fulfillment: string; comment: string;
  location: { latitude: number; longitude: number } | null; scheduledAt: Date;
  deliveryAddress: string; status: string; version: number; workflowVersion: number;
  items: Snapshot[]; total: number; history: Event[]; createdAt: Date; updatedAt: Date;
};
type Job = {
  _id: ObjectId; orderId: string; userId: string; kind: string; order: ReturnType<typeof view>;
  deliveryState: string; attempts: number; createdAt: Date; nextAt?: Date;
  leaseUntil?: Date; leaseToken?: string; messageId?: number;
};
type Mutation = { _id: string; fingerprint: string; result: unknown };
const validId = (value: unknown): value is string => typeof value === 'string' && /^[\w-]{1,80}$/.test(value);
const hash = (data: unknown) => createHash('sha256').update(JSON.stringify(data)).digest('hex');
const invalid = () => new BadRequestException('Invalid cart or delivery details');

function line(input: Input): Line {
  const { productId, variantId = '', qty } = input ?? {};
  if (!validId(productId) || (variantId !== '' && !validId(variantId)) ||
      typeof qty !== 'number' || !Number.isInteger(qty) || qty < 1 || qty > 1000) throw invalid();
  return { productId, variantId, qty };
}
function sameLine(a: Line, b: Pick<Line, 'productId' | 'variantId'>) {
  return a.productId === b.productId && a.variantId === b.variantId;
}
function delivery(value: Input, shop: Shop) {
  const { fulfillment, scheduledAt, location, deliveryAddress = '', comment = '' } = value ?? {};
  if ((fulfillment !== 'pickup' && fulfillment !== 'delivery') ||
      (shop.fulfillment && shop.fulfillment !== 'both' && shop.fulfillment !== fulfillment) ||
      typeof scheduledAt !== 'string' || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{3})?Z$/.test(scheduledAt) ||
      typeof deliveryAddress !== 'string' || deliveryAddress.length > 500 ||
      (fulfillment === 'delivery' && !deliveryAddress.trim()) || typeof comment !== 'string' || comment.length > 1000) throw invalid();
  const date = new Date(scheduledAt);
  const now = Date.now();
  if (!Number.isFinite(date.getTime()) || date.getTime() <= now || date.getTime() > now + 366 * 86400000) throw invalid();
  let coordinates = null;
  if (fulfillment === 'delivery' && location != null) {
    const { latitude, longitude } = location as Input;
    if (typeof latitude !== 'number' || typeof longitude !== 'number' ||
        !Number.isFinite(latitude) || !Number.isFinite(longitude) || Math.abs(latitude) > 90 || Math.abs(longitude) > 180) throw invalid();
    coordinates = { latitude, longitude };
  }
  return { fulfillment, scheduledAt: date, location: coordinates, deliveryAddress: fulfillment === 'pickup' ? '' : deliveryAddress.trim(), comment: comment.trim() };
}

function view(order: Order) {
  const { _id, sellerId, customerId, shopId, shopName, buyerName, buyerPhone, fulfillment, comment, location,
    scheduledAt, deliveryAddress, status, version, workflowVersion, items, total, history, createdAt, updatedAt } = order;
  return { id: _id, number: (order as Order & { number?: string }).number || 'BU-' + _id,
    sellerId, customerId, shopId, shopName, buyerName, buyerPhone, fulfillment: fulfillment || 'delivery',
    comment, location, scheduledAt, deliveryAddress, address: deliveryAddress, version: version ?? 0, workflowVersion,
    status: status === 'out_for_delivery' ? 'delivering' : status === 'delivered' && (workflowVersion ?? 0) < 2 ? 'completed' : status,
    items: items ?? [], total, history: history ?? [], createdAt, updatedAt };
}

@Controller()
export class Commerce {
  constructor(private readonly database: Database, private readonly auth: Auth) {}
  private get db() { return this.database.db; }
  private get carts() { return this.db.collection<Cart>('carts'); }
  private get favorites() { return this.db.collection<Favorites>('favorites'); }
  private get jobs() { return this.db.collection<Job>('telegram_order_outbox'); }
  private get orders() { return this.db.collection<Order>('orders'); }
  private async actor(req: Request) {
    const user = await this.auth.current(req.headers.authorization);
    if (req.headers['x-bakeu-account'] && req.headers['x-bakeu-account'] !== user._id) throw new ForbiddenException('Account changed');
    return user;
  }

  private async currentCart(uid: string, session?: ClientSession) {
    const cart = await this.carts.findOneAndUpdate({ _id: uid }, { $setOnInsert: { items: [], revision: randomUUID() } },
      { upsert: true, returnDocument: 'after', session });
    return { ...cart!, revision: cart?.revision ?? '',
      items: (cart?.items ?? []).map(item => ({ ...item, variantId: item.variantId ?? '' })) };
  }

  private async listing(uid: string, item: Line, session?: ClientSession) {
    const product = await this.db.collection<Product>('products').findOne({
      _id: item.productId, moderationStatus: { $ne: 'hidden' }, draft: { $ne: true }, deletedAt: { $exists: false }, available: true,
    }, { session });
    if (!product || product.sellerId === uid) throw invalid();
    const shop = await this.db.collection<Shop>('markets').findOne({ sellerId: product.sellerId, status: 'approved' }, { session });
    if (!shop) throw invalid();
    const variant = publicProduct(product).variants?.find(entry => entry.id === item.variantId);
    if (item.variantId && !variant) throw invalid();
    const minor = priceInMinorUnits(variant?.price ?? product.price);
    return { shop, minor, snapshot: { ...item, title: product.title, ...(variant && { variantName: variant.name }),
      price: minor / 100, ...(product.imageURLs?.[0] && { imageURL: product.imageURLs[0] }) } };
  }

  @Get('cart')
  async cart(@Req() req: Request) {
    const { items, revision } = await this.currentCart((await this.actor(req))._id);
    return { items, revision };
  }
  private async mutateCart(uid: string, revision: unknown, change: (items: Line[]) => Line[]) {
    const cart = await this.currentCart(uid);
    if (revision !== cart.revision) throw new ConflictException('Cart changed');
    const items = change(cart.items);
    if (items.length > 100) throw invalid();
    const next = randomUUID();
    const result = await this.carts.updateOne(this.cartFilter(uid, revision), { $set: { items, revision: next } });
    if (!result.matchedCount) throw new ConflictException('Cart changed');
    return { items, revision: next };
  }
  private cartFilter(uid: string, revision: string): Filter<Cart> {
    return { _id: uid, ...(revision ? { revision } : { $or: [{ revision: '' }, { revision: { $exists: false } }] }) };
  }
  @Post('cart/items')
  @HttpCode(200)
  async save(@Req() req: Request, @Body() body: Input) {
    const uid = (await this.actor(req))._id;
    const item = line(body);
    await this.listing(uid, item);
    return this.mutateCart(uid, body.cartRevision, items => [...items.filter(value => !sameLine(value, item)), item]);
  }
  @Delete('cart/items/:id')
  async remove(@Req() req: Request, @Param('id') productId: string, @Query() query: Input) {
    const uid = (await this.actor(req))._id;
    const item = line({ ...query, productId, qty: 1 });
    return this.mutateCart(uid, query.cartRevision, items => items.filter(value => !sameLine(value, item)));
  }

  private async favoriteStore(uid: string) {
    return this.favorites.findOneAndUpdate({ _id: uid }, { $setOnInsert: { productIds: [], revision: randomUUID() } },
      { upsert: true, returnDocument: 'after' }).then(doc => doc!);
  }
  private favoriteView(doc: Favorites) { return { uid: doc._id, productIds: doc.productIds, revision: doc.revision }; }
  @Get('favorites')
  async saved(@Req() req: Request) { return this.favoriteView(await this.favoriteStore((await this.actor(req))._id)); }
  @Put('favorites/:id')
  async addFavorite(@Req() req: Request, @Param('id') id: string) {
    const uid = (await this.actor(req))._id;
    if (!validId(id) || !await this.db.collection<Product>('products').findOne({ _id: id, deletedAt: { $exists: false }, draft: { $ne: true } })) throw invalid();
    await this.favoriteStore(uid);
    const doc = await this.favorites.findOneAndUpdate({ _id: uid, $expr: { $lt: [{ $size: '$productIds' }, 1000] } },
      { $addToSet: { productIds: id }, $set: { revision: randomUUID() } }, { returnDocument: 'after' });
    if (!doc) throw invalid();
    return this.favoriteView(doc);
  }
  @Delete('favorites/:id')
  async removeFavorite(@Req() req: Request, @Param('id') id: string) {
    const uid = (await this.actor(req))._id;
    await this.favoriteStore(uid);
    const doc = await this.favorites.findOneAndUpdate({ _id: uid }, { $pull: { productIds: id },
      $set: { revision: randomUUID() } }, { returnDocument: 'after' });
    return this.favoriteView(doc!);
  }
  @Delete('favorites')
  async clearFavorites(@Req() req: Request, @Query('revision') revision: unknown) {
    const uid = (await this.actor(req))._id;
    if (typeof revision !== 'string') throw invalid();
    const doc = await this.favorites.findOneAndUpdate({ _id: uid, revision },
      { $set: { productIds: [], revision: randomUUID() } }, { returnDocument: 'after' });
    if (!doc) throw new ConflictException('Favorites changed');
    return this.favoriteView(doc);
  }

  @Get('orders/capabilities')
  async capabilities(@Req() req: Request) {
    await this.actor(req);
    const server = await this.db.command({ hello: 1 });
    if (!server.isWritablePrimary || !(server.setName || server.msg === 'isdbgrid')) throw new ServiceUnavailableException('Transactional checkout is unavailable');
    return { workflowVersion: 2, idempotency: true, history: true, variantSnapshots: true, cartRevision: true, deliveryScheduling: true };
  }
  @Post('orders/:id/complaints')
  async complain(@Req() req: Request, @Param('id') orderId: string, @Body() input: Input) {
    const userId = (await this.actor(req))._id;
    const subject = input?.subject;
    if (typeof subject !== 'string' || subject.trim().length < 5 || subject.trim().length > 500) throw new BadRequestException('Invalid complaint');
    if (!await this.orders.findOne({ _id: orderId, customerId: userId })) throw new NotFoundException('Order not found');
    const id = hash([orderId, userId]);
    const result = await this.db.collection<{ _id: string }>('complaints').updateOne({ _id: id },
      { $setOnInsert: { userId, orderId, subject: subject.trim(), status: 'open', createdAt: new Date() } }, { upsert: true });
    if (!result.upsertedCount) throw new ConflictException('A complaint already exists for this order');
    return { id, status: 'open' };
  }

  @Get('orders')
  async history(@Req() req: Request) {
    const uid = (await this.actor(req))._id;
    const find = (filter: Filter<Order>) => this.orders.find(filter).sort({ createdAt: -1 }).limit(300).toArray().then(rows => rows.map(view));
    const [placed, incoming] = await Promise.all([find({ customerId: uid }), find({ sellerId: uid })]);
    return { placed, incoming };
  }

  private async once(uid: string, scope: string, req: Request, body: Input,
    work: (session: ClientSession) => Promise<unknown>) {
    const key = req.headers['idempotency-key'];
    if (!key || !/^[\w:-]{1,128}$/.test(key)) throw new BadRequestException('Idempotency-Key is required');
    const collection: Collection<Mutation> = this.db.collection('order_mutations');
    const action = uid + ':' + scope + ':' + key;
    const fingerprint = hash(body);
    return this.database.transaction(async session => {
      const previous = await collection.findOne({ _id: action }, { session });
      if (previous) {
        if (previous.fingerprint !== fingerprint) throw new ConflictException('Idempotency key already used');
        return previous.result;
      }
      const result = await work(session);
      await collection.insertOne({ _id: action, fingerprint, result }, { session });
      return result;
    });
  }

  private async notice(order: Order, userId: string, session: ClientSession) {
    const jobId = new ObjectId();
    const kind = order.status === 'pending' ? 'order.created' : 'order.status_changed';
    const item = { orderId: order._id, userId, status: order.status, kind, createdAt: order.updatedAt };
    await this.db.collection('notifications').insertOne({
      _id: jobId, ...item, title: `Заказ BU-${order._id}: ${order.status}`, read: false, telegramState: 'pending',
    }, { session });
    await this.jobs.insertOne({ _id: jobId, ...item, order: view(order), deliveryState: 'pending', attempts: 0 }, { session });
  }

  @Post('orders')
  async checkout(@Req() req: Request, @Body() body: Input) {
    const user = await this.actor(req);
    if (!user.phoneVerifiedAt || !user.verifiedPhoneE164) throw new ForbiddenException('A verified phone is required');
    if (!Array.isArray(body?.items) || !body.items.length || body.items.length > 100 || typeof body.cartRevision !== 'string') throw invalid();
    const selection = body.items.map(line);
    if (selection.some((item, i) => selection.slice(0, i).some(previous => sameLine(item, previous)))) throw invalid();
    return this.once(user._id, 'checkout', req, body, async session => {
      const cart = await this.currentCart(user._id, session);
      if (cart.revision !== body.cartRevision || cart.items.some(saved => !selection.some(item => sameLine(saved, item) && saved.qty === item.qty))) {
        throw new ConflictException('Cart changed');
      }
      const groups = new Map<string, { shop: Shop; items: Snapshot[]; minor: bigint }>();
      for (const item of selection) {
        const { shop, minor, snapshot } = await this.listing(user._id, item, session);
        const group = groups.get(shop._id) ?? { shop, items: [], minor: 0n };
        group.items.push(snapshot);
        group.minor += BigInt(minor) * BigInt(item.qty);
        groups.set(shop._id, group);
      }
      const deliveries = body.deliveries as Input | undefined;
      if (!deliveries && groups.size !== 1) throw invalid();
      const now = new Date();
      const orders: Order[] = [];
      for (const { shop, items, minor } of groups.values()) {
        if (minor > 100000000000000n) throw invalid();
        const details = delivery((deliveries ? deliveries[shop._id] : body) as Input, shop);
        orders.push({ _id: new ObjectId().toHexString(), shopId: shop._id, shopName: shop.name, sellerId: shop.sellerId,
          customerId: user._id, buyerName: user.name, buyerPhone: user.verifiedPhoneE164!, ...details, status: 'pending',
          workflowVersion: 2, version: 1, items, total: Number(minor) / 100, createdAt: now, updatedAt: now,
          history: [{ status: 'pending', actorId: user._id, at: now, version: 1 }] });
      }
      const changed = await this.carts.updateOne(this.cartFilter(user._id, cart.revision),
        { $set: { items: [], revision: randomUUID() } }, { session });
      if (!changed.matchedCount) throw new ConflictException('Cart changed');
      await this.orders.insertMany(orders, { session });
      for (const order of orders) await this.notice(order, order.sellerId, session);
      return { orders: orders.map(view) };
    });
  }

  @Patch('orders/:id/status')
  async transition(@Req() req: Request, @Param('id') id: string, @Body() body: Input) {
    return this.changeStatus((await this.actor(req))._id, req, id, body);
  }

  private async changeStatus(actorId: string, req: Request, id: string, body: Input, bot = false) {
    return this.once(actorId, 'status:' + id, req, body, async session => {
      const order = await this.orders.findOne({ _id: id, sellerId: actorId }, { session });
      if (!order) throw new NotFoundException('Order not found');
      const target = body.status;
      const displayed = view(order);
      if (displayed.status !== body.expectedStatus || (!bot && displayed.version !== body.expectedVersion)) throw new ConflictException('Order changed');
      const graph: Record<string, string[]> = {
        pending: ['accepted', 'rejected'], accepted: ['preparing', 'cancelled'], preparing: ['ready', 'cancelled'],
        ready: [order.fulfillment === 'pickup' ? 'completed' : 'delivering', 'cancelled'], delivering: ['delivered'], delivered: ['completed'],
      };
      if (typeof target !== 'string' || !Object.hasOwn(graph, displayed.status) || !graph[displayed.status].includes(target)) throw invalid();
      const at = new Date();
      const version = displayed.version + 1;
      const updated = await this.orders.findOneAndUpdate({ _id: id, status: order.status, version: order.version ?? { $exists: false } },
        { $set: { status: target, version, updatedAt: at, workflowVersion: 2 },
          $push: { history: { status: target, at, actorId, version } } }, { returnDocument: 'after', session });
      if (!updated) throw new ConflictException('Order changed');
      await this.notice(updated, updated.customerId, session);
      return view(updated);
    });
  }
  private async botAccount(req: Request) {
    const actor = this.auth.bridgeActor(req);
    const user = await this.auth.telegramUser(actor);
    if (!user) throw new ForbiddenException('Telegram account is unavailable');
    return user;
  }
  private async botMarket(userId: string) {
    const market = await this.db.collection<Shop>('markets').findOne({ sellerId: userId, status: 'approved' });
    if (!market) throw new ForbiddenException('An approved shop is required');
    return market;
  }

  @Get('integrations/telegram/me')
  async telegramIdentity(@Req() req: Request) {
    const telegramId = this.auth.bridgeActor(req);
    const user = await this.auth.telegramUser(telegramId);
    if (!user) return { linked: false };
    const market = await this.db.collection<Shop>('markets').findOne({ sellerId: user._id, status: 'approved' });
    return { linked: true, confirmed: true, botStarted: true, telegramId,
      ...(market && { shopId: market._id }),
      user: { id: user._id, role: market ? 'SELLER' : 'CLIENT', status: 'active', canBuy: true } };
  }

  @Get('integrations/telegram/shop')
  async telegramShop(@Req() req: Request) {
    const user = await this.botAccount(req);
    const shop = await this.botMarket(user._id);
    const [productCount, statuses, stats] = await Promise.all([
      this.db.collection('products').countDocuments({ sellerId: user._id, deletedAt: { $exists: false }, draft: { $ne: true } }),
      this.orders.aggregate([{ $match: { sellerId: user._id, shopId: shop._id } },
        { $group: { _id: '$status', count: { $sum: 1 } } }]).toArray(),
      salesReport(this.database, 'year', undefined, { sellerId: user._id }),
    ]);
    const counts = Object.fromEntries(statuses.map(row => [row._id, row.count]));
    counts.delivering = (counts.delivering ?? 0) + (counts.out_for_delivery ?? 0);
    return { id: shop._id, ownerId: user._id, name: shop.name, phone: user.phone, address: shop.address ?? '',
      productCount, counts, revenue: stats.totals.revenue };
  }

  @Get('integrations/telegram/shop/products')
  async telegramProducts(@Req() req: Request) {
    const user = await this.botAccount(req);
    const shop = await this.botMarket(user._id);
    const products = await this.db.collection<Product>('products').find({
      sellerId: user._id, deletedAt: { $exists: false }, draft: { $ne: true }, moderationStatus: { $ne: 'hidden' },
    }).limit(100).toArray();
    return { ownerId: user._id, shopId: shop._id,
      items: products.map(({ _id: id, title, price }) => ({ id, title, price })) };
  }

  @Get('integrations/telegram/orders')
  async telegramOrders(@Req() req: Request, @Query() query: Input) {
    const user = await this.botAccount(req);
    const { audience, view: mode = 'all', page = '0', limit = '5' } = query;
    if ((audience !== 'buyer' && audience !== 'seller') || typeof mode !== 'string' ||
        typeof page !== 'string' || !/^\d{1,4}$/.test(page) ||
        typeof limit !== 'string' || !/^[1-5]$/.test(limit)) throw invalid();
    const filter: Filter<Order> = audience === 'seller' ?
      { sellerId: user._id, shopId: (await this.botMarket(user._id))._id } : { customerId: user._id };
    if (mode === 'active') filter.status = { $in: activeStatuses };
    else if (mode === 'history') filter.status = { $nin: activeStatuses };
    else if (mode !== 'all') {
      if (![...activeStatuses, 'completed', 'rejected', 'cancelled', 'delivered'].includes(mode)) throw invalid();
      filter.status = mode === 'delivering' ? { $in: ['delivering', 'out_for_delivery'] } : mode;
    }
    const size = Number(limit);
    const rows = await this.orders.find(filter).sort({ createdAt: -1, _id: -1 }).skip(Number(page) * size).limit(size + 1).toArray();
    return { orders: rows.slice(0, size).map(view), nextPage: rows.length > size ? Number(page) + 1 : null };
  }

  @Get('integrations/telegram/orders/:id')
  async telegramOrder(@Req() req: Request, @Param('id') id: string) {
    const user = await this.botAccount(req);
    const order = await this.orders.findOne({ _id: id, $or: [{ customerId: user._id }, { sellerId: user._id }] });
    if (!order || (order.customerId !== user._id && (await this.botMarket(user._id))._id !== order.shopId)) {
      throw new NotFoundException('Order not found');
    }
    return view(order);
  }

  @Patch('integrations/telegram/orders/:id/status')
  async telegramStatus(@Req() req: Request, @Param('id') id: string, @Body() body: Input) {
    const user = await this.botAccount(req);
    const shop = await this.botMarket(user._id);
    if (!await this.orders.findOne({ _id: id, shopId: shop._id, sellerId: user._id })) throw new NotFoundException('Order not found');
    return this.changeStatus(user._id, req, id, body, true);
  }

  private async recipient(userId: string, order: Job['order']) {
    const binding = await this.db.collection<{ _id: string }>('telegram_bindings').findOne({
      userId, blocked: { $ne: true }, botStarted: true, confirmedAt: { $exists: true },
    });
    const user = await this.db.collection<{ _id: string; role?: string; name?: string }>('users').findOne({
      _id: userId, disabled: { $ne: true }, frozen: { $ne: true },
      $and: [{ $or: [{ status: 'active' }, { status: '' }, { status: { $exists: false } }] },
        { $or: [{ freezeUntil: { $lte: new Date() } }, { freezeUntil: { $exists: false } }] }],
    });
    if (!binding || !user) return;
    const shop = user.role === 'seller' ? await this.db.collection('markets').findOne({ sellerId: userId, status: 'approved' }) : null;
    if (user.role === 'seller' && !shop) return;
    if (order.sellerId === userId && String(shop?._id) !== order.shopId) return;
    return { confirmed: true, telegramId: binding._id, botStarted: true,
      ...(shop && { shopId: String(shop._id) }), user: { id: userId, role: user.role, status: 'active', name: user.name, canBuy: true } };
  }

  private async deliveryState(job: Filter<Job>, state: string, extra: Partial<Job> = {}) {
    return this.database.transaction(async session => {
      const changed = await this.jobs.findOneAndUpdate(job, { $set: { deliveryState: state, ...extra } }, { session });
      if (changed) await this.db.collection('notifications').updateOne({ _id: changed._id }, { $set: { telegramState: state } }, { session });
      return changed;
    });
  }

  @Post('integrations/telegram/notifications/claim')
  @HttpCode(200)
  async claim(@Req() req: Request, @Body() body: Input) {
    this.auth.authorizeBridge(req);
    const limit = body?.limit ?? 10;
    if (typeof limit !== 'number' || !Number.isInteger(limit) || limit < 1 || limit > 10) throw invalid();
    const now = new Date();
    const expired = await this.jobs.find({ deliveryState: 'sending', leaseUntil: { $lte: now } }).limit(100).toArray();
    for (const item of expired) await this.deliveryState({ _id: item._id, deliveryState: 'sending', leaseUntil: { $lte: now } }, 'uncertain');
    const jobs = [];
    for (let index = 0; index < limit; index++) {
      const leaseToken = randomUUID();
      const job = await this.jobs.findOneAndUpdate({
        attempts: { $lt: 8 },
        $or: [{ deliveryState: 'pending' }, { deliveryState: 'retry', nextAt: { $lte: now } },
          { deliveryState: 'leased', leaseUntil: { $lte: now } }],
      }, { $set: { deliveryState: 'leased', leaseToken, leaseUntil: new Date(Date.now() + 60000) } },
      { sort: { createdAt: 1 }, returnDocument: 'after' });
      if (!job) break;
      const recipient = await this.recipient(job.userId, job.order);
      if (!recipient) {
        await this.deliveryState({ _id: job._id, leaseToken }, 'unlinked');
        continue;
      }
      jobs.push({ id: job._id.toHexString(), leaseToken, kind: job.kind, order: job.order, recipient });
    }
    return { jobs };
  }

  @Post('integrations/telegram/notifications/:id/:action')
  @HttpCode(200)
  async receipt(@Req() req: Request, @Param('id') jobId: string, @Param('action') action: string, @Body() body: Input) {
    this.auth.authorizeBridge(req);
    if (!/^[a-f0-9]{24}$/.test(jobId) || typeof body?.leaseToken !== 'string' || body.leaseToken.length > 64) throw invalid();
    const filter = { _id: new ObjectId(jobId), leaseToken: body.leaseToken };
    if (action === 'begin') {
      const job = await this.jobs.findOne({ ...filter, deliveryState: 'leased', leaseUntil: { $gt: new Date() } });
      if (!job) return { started: false };
      if (!await this.recipient(job.userId, job.order)) {
        await this.deliveryState({ ...filter, deliveryState: 'leased' }, 'blocked');
        return { started: false };
      }
      const result = await this.jobs.updateOne({ ...filter, deliveryState: 'leased', leaseUntil: { $gt: new Date() } },
        { $set: { deliveryState: 'sending', leaseUntil: new Date(Date.now() + 60000) }, $inc: { attempts: 1 } });
      return { started: result.matchedCount === 1 };
    }
    const job = await this.jobs.findOne(filter);
    if (!job) throw new ConflictException('Job lease changed');
    if (action === 'ack') {
      if (typeof body.messageId !== 'number' || !Number.isSafeInteger(body.messageId) || body.messageId <= 0) throw invalid();
      if (job.deliveryState === 'sent' && job.messageId === body.messageId) return { ok: true };
      if (!await this.deliveryState({ ...filter, deliveryState: { $in: ['sending', 'uncertain'] } }, 'sent', { messageId: body.messageId })) {
        throw new ConflictException('Job lease changed');
      }
      return { ok: true };
    }
    if (action !== 'fail' || typeof body.state !== 'string' || !['retry', 'blocked', 'failed', 'uncertain'].includes(body.state)) throw invalid();
    const requested = body.state;
    const state = requested === 'retry' && job.attempts >= 8 ? 'failed' : requested;
    const retryAt = typeof body.retryAt === 'string' ? Date.parse(body.retryAt) : NaN;
    const nextAt = new Date(Date.now() + Math.min(86400000, Math.max(1000, Number.isFinite(retryAt) ? retryAt - Date.now() : 2000)));
    if (!await this.deliveryState({ ...filter, deliveryState: { $in: ['leased', 'sending'] } },
      state, state === 'retry' ? { nextAt } : {})) throw new ConflictException('Job lease changed');
    return { ok: true };
  }

  @Put('me/notifications/:id/read')
  @HttpCode(204)
  async readNotification(@Req() req: Request, @Param('id') id: string) {
    const userId = (await this.actor(req))._id;
    if (!/^[a-f0-9]{24}$/.test(id)) throw invalid();
    const result = await this.db.collection('notifications').updateOne({ _id: new ObjectId(id), userId }, { $set: { read: true } });
    if (!result.matchedCount) throw new NotFoundException('Notification not found');
  }
  @Get('me/notifications')
  async notifications(@Req() req: Request) {
    const userId = (await this.actor(req))._id;
    const items = await this.db.collection('notifications').find({ userId }).sort({ createdAt: -1 }).limit(100).toArray();
    return { items: items.map(({ _id, ...item }) => ({ ...item, id: String(_id) })),
      unread: await this.db.collection('notifications').countDocuments({ userId, read: false }) };
  }
}
