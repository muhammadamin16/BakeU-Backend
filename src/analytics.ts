import { BadRequestException, Controller, ForbiddenException, Get, Headers, Query } from '@nestjs/common';
import type { Document } from 'mongodb';
import { Auth } from './auth.js';
import { Database } from './database.js';

const day = 86400000;
const offset = 5 * 3600000;
export const activeStatuses = ['pending', 'accepted', 'preparing', 'ready', 'delivering', 'out_for_delivery'];

export function salesBounds(period: string, input?: string) {
  const today = new Date(Date.now() + offset).toISOString().slice(0, 10);
  const value = input ?? today;
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value)) throw new BadRequestException('Invalid date');
  let since = new Date(value + 'T00:00:00Z');
  if (!Number.isFinite(since.getTime()) || since.toISOString().slice(0, 10) !== value) throw new BadRequestException('Invalid date');
  let until: Date;
  switch (period) {
    case 'day':
      until = new Date(since.getTime() + day);
      break;
    case 'week':
      since = new Date(since.getTime() - ((since.getUTCDay() + 6) % 7) * day);
      until = new Date(since.getTime() + 7 * day);
      break;
    case 'month':
      since.setUTCDate(1);
      until = new Date(since);
      until.setUTCMonth(until.getUTCMonth() + 1);
      break;
    case 'year':
      since.setUTCMonth(0, 1);
      until = new Date(since);
      until.setUTCFullYear(until.getUTCFullYear() + 1);
      break;
    default:
      throw new BadRequestException('Invalid period');
  }
  return { from: since.toISOString().slice(0, 10), through: new Date(until.getTime() - day).toISOString().slice(0, 10),
    start: new Date(since.getTime() - offset), end: new Date(until.getTime() - offset) };
}

export function salesPipeline(start: Date, end: Date, scope: Document = {}): Document[] {
  return [
    { $match: { ...scope, $or: [{ status: 'completed' }, { status: 'delivered', workflowVersion: { $ne: 2 } }] } },
    { $set: { soldAt: { $ifNull: [{ $arrayElemAt: [{ $map: {
      input: { $filter: { input: { $ifNull: ['$history', []] }, as: 'event', cond: { $eq: ['$$event.status', 'completed'] } } },
      as: 'completed', in: '$$completed.at',
    } }, -1] }, { $ifNull: ['$updatedAt', '$createdAt'] }] } } },
    { $match: { soldAt: { $gte: start, $lt: end } } },
    { $unwind: '$items' },
    { $set: { revenueMinor: { $multiply: [
      { $round: [{ $multiply: [{ $toDecimal: '$items.price' }, 100] }, 0] }, '$items.qty',
    ] }, day: { $dateToString: { date: '$soldAt', format: '%Y-%m-%d', timezone: 'Asia/Tashkent' } },
    weekday: { $isoDayOfWeek: { date: '$soldAt', timezone: 'Asia/Tashkent' } } } },
  ];
}

const amount = (minor: unknown) => Number(String(minor ?? '0')) / 100;
export async function salesReport(database: Database, period: string, date?: string, scope: Document = {}) {
  const { from, through, start, end } = salesBounds(period, date);
  const [result] = await database.db.collection('orders').aggregate([
    ...salesPipeline(start, end, scope),
    { $facet: {
      rows: [
        { $group: { _id: { day: '$day', productId: '$items.productId' }, title: { $last: '$items.title' },
          quantity: { $sum: '$items.qty' }, minor: { $sum: '$revenueMinor' } } },
        { $sort: { '_id.day': -1, '_id.productId': 1 } }, { $limit: 500 },
      ],
      totals: [{ $group: { _id: null, quantity: { $sum: '$items.qty' }, minor: { $sum: '$revenueMinor' } } }],
      products: [
        { $group: { _id: '$items.productId', title: { $last: '$items.title' }, quantity: { $sum: '$items.qty' }, minor: { $sum: '$revenueMinor' } } },
        { $sort: { minor: -1, _id: 1 } }, { $limit: 10 },
      ],
      markets: [
        { $group: { _id: '$shopId', title: { $last: '$shopName' }, quantity: { $sum: '$items.qty' }, minor: { $sum: '$revenueMinor' } } },
        { $sort: { minor: -1, _id: 1 } }, { $limit: 10 },
      ],
      weekdays: [{ $group: { _id: '$weekday', quantity: { $sum: '$items.qty' }, minor: { $sum: '$revenueMinor' } } }, { $sort: { _id: 1 } }],
    } },
  ]).toArray();
  const values = ({ quantity, minor }: Document = {}) => ({ quantity: quantity ?? 0, revenue: amount(minor) });
  const totals = result?.totals[0];
  const items = (result?.rows ?? []).map((row: Document) => ({ ...row._id, title: row.title, ...values(row) }));
  const summarize = (rows: Document[]) => rows.map(row => ({ id: row._id, title: row.title, ...values(row) }));
  return { from, through, items, totals: values(totals),
    topProducts: summarize(result?.products ?? []), topMarkets: summarize(result?.markets ?? []), weekdays: summarize(result?.weekdays ?? []) };
}

@Controller()
export class Analytics {
  constructor(private readonly database: Database, private readonly auth: Auth) {}

  @Get('seller/dashboard')
  async dashboard(@Headers('authorization') token: string | undefined, @Query('period') period = 'week') {
    const user = await this.auth.current(token);
    if (user.role !== 'seller') throw new ForbiddenException('Seller account required');
    const sellerId = user._id;
    const [report, active, count] = await Promise.all([
      salesReport(this.database, period, undefined, { sellerId }),
      this.database.db.collection('orders').countDocuments({ sellerId, status: { $in: activeStatuses } }),
      this.database.db.collection('products').countDocuments({ sellerId, deletedAt: { $exists: false }, draft: { $ne: true } }),
    ]);
    return { ...report, revenue: { total: report.totals.revenue }, activeOrders: { total: active }, products: { count } };
  }
}
