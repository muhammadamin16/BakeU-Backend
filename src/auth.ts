import {
  BadRequestException, Body, ConflictException, Controller, ForbiddenException, Get, Header,
  Headers, HttpCode, Injectable, OnModuleInit, Post, Req, Res, ServiceUnavailableException,
  HttpException, UnauthorizedException,
} from '@nestjs/common';
import { createHash, createHmac, randomBytes, randomInt, timingSafeEqual } from 'node:crypto';
import { MongoServerError, ObjectId, type ClientSession } from 'mongodb';
import { Database } from './database.js';

type Request = { headers: Record<string, string | undefined> };
type Response = { setHeader(name: string, value: string): void };
type Input = Record<string, unknown>;
type Role = 'seller' | 'customer';
type User = {
  _id: string; role: Role; name: string; phone: string; firstName?: string; lastName?: string;
  verifiedPhoneE164?: string; phoneVerifiedAt?: Date; status?: string; disabled?: boolean;
  frozen?: boolean; freezeUntil?: Date; createdAt?: Date; address?: string;
};
type MarketDraft = { name: string; address: string; latitude: number; longitude: number };
type Proof = {
  _id: string; purpose: 'login' | 'register'; phone: string; draft?: Partial<User>; market?: MarketDraft;
  expiresAt: Date; attempts: number; actor?: string; codeHash?: string; used?: boolean;
};
type Family = { _id: string; userId: string; current: string; expiresAt: Date; rotations: number; spent?: string[]; revokedAt?: Date };
type Ticket = { _id: string; userId: string; expiresAt: Date };

const digest = (value: string) => createHash('sha256').update(value).digest('hex');
const equal = (a: string, b: string) => timingSafeEqual(Buffer.from(digest(a)), Buffer.from(digest(b)));
const id = () => new ObjectId().toHexString();
const lifetime = 30 * 24 * 3600;
const cookie = '__Host-bakeu-refresh';
const tokenPattern = /^[a-f0-9]{48}$/;
const invalid = () => new UnauthorizedException('Invalid or expired proof or session');
const active = (user: User | null): user is User => !!user && ['seller', 'customer'].includes(user.role) &&
  !user.disabled && !user.frozen && (!user.status || user.status === 'active') &&
  !(user.freezeUntil && user.freezeUntil > new Date());

function normalizePhone(raw: unknown) {
  if (typeof raw !== 'string' || raw.length > 40 || !/^[+\d ()-]+$/.test(raw)) throw new BadRequestException('Invalid phone');
  const digits = raw.replace(/\D/g, '');
  if (!/^[1-9]\d{7,14}$/.test(digits) || (!raw.startsWith('+') && !digits.startsWith('998'))) {
    throw new BadRequestException('Use an international phone number');
  }
  return '+' + digits;
}
function required(value: unknown, limit: number) {
  if (typeof value !== 'string' || !value.trim() || value.trim().length > limit) throw new BadRequestException('Invalid registration fields');
  return value.trim();
}
function coordinate(value: unknown, bound: number) {
  if (typeof value !== 'number' || !Number.isFinite(value) || Math.abs(value) > bound) throw new BadRequestException('Invalid shop coordinates');
  return value;
}
export function isAdminAccount(user: User) {
  return !!user.verifiedPhoneE164 && !!user.phoneVerifiedAt &&
    (process.env.ADMIN_USER_IDS ?? '').split(',').map(id => id.trim()).includes(user._id);
}
export function publicUser(user: User) {
  const { _id: uid, role, name, phone, firstName, lastName, verifiedPhoneE164, phoneVerifiedAt, address, createdAt } = user;
  return { uid, role, name, phone, firstName, lastName, verifiedPhoneE164, phoneVerifiedAt, address, createdAt, capabilities: { admin: isAdminAccount(user) } };
}

@Injectable()
export class Auth implements OnModuleInit {
  constructor(private readonly database: Database) {}

  async onModuleInit() {
    if (!process.env.TELEGRAM_BOT_USERNAME) return;
    await this.users.createIndex({ verifiedPhoneE164: 1 }, {
      name: 'unique_verified_phone', unique: true,
      partialFilterExpression: { verifiedPhoneE164: { $type: 'string', $gt: '' } },
    });
    for (const collection of ['phone_proofs', 'auth_rate_limits', 'telegram_launch_tickets']) {
      await this.db.collection(collection).createIndex({ expiresAt: 1 }, { expireAfterSeconds: 0 });
    }
  }

