import {
  BadRequestException, Body, Controller, Delete, ForbiddenException, Get, Headers, HttpCode,
  Injectable, NotFoundException, OnModuleDestroy, Param, Post, Put, Query,
} from '@nestjs/common';
import { createHash } from 'node:crypto';
import type { Server as HttpServer } from 'node:http';
import { ObjectId } from 'mongodb';
import { Server } from 'socket.io';
import { Auth } from './auth.js';
import { Database } from './database.js';

type Chat = { _id: string; shopId: string; sellerId: string; buyerId: string; updatedAt: Date; sequence: number };
type Message = { _id: ObjectId; chatId: string; senderId: string; text: string; createdAt: Date; sequence: number };
type Read = { _id: string; sequence: number };
type Follow = { _id: string; userId: string; shopId: string; at: Date };
const stringId = (id: string) => /^[\w-]{1,80}$/.test(id);
const key = (...parts: string[]) => createHash('sha256').update(JSON.stringify(parts)).digest('hex');
const exposed = ({ _id, ...fields }: Message) => ({ id: _id.toHexString(), ...fields });

@Injectable()
export class Community implements OnModuleDestroy {
  private io?: Server;
  constructor(private readonly database: Database, private readonly auth: Auth) {}
  private get db() { return this.database.db; }
  private get chats() { return this.db.collection<Chat>('chats'); }
  private get messages() { return this.db.collection<Message>('chat_messages'); }
  private get reads() { return this.db.collection<Read>('chat_reads'); }
  private get follows() { return this.db.collection<Follow>('market_follows'); }

  attach(server: HttpServer) {
    const io = new Server(server, { path: '/api/chat/socket', serveClient: false, maxHttpBufferSize: 4096,
      allowRequest: (request, callback) => {
        const origin = request.headers.origin;
        const allowed = (process.env.AUTH_BROWSER_ORIGINS ?? '').split(',').map(value => value.trim());
        callback(null, !origin || allowed.includes(origin));
      },
    });
    this.io = io;
    io.use(async (socket, next) => {
      try {
        const token = socket.handshake.auth.token;
        if (typeof token !== 'string') throw new ForbiddenException();
        const user = await this.auth.current('Bearer ' + token);
        socket.data.userId = user._id;
        next();
      } catch { next(new Error('Unauthorized')); }
    });
    io.on('connection', socket => { void socket.join('user:' + socket.data.userId); });
  }

  onModuleDestroy() {
    this.io?.disconnectSockets(true);
    this.io?.engine.close();
  }

  private async notify(chat: Chat, message: Message) {
    const io = this.io;
    if (!io) return;
    const sockets = await io.in(['user:' + chat.buyerId, 'user:' + chat.sellerId]).fetchSockets();
    await Promise.allSettled(sockets.map(async socket => {
      try {
        const user = await this.auth.current('Bearer ' + socket.handshake.auth.token);
        if (user._id !== chat.buyerId && user._id !== chat.sellerId) return;
        socket.emit('chat:message', exposed(message));
      } catch { socket.disconnect(true); }
    }));
  }

  async user(token: string | undefined) { return this.auth.current(token); }

  private async owned(chatId: string, userId: string) {
    const chat = await this.chats.findOne({ _id: chatId, $or: [{ buyerId: userId }, { sellerId: userId }] });
    if (!chat) throw new NotFoundException('Chat not found');
    return chat;
  }

  async start(userId: string, shopId: string) {
    if (typeof shopId !== 'string' || !stringId(shopId)) throw new BadRequestException('Invalid shop');
    const market = await this.db.collection<{ _id: string; sellerId: string }>('markets').findOne({ _id: shopId, status: 'approved' });
    if (!market || market.sellerId === userId) throw new NotFoundException('Shop not found');
    const chat = await this.chats.findOneAndUpdate({ _id: key(shopId, userId, market.sellerId) },
      { $setOnInsert: { buyerId: userId, shopId, sellerId: market.sellerId, updatedAt: new Date(), sequence: 0 } },
      { upsert: true, returnDocument: 'after' });
    return this.chatView(chat!, userId);
  }

