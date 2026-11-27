import {
  BadRequestException, Body, Controller, ForbiddenException, Get, Headers, NotFoundException, Param, Patch, Query,
  ConflictException, Put,
} from '@nestjs/common';
import { ObjectId, type ClientSession, type Document } from "mongodb";
import { Auth, isAdminAccount } from './auth.js';
import { salesReport } from './analytics.js';
import { Database } from './database.js';

type Input = Record<string, unknown>;
type Row = { _id: string; [key: string]: unknown };
const pageSize = 50;
const productFields = 'description unit weight portions ingredients allergens variants updatedAt title category price sellerId available imageURLs draft deletedAt moderatedAt moderationStatus createdAt';
const userFields = 'name firstName lastName phone address role status disabled frozen freezeUntil createdAt';
const orderFields = 'version workflowVersion number buyerName buyerPhone customerId sellerId shopId shopName items total status createdAt history fulfillment deliveryAddress updatedAt';
const marketFields = 'city area latitude longitude fulfillment updatedAt name status address sellerId description logoURL rejectionReason createdAt';
const projection = (columns: string) => Object.fromEntries(columns.split(' ').map(field => [field, 1]));

const sections: Record<string, { collection: string; fields: string; filter?: Document }> = {
      customers: { collection: 'users', fields: userFields, filter: { role: 'customer' } },
      sellers: { collection: 'users', fields: userFields, filter: { role: 'seller' } },
      applications: { collection: 'markets', fields: marketFields },
      shops: { collection: 'markets', fields: marketFields },
      products: { collection: 'products', fields: productFields },
      orders: { collection: 'orders', fields: orderFields, filter: {} },
      audit: { collection: 'audit_events', fields: 'actorId targetId kind at data.from data.to data.freezeUntil' },
      complaints: { collection: 'complaints', fields: 'userId orderId subject status createdAt updatedAt' },
      plans: { collection: 'billing_plans', fields: 'name amount currency active' },
      subscriptions: { collection: 'subscriptions', fields: 'sellerId shopId userId planId status createdAt expiresAt' },
      notifications: { collection: 'notifications', fields: 'userId title orderId read telegramState adminRead createdAt' },
};

@Controller('admin')
export class Admin {
  constructor(private readonly database: Database, private readonly auth: Auth) {}
  private get db() { return this.database.db; }
  private async actor(token: string | undefined) {
    const user = await this.auth.current(token);
    if (!isAdminAccount(user)) throw new ForbiddenException('Administrator access required');
    return user._id;
  }
  private async audit(actorId: string, kind: string, targetId: string, data: Input, session: ClientSession) {
    await this.db.collection('audit_events').insertOne({
      actorId, targetId, kind, data, at: new Date(),
    }, { session });
  }

  @Get('overview')
  async overview(@Headers('authorization') token: string | undefined) {
    const actor = await this.actor(token);
    const now = new Date();
    const since = new Date(now.getTime() - 30 * 86400000);
    const users = this.db.collection('users');
    const active = {
      disabled: { $ne: true }, frozen: { $ne: true },
      $and: [{ $or: [{ status: 'active' }, { status: '' }, { status: { $exists: false } }] },
        { $or: [{ freezeUntil: { $lte: now } }, { freezeUntil: { $exists: false } }] }],
    };
    const [customers, sellers, newAccounts, activeAccounts, shops, products, orders] = await Promise.all([
      users.countDocuments({ role: 'customer' }), users.countDocuments({ role: 'seller' }),
      users.countDocuments({ createdAt: { $gte: since } }),
      users.countDocuments({ role: { $in: ['customer', 'seller'] }, ...active }),
      this.db.collection('markets').countDocuments(), this.db.collection('products').countDocuments({ deletedAt: { $exists: false } }),
      this.db.collection('orders').countDocuments(),
    ]);
    const prefs = await this.db.collection<{ _id: string; notifications?: boolean }>('admin_preferences').findOne({ _id: actor });
    const unreadNotifications = prefs?.notifications === false ? null :
      await this.db.collection('notifications').countDocuments({ adminRead: { $ne: true } });
    return { customers, sellers, newAccounts, activeAccounts, shops, products, orders, unreadNotifications };
  }