  private get db() { return this.database.db; }
  private get users() { return this.db.collection<User>('users'); }
  private get sessions() { return this.db.collection<Family>('auth_sessions'); }
  private get proofs() { return this.db.collection<Proof>('phone_proofs'); }
  private get signingKey() {
    const value = process.env.JWT_SECRET;
    if (!value) throw new ServiceUnavailableException('Session signing is unavailable');
    return value;
  }
  private liveFamily(familyId: string) {
    return { _id: familyId, revokedAt: { $exists: false }, expiresAt: { $gt: new Date() } };
  }
  private codeHash(proofId: string, code: string) {
    return createHmac('sha256', this.signingKey).update(`bakeu-phone:${proofId}:${code}`).digest('hex');
  }
  private access(uid: string, sid: string) {
    const encode = (value: Input) => Buffer.from(JSON.stringify(value)).toString('base64url');
    const now = Math.floor(Date.now() / 1000);
    const claims = encode({ sub: uid, sid, iat: now, exp: now + 900 });
    const data = `${encode({ alg: 'HS256', typ: 'JWT' })}.${claims}`;
    return `${data}.${createHmac('sha256', this.signingKey).update(data).digest('base64url')}`;
  }
  async current(authorization: string | undefined) {
    const match = /^Bearer ([\w-]+)\.([\w-]+)\.([\w-]+)$/.exec(authorization ?? '');
    if (!match || match[0].length > 4096) throw invalid();
    const [, header, data, signature] = match;
    const expected = createHmac('sha256', this.signingKey).update(`${header}.${data}`).digest('base64url');
    if (!equal(expected, signature)) throw invalid();
    let claims: Input;
    try {
      if (JSON.parse(Buffer.from(header, 'base64url').toString()).alg !== 'HS256') throw invalid();
      claims = JSON.parse(Buffer.from(data, 'base64url').toString());
    } catch { throw invalid(); }
    const now = Date.now() / 1000;
    if (!claims || typeof claims.sub !== 'string' || !claims.sub || typeof claims.exp !== 'number' ||
        claims.exp <= now || (claims.nbf !== undefined && (typeof claims.nbf !== 'number' || claims.nbf > now))) throw invalid();
    if (claims.sid !== undefined &&
        (typeof claims.sid !== 'string' || !await this.sessions.findOne({ ...this.liveFamily(claims.sid), userId: claims.sub }))) throw invalid();
    const user = await this.users.findOne({ _id: claims.sub });
    if (!active(user)) throw new ForbiddenException('Account is unavailable');
    return user;
  }
  private async rate(key: string, maximum: number) {
    const now = Date.now();
    const slot = Math.floor(now / 3600000);
    const count = await this.db.collection<{ _id: string; count: number }>('auth_rate_limits').findOneAndUpdate(
      { _id: `${key}:${slot}` }, { $inc: { count: 1 }, $setOnInsert: { expiresAt: new Date((slot + 1) * 3600000) } },
      { upsert: true, returnDocument: 'after' },
    );
    if (!count || count.count > maximum) throw new HttpException('Too many requests', 429);
  }
  async start(input: Input) {
    const bot = process.env.TELEGRAM_BOT_USERNAME ?? '';
    if (!process.env.TELEGRAM_BRIDGE_TOKEN || !process.env.JWT_SECRET || !/^[A-Za-z0-9_]{5,32}$/.test(bot)) throw new ServiceUnavailableException('Telegram verification is unavailable');
    const purpose = input?.purpose;
    if (purpose !== 'register' && purpose !== 'login') throw new BadRequestException('Invalid proof purpose');
    const phone = normalizePhone(input.phone);
    let draft: Partial<User> | undefined;
    let market: MarketDraft | undefined;
    if (purpose === 'register') {
      const role = input.role;
      if (role !== 'seller' && role !== 'customer') throw new BadRequestException('Invalid registration role');
      const firstName = required(input.firstName, 80);
      const lastName = required(input.lastName, 80);
      draft = { name: firstName + ' ' + lastName, firstName, lastName, role };
      if (role === 'seller') market = {
        name: required(input.marketName, 150), address: required(input.address, 500),
        latitude: coordinate(input.lat, 90), longitude: coordinate(input.lng, 180),
      };
    }
    await this.rate('phone:' + digest(phone), 5);
    const proofId = randomBytes(24).toString('hex');
    const expiresAt = new Date(Date.now() + 600000);
    await this.proofs.insertOne({ _id: proofId, purpose, phone, expiresAt, attempts: 0, ...(draft && { draft }), ...(market && { market }) });
    return { id: proofId, expiresAt, telegramUrl: `https://t.me/${bot}?start=goauth_${proofId}` };
  }
  authorizeBridge(req: Request) {
    const secret = process.env.TELEGRAM_BRIDGE_TOKEN;
    if (!secret || !equal(req.headers.authorization ?? '', 'Bearer ' + secret)) throw invalid();
  }
  bridgeActor(req: Request) {
    this.authorizeBridge(req);
    const actor = req.headers['x-telegram-user-id'] ?? '';
    if (!/^[1-9]\d{0,19}$/.test(actor)) throw invalid();
    return actor;
  }
  async contact(actor: string, input: Input) {
    const proofId = input?.id;
    if (typeof proofId !== 'string' || !tokenPattern.test(proofId)) throw invalid();
    const rawPhone = input.phone;
    const phone = normalizePhone(typeof rawPhone === 'string' && !rawPhone.startsWith('+') ? '+' + rawPhone : rawPhone);
    await this.rate('actor:' + actor, 20);
    const code = randomInt(100000, 1000000).toString();
    const edited = await this.proofs.updateOne({
      _id: proofId, phone, expiresAt: { $gt: new Date() }, used: { $ne: true },
      $or: [{ actor: { $exists: false } }, { actor }], attempts: { $lt: 5 },
    }, { $set: { actor, codeHash: this.codeHash(proofId, code) } });
    if (!edited.matchedCount) throw invalid();
    return { code };
  }
  private async newFamily(user: User, session: ClientSession) {
    const sid = id();
    const refresh = sid + '.' + randomBytes(32).toString('hex');
    const token = this.access(user._id, sid);
    const expiresAt = new Date(Date.now() + lifetime * 1000);
    await this.sessions.insertOne({ _id: sid, userId: user._id, current: digest(refresh), expiresAt, rotations: 0 }, { session });
    return { token, refresh, expiresAt, user };
  }
  async verify(input: Input) {
    const { id: proofId, code } = input ?? {};
    if (typeof proofId !== 'string' || !tokenPattern.test(proofId) || typeof code !== 'string' || !/^\d{6}$/.test(code)) throw invalid();
    const proof = await this.proofs.findOneAndUpdate(
      { _id: proofId, expiresAt: { $gt: new Date() }, used: { $ne: true }, attempts: { $lt: 5 }, codeHash: { $exists: true } },
      { $inc: { attempts: 1 } }, { returnDocument: 'after' },
    );
    if (!proof?.actor || !proof.codeHash || !equal(proof.codeHash, this.codeHash(proofId, code))) throw invalid();
    try {
      return await this.database.transaction(async (session) => {
        const options = { session };
        const now = new Date();
        const consumed = await this.proofs.updateOne(
          { _id: proofId, codeHash: proof.codeHash, used: { $ne: true }, expiresAt: { $gt: now } },
          { $set: { used: true }, $unset: { codeHash: '', draft: '', market: '' } }, options,
        );
        if (!consumed.matchedCount) throw invalid();
        let user = await this.users.findOne({ verifiedPhoneE164: proof.phone }, options);
        if (proof.purpose === 'register') {
          if (user || await this.users.findOne({ phone: proof.phone, verifiedPhoneE164: { $exists: false } }, options)) {
            throw new ConflictException('Account exists or requires a verified legacy claim');
          }
          const draft = proof.draft;
          if (!draft?.name || !draft.role) throw invalid();
          user = { ...draft, _id: id(), name: draft.name, role: draft.role, phone: proof.phone, verifiedPhoneE164: proof.phone,
            phoneVerifiedAt: now, status: 'active', createdAt: now, address: proof.market?.address ?? '' };
          await this.users.insertOne(user, options);
          await this.db.collection('audit_events').insertOne({
            actorId: user._id, targetId: user._id, kind: 'user.phone_registered', at: now,
          }, options);
          if (proof.market) {
            const marketId = id();
            await this.db.collection<{ _id: string }>('markets').insertOne({
              _id: marketId, ...proof.market, sellerId: user._id, status: 'approved', fulfillment: 'both',
              createdAt: now, updatedAt: now, reviewedAt: now,
            } as { _id: string }, options);
            await this.db.collection('market_events').insertOne({ marketId, actorId: user._id, kind: 'verified_phone_registration', at: now }, options);
          }
        }
        if (!active(user) || !user.phoneVerifiedAt) throw invalid();
        const bindings = this.db.collection<{ _id: string; userId: string }>('telegram_bindings');
        const binding = await bindings.findOne({ _id: proof.actor }, options);
        if (binding && binding.userId !== user._id) throw new ConflictException('Telegram account is already linked');
        await bindings.updateOne({ _id: proof.actor }, { $set: {
          userId: user._id, verifiedPhoneE164: proof.phone, confirmedAt: now, botStarted: true, blocked: false,
        } }, { ...options, upsert: true });
        return this.newFamily(user, session);
      });
    } catch (error) {
      if (error instanceof MongoServerError && error.code === 11000) throw new ConflictException('Account already exists or requires index migration');
      throw error;
    }
  }
  async telegramUser(actor: string) {
    const binding = await this.db.collection<{ _id: string; userId: string }>('telegram_bindings').findOne({
      _id: actor, blocked: { $ne: true }, botStarted: true, confirmedAt: { $exists: true },
    });
    const user = binding && await this.users.findOne({ _id: binding.userId });
    return active(user) && user.phoneVerifiedAt && user.verifiedPhoneE164 ? user : null;
  }
  async launch(actor: string) {
    const user = await this.telegramUser(actor);
    if (!user) return { linked: false };
    const ticket = randomBytes(32).toString('hex');
    await this.db.collection<Ticket>('telegram_launch_tickets').insertOne({
      _id: digest(ticket), userId: user._id, expiresAt: new Date(Date.now() + 60000),
    });
    return { linked: true, ticket, uid: user._id };
  }
  async exchange(input: Input) {
    const value = input?.ticket;
    if (typeof value !== 'string' || !/^[a-f0-9]{64}$/.test(value)) throw invalid();
    return this.database.transaction(async (session) => {
      const ticket = await this.db.collection<Ticket>('telegram_launch_tickets').findOneAndDelete({
        _id: digest(value), expiresAt: { $gt: new Date() },
      }, { session });
      if (!ticket) throw invalid();
      const user = await this.users.findOne({ _id: ticket.userId }, { session });
      if (!active(user) || !user.phoneVerifiedAt) throw invalid();
      return this.newFamily(user, session);
    });
  }
  async editProfile(authorization: string | undefined, input: Input) {
    const user = await this.current(authorization);
    const name = required(input?.name, 160);
    const phone = required(input?.phone, 40);
    if (typeof input?.address !== 'string' || input.address.trim().length > 500) throw new BadRequestException('Invalid address');
    const updated = await this.users.findOneAndUpdate({ _id: user._id },
      { $set: { name, phone, address: input.address.trim() } }, { returnDocument: 'after' });
    if (!active(updated)) throw new ForbiddenException('Account is unavailable');
    return publicUser(updated);
  }