  private async chatView(chat: Chat, userId: string) {
    const [{ name } = {}, marker, buyer] = await Promise.all([
      this.db.collection<{ _id: string; name?: string }>('markets').findOne({ _id: chat.shopId }).then(value => value ?? undefined),
      this.reads.findOne({ _id: key(chat._id, userId) }),
      this.db.collection<{ _id: string; name?: string }>('users').findOne({ _id: chat.buyerId }, { projection: { name: 1 } }),
    ]);
    const unread = await this.messages.countDocuments({ chatId: chat._id, senderId: { $ne: userId }, sequence: { $gt: marker?.sequence ?? 0 } });
    return { id: chat._id, shopId: chat.shopId, shopName: name, buyerName: buyer?.name, buyerId: chat.buyerId, sellerId: chat.sellerId, updatedAt: chat.updatedAt, unread };
  }

  async list(userId: string) {
    const items = await this.chats.find({ $or: [{ buyerId: userId }, { sellerId: userId }] }).sort({ updatedAt: -1 }).limit(100).toArray();
    return { items: await Promise.all(items.map(chat => this.chatView(chat, userId))) };
  }

  async history(chatId: string, userId: string, before?: string) {
    await this.owned(chatId, userId);
    if (before !== undefined && (typeof before !== 'string' || !/^\d{1,15}$/.test(before))) throw new BadRequestException('Invalid cursor');
    const messages = await this.messages.find({ chatId, ...(before !== undefined && { sequence: { $lt: Number(before) } }) })
      .sort({ sequence: -1 }).limit(51).toArray();
    return { items: messages.slice(0, 50).reverse().map(exposed), next: messages.length > 50 ? String(messages[49].sequence) : '' };
  }

  async send(chatId: string, userId: string, input: unknown) {
    if (typeof input !== 'string' || !input.trim() || input.length > 2000) throw new BadRequestException('Invalid message');
    const text = input.trim();
    const { chat, message } = await this.database.transaction(async session => {
      const createdAt = new Date();
      const chat = await this.chats.findOneAndUpdate({ _id: chatId, $or: [{ buyerId: userId }, { sellerId: userId }] },
        { $inc: { sequence: 1 }, $set: { updatedAt: createdAt } }, { session, returnDocument: 'after' });
      if (!chat) throw new NotFoundException('Chat not found');
      const message = { _id: new ObjectId(), chatId, senderId: userId, text, createdAt, sequence: chat.sequence };
      await this.messages.insertOne(message, { session });
      return { chat, message };
    });
    await this.notify(chat, message).catch(() => {});
    return exposed(message);
  }

  async read(chatId: string, userId: string, through: unknown) {
    const chat = await this.owned(chatId, userId);
    if (typeof through !== 'number' || !Number.isSafeInteger(through) || through < 0 || through > chat.sequence) {
      throw new BadRequestException('Invalid read marker');
    }
    await this.reads.updateOne({ _id: key(chatId, userId) }, { $max: { sequence: through } }, { upsert: true });
  }

  async follow(userId: string | null, shopId: string, change?: boolean) {
    if (!stringId(shopId)) throw new BadRequestException('Invalid shop');
    const market = await this.db.collection<{ _id: string; sellerId: string }>('markets').findOne({ _id: shopId, status: 'approved' });
    if (!market) throw new NotFoundException('Shop not found');
    const id = userId && key(userId, shopId);
    if (id && userId && change === true) {
      if (market.sellerId === userId) throw new ForbiddenException('Cannot follow your own shop');
      await this.follows.updateOne({ _id: id }, { $setOnInsert: { userId, shopId, at: new Date() } }, { upsert: true });
    }
    if (id && change === false) await this.follows.deleteOne({ _id: id });
    const [subscribers, entry] = await Promise.all([this.follows.countDocuments({ shopId }), id && this.follows.findOne({ _id: id })]);
    return { followers: subscribers, following: !!entry };
  }
}