  @Get('records/:kind')
  async records(@Headers('authorization') token: string | undefined, @Param('kind') kind: string,
    @Query('page') pageValue = '1', @Query('buyer') buyer: string | undefined = undefined, @Query() query: Record<string, unknown> = {}) {
    await this.actor(token);
    if (typeof pageValue !== 'string' || !/^\d{1,5}$/.test(pageValue) || Number(pageValue) < 1 ||
        (buyer !== undefined && (typeof buyer !== 'string' || buyer.length > 80))) throw new BadRequestException('Invalid page');
    const page = Number(pageValue);
    if (!Object.hasOwn(sections, kind)) throw new NotFoundException('Section not found');
    const { collection, fields } = sections[kind];
    const filter: Document = { ...sections[kind].filter, ...(kind === 'orders' && buyer && { customerId: buyer }) };
    if (kind === 'applications' && !query.status) filter.status = 'pending';
    for (const [key, field] of Object.entries({ status: 'status', shop: 'shopId', seller: 'sellerId', customer: 'customerId', target: 'targetId', user: 'userId', kind: 'kind' })) {
      const value = query[key];
      if (value === undefined || value === '' || value === 'all') continue;
      if (typeof value !== 'string' || value.length > 120) throw new BadRequestException('Invalid filter');
      if (fields.split(' ').includes(field)) filter[field] = value;
    }
    if (query.unread === '1' && kind === 'notifications') filter.adminRead = { $ne: true };
    if (typeof query.q === 'string' && query.q.trim()) {
      if (query.q.length > 120) throw new BadRequestException('Invalid search');
      const term = query.q.trim();
      const searchFields = ['title', 'name', 'number', 'subject', 'kind', 'targetId', 'userId'].filter(field => fields.split(' ').includes(field));
      if (searchFields.length) filter.$or = searchFields.map(field => ({ [field]: { $regex: RegExp.escape(term), $options: 'i' } }));
    }
    if (kind === 'products' && typeof query.shop === 'string' && query.shop) {
      const shop = await this.db.collection<Row>('markets').findOne({ _id: query.shop }, { projection: { sellerId: 1 } });
      if (!shop || (filter.sellerId && filter.sellerId !== shop.sellerId)) return { items: [], total: 0, page, more: false };
      filter.sellerId = shop.sellerId;
    }
    const table = this.db.collection(collection);
    const [rows, total] = await Promise.all([
      table.find(filter, { projection: projection(fields) }).sort({ _id: -1 }).skip((page - 1) * pageSize).limit(pageSize).toArray(),
      table.countDocuments(filter),
    ]);
    const items = rows.map(({ _id, ...row }) => {
      const id = String(_id);
      if (collection === 'orders') return { ...row, id, number: row.number ?? 'BU-' + id, items: row.items ?? [] };
      return { ...row, id, ...(collection === 'users' && { uid: id }) };
    });
    return { items, total, page, more: page * pageSize < total };
  }

  @Get('records/:kind/:id')
  async record(@Headers('authorization') token: string | undefined, @Param('kind') kind: string, @Param('id') id: string) {
    await this.actor(token);
    if (!Object.hasOwn(sections, kind)) throw new NotFoundException('Section not found');
    const { collection, fields, filter } = sections[kind];
    const select = projection(fields);
    const table = this.db.collection<{ _id: string | ObjectId }>(collection);
    let doc = await table.findOne({ ...filter, _id: id }, { projection: select });
    if (!doc && kind === 'subscriptions' && /^[a-f0-9]{24}$/i.test(id)) {
      doc = await table.findOne({ _id: new ObjectId(id) }, { projection: select });
    }
    if (!doc) throw new NotFoundException('Record not found');
    const { _id, ...data } = doc;
    return { ...data, id: String(_id) };
  }

  @Get('settings')
  async settings(@Headers('authorization') token: string | undefined) {
    const actor = await this.actor(token);
    const row = await this.db.collection<{ _id: string; name?: string; email?: string; site?: string; notifications?: boolean }>('admin_preferences').findOne({ _id: actor });
    return { name: row?.name ?? '', email: row?.email ?? '', site: row?.site ?? '', notifications: row?.notifications ?? true };
  }

