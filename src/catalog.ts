import { BadRequestException, Controller, Get, NotFoundException, Param, Query } from '@nestjs/common';
import { createHash } from 'node:crypto';
import type { Filter } from 'mongodb';
import { Database } from './database.js';

interface Listing {
  _id: string;
  sellerId: string;
}
interface Market extends Listing {
  name: string;
  description?: string;
  city?: string;
  area?: string;
  address?: string;
  latitude?: number;
  longitude?: number;
  logoURL?: string;
  fulfillment?: string;
  status: string;
}
export interface Product extends Listing {
  title: string;
  description?: string;
  category?: string;
  price: number;
  available: boolean;
  imageURLs?: string[];
  variants?: { id?: string; name: string; price: number }[];
  ratingAvg?: number;
  ratingCount?: number;
  deletedAt?: Date;
  draft?: boolean;
  revision?: number;
  priorPublicImages?: string[];
  unit?: string;
  weight?: string;
  portions?: string;
  ingredients?: string;
  allergens?: string;
  createdAt?: Date;
  updatedAt?: Date;
}

function pagination(query: Record<string, unknown>, legacyArray = false) {
  const after = query.after ?? '';
  const raw = query.limit ?? '50';
  if (typeof after !== 'string' || after.length > 128 || typeof raw !== 'string' ||
      !/^\d{1,3}$/.test(raw) || Number(raw) < 1 || Number(raw) > 100) {
    throw new BadRequestException('Invalid pagination');
  }
  return { after, limit: Number(raw), paged: !legacyArray || query.limit !== undefined || after !== '' };
}

function page<T extends Listing, V>(docs: T[], limit: number, serialize: (doc: T) => V) {
  return { items: docs.slice(0, limit).map(serialize), next: docs.length > limit ? docs[limit - 1]._id : '' };
}

function publicMarket(doc: Market) {
  const { _id, sellerId, name, description, city, area, address, latitude, longitude, logoURL, fulfillment } = doc;
  return { id: _id, sellerId, name, description, city, area, address, lat: latitude, lng: longitude, logoURL, fulfillment };
}

export function publicProduct(doc: Product) {
  const { _id, sellerId, title, description, category, price, available, imageURLs, variants,
    ratingAvg, ratingCount, unit, weight, portions, ingredients, allergens, createdAt, updatedAt } = doc;
  return { id: _id, sellerId, title, description, category, price, available, imageURLs: imageURLs ?? [],
    variants: variants?.map(({ id, name, price }) => ({
      id: id || createHash('sha256').update(_id + '\0' + name).digest('hex').slice(0, 24), name, price,
    })),
    ratingAvg, ratingCount, unit, weight, portions, ingredients, allergens, createdAt, updatedAt };
}

const activeProduct = { moderationStatus: { $ne: "hidden" }, deletedAt: { $exists: false }, draft: { $ne: true } };

@Controller()
export class CatalogController {
  constructor(private readonly database: Database) {}

  private get markets() { return this.database.db.collection<Market>('markets'); }
  private get products() { return this.database.db.collection<Product>('products'); }

  @Get('markets')
  async listMarkets(@Query() query: Record<string, unknown>) {
    const { after, limit } = pagination(query);
    const filter: Filter<Market> = { status: 'approved', ...(after && { _id: { $gt: after } }) };
    return page(await this.markets.find(filter).sort({ _id: 1 }).limit(limit + 1).toArray(), limit, publicMarket);
  }

  @Get('markets/:id')
  async market(@Param('id') id: string) {
    const doc = await this.markets.findOne({ _id: id, status: 'approved' });
    if (!doc) throw new NotFoundException('Market not found');
    return publicMarket(doc);
  }

  @Get('products')
  async listProducts(@Query() query: Record<string, unknown>) {
    const { after, limit, paged } = pagination(query, true);
    for (const field of ['category', 'seller', 'q']) {
      const value = query[field];
      if (value !== undefined && (typeof value !== 'string' || value.length > 128)) {
        throw new BadRequestException('Invalid catalog filter');
      }
    }
    const sellers = await this.markets.distinct('sellerId', {
      status: 'approved', ...(typeof query.seller === 'string' && query.seller && { sellerId: query.seller }),
    });
    const filter: Filter<Product> = {
      ...activeProduct, sellerId: { $in: sellers },
      ...(after && { _id: { $gt: after } }),
      ...(typeof query.category === 'string' && query.category && { category: query.category }),
      ...(typeof query.q === 'string' && query.q && { title: new RegExp(RegExp.escape(query.q), 'i') }),
    };
    const cursor = this.products.find(filter).sort({ _id: 1 });
    if (paged) return page(await cursor.limit(limit + 1).toArray(), limit, publicProduct);
    return (await cursor.toArray()).map(publicProduct);
  }

  @Get('products/:id')
  async product(@Param('id') id: string) {
    const doc = await this.products.findOne({ _id: id, ...activeProduct });
    if (!doc || !await this.markets.findOne({ sellerId: doc.sellerId, status: 'approved' })) {
      throw new NotFoundException('Product not found');
    }
    return publicProduct(doc);
  }
}