@Controller()
export class CommunityController {
  constructor(private readonly service: Community, private readonly database: Database) {}

  @Get('chats')
  async list(@Headers('authorization') token: string | undefined) {
    return this.service.list((await this.service.user(token))._id);
  }

  @Post('chats')
  async start(@Headers('authorization') token: string | undefined, @Body('shopId') shopId: string) {
    return this.service.start((await this.service.user(token))._id, shopId);
  }

  @Get('chats/:id/messages')
  async history(@Headers('authorization') token: string | undefined, @Param('id') id: string, @Query('before') before?: string) {
    return this.service.history(id, (await this.service.user(token))._id, before);
  }

  @Post('chats/:id/messages')
  async send(@Headers('authorization') token: string | undefined, @Param('id') id: string, @Body('text') text: unknown) {
    return this.service.send(id, (await this.service.user(token))._id, text);
  }

  @Put('chats/:id/read')
  @HttpCode(204)
  async read(@Headers('authorization') token: string | undefined, @Param('id') id: string, @Body('through') through: unknown) {
    await this.service.read(id, (await this.service.user(token))._id, through);
  }

  @Get('markets/:id/follow')
  async follows(@Headers('authorization') token: string | undefined, @Param('id') id: string) {
    return this.service.follow(token ? (await this.service.user(token))._id : null, id);
  }

  @Put('markets/:id/follow')
  async follow(@Headers('authorization') token: string | undefined, @Param('id') id: string) {
    return this.service.follow((await this.service.user(token))._id, id, true);
  }

  @Delete('markets/:id/follow')
  async unfollow(@Headers('authorization') token: string | undefined, @Param('id') id: string) {
    return this.service.follow((await this.service.user(token))._id, id, false);
  }

  @Get('products/:id/reviews')
  async reviews(@Param('id') productId: string) {
    const items = await this.database.db.collection('reviews').find({ productId }).sort({ createdAt: -1 }).limit(100).toArray();
    return items.map(({ _id, rating, text, comment, authorName, createdAt }) => ({ id: String(_id), rating, comment: comment ?? text, authorName, createdAt }));
  }

  @Post('products/:id/reviews')
  async review(@Headers('authorization') token: string | undefined, @Param('id') productId: string, @Body() body: Record<string, unknown>) {
    const user = await this.service.user(token);
    const { rating, comment } = body ?? {};
    if (!stringId(productId) || typeof rating !== 'number' || !Number.isInteger(rating) || rating < 1 || rating > 5 ||
        typeof comment !== 'string' || comment.length > 2000) throw new BadRequestException('Invalid review');
    const db = this.database.db;
    return this.database.transaction(async session => {
      const purchase = await db.collection('orders').findOne({ customerId: user._id, 'items.productId': productId,
        $or: [{ status: 'completed' }, { status: 'delivered', workflowVersion: { $ne: 2 } }] }, { session });
      if (!purchase) throw new ForbiddenException('A completed purchase is required');
      const id = key(user._id, productId);
      const result = await db.collection<{ _id: string }>('reviews').updateOne({ _id: id },
        { $setOnInsert: { productId, customerId: user._id, rating, comment: comment.trim(),
          authorName: user.firstName ?? user.name, createdAt: new Date() } }, { session, upsert: true });
      if (!result.upsertedCount) throw new BadRequestException('Product has already been reviewed');
      const [summary] = await db.collection('reviews').aggregate([
        { $match: { productId } }, { $group: { _id: null, ratingAvg: { $avg: '$rating' }, ratingCount: { $sum: 1 } } },
      ], { session }).toArray();
      await db.collection<{ _id: string }>('products').updateOne({ _id: productId },
        { $set: { ratingAvg: summary.ratingAvg, ratingCount: summary.ratingCount } }, { session });
      return { id };
    });
  }
}