  @Put('settings')
  async updateSettings(@Headers('authorization') token: string | undefined, @Body() input: Input) {
    const actor = await this.actor(token);
    const { name, email, site, notifications } = input ?? {};
    if (typeof name !== 'string' || name.trim().length > 120 || typeof site !== 'string' || site.trim().length > 200 ||
        typeof email !== 'string' || email.length > 254 || (email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) ||
        typeof notifications !== 'boolean') throw new BadRequestException('Invalid preferences');
    const values = { name: name.trim(), email: email.trim(), site: site.trim(), notifications };
    await this.db.collection<{ _id: string }>('admin_preferences').updateOne({ _id: actor }, { $set: values }, { upsert: true });
    return values;
  }

  @Patch('notifications/read')
  async readNotifications(@Headers('authorization') token: string | undefined, @Body('id') id: unknown) {
    await this.actor(token);
    const table = this.db.collection<{ _id: string | ObjectId }>('notifications');
    if (id === undefined) {
      const result = await table.updateMany({ adminRead: { $ne: true } }, { $set: { adminRead: true } });
      return { updated: result.modifiedCount };
    }
    if (typeof id !== 'string' || !id || id.length > 120) throw new BadRequestException('Invalid notification');
    const match = await table.findOne({ _id: id }, { projection: { _id: 1 } });
    const result = await table.updateOne({ _id: match?._id ?? (/^[a-f0-9]{24}$/i.test(id) ? new ObjectId(id) : id) }, { $set: { adminRead: true } });
    if (!result.matchedCount) throw new NotFoundException('Notification not found');
    return { updated: result.modifiedCount };
  }

  @Get('telegram')
  async telegram(@Headers('authorization') token: string | undefined) {
    await this.actor(token);
    const [botStarted, deliveries] = await Promise.all([
      this.db.collection('telegram_bindings').countDocuments({ botStarted: true, blocked: { $ne: true } }),
      this.db.collection('telegram_order_outbox').aggregate([
        { $group: { _id: '$deliveryState', total: { $sum: 1 } } }, { $sort: { _id: 1 } },
      ]).toArray(),
    ]);
    return { botStarted, deliveries: deliveries.map(row => ({ state: row._id, total: row.total })) };
  }

  @Get('users/:id')
  async user(@Headers('authorization') token: string | undefined, @Param('id') uid: string) {
    await this.actor(token);
    const account = await this.db.collection<Row>('users').findOne({ _id: uid }, { projection: projection(userFields) });
    if (!account) throw new NotFoundException('Account not found');
    const { _id, ...data } = account;
    const [placedOrders, incomingOrders, history] = await Promise.all([
      this.db.collection('orders').countDocuments({ customerId: uid }),
      this.db.collection('orders').countDocuments({ sellerId: uid }),
      this.db.collection('audit_events').find({ targetId: uid }, { projection: projection('actorId kind at data.from data.to data.freezeUntil') }).sort({ at: -1 }).limit(100).toArray(),
    ]);
    return { uid, ...data, placedOrders, incomingOrders, history };
  }

  @Patch('users/:id/status')
  async userStatus(@Headers('authorization') token: string | undefined, @Param('id') targetId: string, @Body() input: Input) {
    const actorId = await this.actor(token);
    const action = input?.action;
    if (actorId === targetId || (action !== 'freeze' && action !== 'block' && action !== 'unfreeze')) throw new BadRequestException('Invalid moderation action');
    const freezeUntil = action === 'freeze' && typeof input.freezeUntil === 'string' ? new Date(input.freezeUntil) : null;
    if (action === 'freeze' && (!freezeUntil || !Number.isFinite(freezeUntil.getTime()) || freezeUntil <= new Date())) {
      throw new BadRequestException('Freeze end must be in the future');
    }
    return this.database.transaction(async session => {
      const users = this.db.collection<Row>('users');
      const account = await users.findOne({ _id: targetId }, { session });
      if (!account) throw new NotFoundException('Account not found');
      const now = new Date();
      const updates = action === 'block' ? { $set: { disabled: true, status: 'blocked', updatedAt: now } } :
        action === 'freeze' ? { $set: { freezeUntil, updatedAt: now } } :
        { $set: { frozen: false, disabled: false, status: 'active', updatedAt: now }, $unset: { freezeUntil: '' as const } };
      await users.updateOne({ _id: targetId }, updates, { session });
      await this.audit(actorId, 'user.' + action, targetId, { ...(freezeUntil && { freezeUntil }) }, session);
      return { uid: targetId, action, freezeUntil };
    });
  }