  async rotate(refresh: string) {
    if (!/^[a-f0-9]{24}\.[a-f0-9]{64}$/i.test(refresh)) throw invalid();
    const sid = refresh.slice(0, 24);
    const current = digest(refresh);
    const next = sid + '.' + randomBytes(32).toString('hex');
    const filter = this.liveFamily(sid);
    const family = await this.sessions.findOne({ ...filter, current, rotations: { $lt: 8192 } });
    const user = family && await this.users.findOne({ _id: family.userId });
    if (family && !active(user)) throw new ForbiddenException('Account is unavailable');
    if (family && active(user)) {
      const token = this.access(user._id, sid);
      const changed = await this.sessions.updateOne({ ...filter, current, rotations: { $lt: 8192 } },
        { $set: { current: digest(next) }, $push: { spent: current }, $inc: { rotations: 1 } });
      if (changed.matchedCount) return { token, user, refresh: next, expiresAt: family.expiresAt };
    }
    await this.sessions.updateOne({ ...filter, spent: current }, { $set: { revokedAt: new Date() } });
    throw invalid();
  }
  async revoke(refresh: string) {
    if (!/^[a-f0-9]{24}\.[a-f0-9]{64}$/i.test(refresh)) return;
    const hash = digest(refresh);
    await this.sessions.updateOne({ ...this.liveFamily(refresh.slice(0, 24)), $or: [{ current: hash }, { spent: hash }] },
      { $set: { revokedAt: new Date() } });
  }
}