  @Get('markets')
  async markets(@Headers('authorization') token: string | undefined, @Query('after') after = '') {
    await this.actor(token);
    if (typeof after !== 'string' || after.length > 80) throw new BadRequestException('Invalid cursor');
    const rows = await this.db.collection<Row>('markets').find(after ? { _id: { $gt: after } } : {},
      { projection: projection(marketFields) }).sort({ _id: 1 }).limit(51).toArray();
    return { items: rows.slice(0, 50).map(({ _id, ...row }) => ({ id: _id, ...row })), next: rows.length > 50 ? rows[49]._id : '' };
  }

  @Patch('markets/:id/status')
  async marketStatus(@Headers('authorization') token: string | undefined, @Param('id') marketId: string, @Body() body: Input) {
    const actor = await this.actor(token);
    const { status, expectedStatus, reason = '' } = body ?? {};
    const vocabulary = ['draft', 'pending', 'approved', 'rejected', 'suspended'];
    if (typeof status !== 'string' || !vocabulary.includes(status) || typeof expectedStatus !== 'string' ||
        !vocabulary.includes(expectedStatus) || typeof reason !== 'string' || reason.length > 500) throw new BadRequestException('Invalid market status');
    return this.database.transaction(async session => {
      const row = await this.db.collection<Row>('markets').findOneAndUpdate({ _id: marketId, status: expectedStatus },
        { $set: { status, rejectionReason: reason.trim(), reviewedAt: new Date(), updatedAt: new Date() } },
        { session, projection: projection(marketFields), returnDocument: 'after' });
      if (!row) throw new ConflictException('Market status changed');
      await this.audit(actor, 'market.status', marketId, { from: expectedStatus, to: status }, session);
      const { _id, ...data } = row;
      return { id: _id, ...data };
    });
  }

  @Patch('products/:id/status')
  async productStatus(@Headers('authorization') token: string | undefined, @Param('id') productId: string, @Body() body: Input) {
    const actor = await this.actor(token);
    const status = body?.status;
    const expected = body?.expectedStatus;
    if (typeof status !== 'string' || typeof expected !== 'string' || !['approved', 'hidden'].includes(status) || !['approved', 'hidden'].includes(expected)) {
      throw new BadRequestException('Invalid moderation status');
    }
    return this.database.transaction(async session => {
      const products = this.db.collection<Row>('products');
      const filter = expected === 'approved' ? { moderationStatus: { $ne: 'hidden' } } : { moderationStatus: 'hidden' };
      const now = new Date();
      const res = await products.updateOne({ _id: productId, deletedAt: { $exists: false }, ...filter },
        { $set: { moderationStatus: status, moderatedAt: now, updatedAt: now } }, { session });
      if (!res.matchedCount) throw new ConflictException('Product status changed');
      await this.audit(actor, 'product.status', productId, { from: expected, to: status }, session);
      return { id: productId, status };
    });
  }

  @Patch('complaints/:id/status')
  async complaintStatus(@Headers('authorization') token: string | undefined, @Param('id') id: string, @Body('status') status: unknown) {
    const actor = await this.actor(token);
    if (!/^[a-f0-9]{64}$/.test(id) || (status !== 'closed' && status !== 'reviewing')) throw new BadRequestException('Invalid complaint status');
    return this.database.transaction(async session => {
      const complaints = this.db.collection<Row>('complaints');
      const previous = await complaints.findOneAndUpdate({ _id: id, status: status === 'reviewing' ? 'open' : { $in: ['open', 'reviewing'] } },
        { $set: { status, updatedAt: new Date() } }, { session });
      if (!previous) throw new ConflictException('Complaint status changed');
      await this.audit(actor, 'complaint.status', id, { from: previous.status, to: status }, session);
      return { id, status };
    });
  }

  @Get('sales')
  async sales(@Headers('authorization') token: string | undefined, @Query('period') period = 'week', @Query('date') date?: string) {
    await this.actor(token);
    return salesReport(this.database, period, date);
  }
}