@Controller()
export class AuthController {
  constructor(private readonly auth: Auth) {}

  private mode(req: Request) {
    const mode = req.headers['x-bakeu-session'];
    if (mode === 'refresh') return mode;
    const origin = req.headers.origin;
    const origins = (process.env.AUTH_BROWSER_ORIGINS ?? '').split(',').map(value => value.trim()).filter(Boolean);
    if (mode !== 'browser' || !origin || !origins.includes(origin)) throw new ForbiddenException('Browser session origin is not allowed');
    return mode;
  }
  private refreshToken(req: Request, mode: string, input: Input) {
    const value = mode === 'browser' ?
      req.headers.cookie?.split(';').map(item => item.trim()).find(item => item.startsWith(cookie + '='))?.slice(cookie.length + 1) :
      input?.refreshToken;
    return typeof value === 'string' ? value : '';
  }
  private respond(res: Response, mode: string, session: { token: string; refresh: string; user: User; expiresAt: Date }) {
    const result = { token: session.token, expiresIn: 900, user: publicUser(session.user) };
    if (mode === 'refresh') return { ...result, refreshToken: session.refresh };
    const maxAge = Math.max(0, Math.floor((session.expiresAt.getTime() - Date.now()) / 1000));
    res.setHeader('Set-Cookie', `${cookie}=${session.refresh}; Max-Age=${maxAge}; Path=/; HttpOnly; Secure; SameSite=Strict`);
    return result;
  }

  @Post('auth/phone/start')
  @Header('Cache-Control', 'no-store')
  start(@Req() req: Request, @Body() input: Input) { this.mode(req); return this.auth.start(input); }

  @Post('auth/phone/verify')
  @Header('Cache-Control', 'no-store')
  @HttpCode(200)
  async verify(@Req() req: Request, @Body() input: Input, @Res({ passthrough: true }) res: Response) {
    const mode = this.mode(req);
    return this.respond(res, mode, await this.auth.verify(input));
  }

  @Post('integrations/telegram/auth/phone/contact')
  @Header('Cache-Control', 'no-store')
  @HttpCode(200)
  contact(@Req() req: Request, @Body() input: Input) { return this.auth.contact(this.auth.bridgeActor(req), input); }

  @Post('integrations/telegram/auth/launch')
  @Header('Cache-Control', 'no-store')
  @HttpCode(200)
  launch(@Req() req: Request) { return this.auth.launch(this.auth.bridgeActor(req)); }

  @Post('auth/phone/telegram')
  @Header('Cache-Control', 'no-store')
  @HttpCode(200)
  async exchange(@Req() req: Request, @Body() input: Input, @Res({ passthrough: true }) res: Response) {
    const mode = this.mode(req);
    return this.respond(res, mode, await this.auth.exchange(input));
  }

  @Post('auth/refresh')
  @Header('Cache-Control', 'no-store')
  @HttpCode(200)
  async refresh(@Req() req: Request, @Body() input: Input, @Res({ passthrough: true }) res: Response) {
    const mode = this.mode(req);
    return this.respond(res, mode, await this.auth.rotate(this.refreshToken(req, mode, input)));
  }

  @Post('auth/logout')
  @Header('Cache-Control', 'no-store')
  @HttpCode(204)
  async logout(@Req() req: Request, @Body() input: Input, @Res({ passthrough: true }) res: Response) {
    const mode = this.mode(req);
    await this.auth.revoke(this.refreshToken(req, mode, input));
    if (mode === 'browser') res.setHeader('Set-Cookie', `${cookie}=; Max-Age=0; Path=/; HttpOnly; Secure; SameSite=Strict`);
  }

  @Post('profile')
  @HttpCode(200)
  profile(@Headers('authorization') token: string | undefined, @Body() input: Input) { return this.auth.editProfile(token, input); }

  @Get('me')
  async me(@Headers('authorization') token: string | undefined) { return publicUser(await this.auth.current(token)); }
}
