import {
  BadRequestException, Body, ConflictException, Controller, Delete, ForbiddenException, Get, Headers,
  Header, HttpCode, StreamableFile, NotFoundException, Param, Post, Put, UploadedFiles, UseInterceptors,
} from '@nestjs/common';
import { FilesInterceptor } from '@nestjs/platform-express';
import { GridFSBucket, ObjectId, type Filter, type UpdateFilter } from 'mongodb';
import sharp from 'sharp';
import { Auth } from './auth.js';
import { publicProduct, type Product } from './catalog.js';
import { Database } from './database.js';

type Input = Record<string, unknown>;
const live = { deletedAt: { $exists: false } };
const maxImages = 5;
const maxFileSize = 1900 * 1024;

export function priceInMinorUnits(price: unknown) {
  if (typeof price !== 'number' || !Number.isFinite(price) || price <= 0 || price > 1e12 ||
      !/^\d+(\.\d{1,2})?$/.test(String(price))) throw new BadRequestException('Invalid price');
  return Math.round(price * 100);
}

function text(value: unknown, max: number) {
  if (typeof value !== 'string' || value.trim().length > max) throw new BadRequestException('Invalid product field');
  return value.trim();
}
function view(product: Product) {
  return { ...publicProduct(product), draft: product.draft === true, revision: product.revision ?? 0 };
}

@Controller()
export class SellerProducts {
  constructor(private readonly database: Database, private readonly auth: Auth) {}

  private get products() { return this.database.db.collection<Product>('products'); }
  private async seller(token: string | undefined) {
    const user = await this.auth.current(token);
    const shop = await this.database.db.collection('markets').findOne({ sellerId: user._id, status: 'approved' });
    if (user.role !== 'seller' || !shop) throw new ForbiddenException('An approved shop is required');
    return user._id;
  }

  private async owned(productId: string, sellerId: string) {
    const product = await this.products.findOne({ _id: productId, sellerId, ...live });
    if (!product) throw new NotFoundException('Product not found');
    return product;
  }

  private async update(product: Product, changes: UpdateFilter<Product>) {
    const filter: Filter<Product> = { _id: product._id, sellerId: product.sellerId, ...live,
      revision: product.revision ?? { $exists: false } };
    const next = await this.products.findOneAndUpdate(filter, { ...changes, $inc: { revision: 1 } }, { returnDocument: 'after' });
    if (!next) throw new ConflictException('Product was changed. Reload it and retry.');
    return view(next);
  }

  private fields(input: Input, previous?: Product): Partial<Product> {
    if (!input || typeof input !== 'object' || Array.isArray(input)) throw new BadRequestException('Invalid product');
    const changes: Partial<Product> = {};
    if (!previous || input.title !== undefined) {
      changes.title = text(input.title, 150);
      if (!changes.title) throw new BadRequestException('Title is required');
    }
    if (!previous || input.price !== undefined) changes.price = priceInMinorUnits(input.price) / 100;
    for (const key of ['description', 'category', 'unit', 'weight', 'portions', 'ingredients', 'allergens'] as const) {
      if (input[key] !== undefined) changes[key] = text(input[key], key === 'description' ? 4000 : 1000);
    }
    if (input.available !== undefined) {
      if (typeof input.available !== 'boolean') throw new BadRequestException('Invalid availability');
      changes.available = input.available;
    }
    if (input.variants !== undefined) {
      if (!Array.isArray(input.variants) || input.variants.length > 20) throw new BadRequestException('Invalid variants');
      const oldIds = new Set(previous && publicProduct(previous).variants?.map(item => item.id));
      const ids = new Set<string>();
      const names = new Set<string>();
      changes.variants = input.variants.map((entry: Input) => {
        const name = text(entry?.name, 100);
        const key = name.toLowerCase();
        const variantId = entry?.id ?? new ObjectId().toHexString();
        if (!name || names.has(key) || typeof variantId !== 'string' || ids.has(variantId) ||
            (entry.id !== undefined && !oldIds.has(variantId))) throw new BadRequestException('Invalid variant');
        names.add(key);
        ids.add(variantId);
        return { id: variantId, name, price: priceInMinorUnits(entry.price) / 100 };
      });
    }
    return changes;
  }

  @Get('market/mine')
  async market(@Headers('authorization') token: string | undefined) {
    const user = await this.auth.current(token);
    const shop = await this.database.db.collection('markets').findOne({ sellerId: user._id });
    if (!shop || user.role !== 'seller') throw new NotFoundException('Market not found');
    const { _id, sellerId, name, description, city, area, address, latitude, longitude,
      fulfillment, logoURL, status, rejectionReason } = shop;
    return { id: String(_id), sellerId, name, description, city, area, address,
      lat: latitude, lng: longitude, fulfillment, logoURL, status, rejectionReason };
  }

  @Get('me/products/:id/images/:imageId')
  @Header('Cache-Control', 'no-store')
  @Header('X-Content-Type-Options', 'nosniff')
  async preview(@Headers('authorization') token: string | undefined, @Param('id') productId: string,
    @Param('imageId') imageId: string) {
    const product = await this.owned(productId, await this.seller(token));
    if (!/^[a-f0-9]{24}$/.test(imageId) || !product.imageURLs?.includes('/api/images/' + imageId)) {
      throw new NotFoundException('Image not found');
    }
    const bucket = new GridFSBucket(this.database.db);
    const objectId = new ObjectId(imageId);
    const file = await bucket.find({ _id: objectId, 'metadata.productId': productId, 'metadata.visibility': 'product' }).next();
    if (!file) throw new NotFoundException('Image not found');
    return new StreamableFile(bucket.openDownloadStream(objectId), { type: 'image/webp', length: file.length });
  }

  @Get('me/products')
  async list(@Headers('authorization') token: string | undefined) {
    const sellerId = await this.seller(token);
    return (await this.products.find({ sellerId, ...live }).sort({ _id: -1 }).limit(500).toArray()).map(view);
  }

  @Post('products')
  async create(@Headers('authorization') token: string | undefined, @Body() input: Input) {
    const sellerId = await this.seller(token);
    const fields = this.fields(input);
    const now = new Date();
    const product = { available: true, ...fields, _id: new ObjectId().toHexString(), sellerId, draft: true,
      title: fields.title!, price: fields.price!, imageURLs: [], revision: 0, createdAt: now, updatedAt: now };
    await this.products.insertOne(product);
    return view(product);
  }

  @Put('products/:id')
  async edit(@Headers('authorization') token: string | undefined, @Param('id') productId: string, @Body() input: Input) {
    const product = await this.owned(productId, await this.seller(token));
    const changes = this.fields(input, product);
    if (input.revision !== undefined && input.revision !== (product.revision ?? 0)) {
      throw new ConflictException('Product was changed. Reload it and retry.');
    }
    if (input.imageURLs !== undefined) {
      const oldImages = product.imageURLs ?? [];
      if (!Array.isArray(input.imageURLs) || input.imageURLs.length > maxImages ||
          !input.imageURLs.every(url => typeof url === 'string' && oldImages.includes(url)) ||
          new Set(input.imageURLs).size !== input.imageURLs.length) throw new BadRequestException('Invalid image order');
      changes.imageURLs = input.imageURLs;
    }
    const images = changes.imageURLs ?? product.imageURLs ?? [];
    if (input.publish === true) changes.draft = false;
    if ((product.draft === true && changes.draft === false && images.length < 2) ||
        (product.draft !== true && changes.imageURLs && images.length < Math.min(2, product.imageURLs?.length ?? 0))) {
      throw new BadRequestException('At least two photos are required');
    }
    const removed = product.draft !== true ? (product.imageURLs ?? []).filter(url => !images.includes(url)) : [];
    return this.update(product, { $set: { ...changes, updatedAt: new Date() },
      ...(removed.length && { $addToSet: { priorPublicImages: { $each: removed } } }) });
  }

  @Delete('products/:id')
  @HttpCode(204)
  async archive(@Headers('authorization') token: string | undefined, @Param('id') productId: string) {
    const product = await this.owned(productId, await this.seller(token));
    await this.update(product, { $set: { deletedAt: new Date(), updatedAt: new Date(), available: false } });
  }

  @Post('products/:id/image')
  @UseInterceptors(FilesInterceptor('image', 3, { limits: { fileSize: maxFileSize, fields: 1, fieldSize: 200, parts: 4 } }))
  async upload(@Headers('authorization') token: string | undefined, @Param('id') productId: string,
    @UploadedFiles() files: Express.Multer.File[], @Body('replace') replace?: string) {
    const product = await this.owned(productId, await this.seller(token));
    const oldImages = product.imageURLs ?? [];
    if (!files?.length || (replace !== undefined && (files.length !== 1 || !oldImages.includes(replace))) ||
        oldImages.length + files.length - Number(replace !== undefined) > maxImages) {
      throw new BadRequestException('Invalid photo count or replacement');
    }
    const images = await Promise.all(files.map(async file => {
      try {
        const decoded = sharp(file.buffer, { limitInputPixels: 16_000_000, failOn: 'warning' });
        const { format } = await decoded.metadata();
        const mime = ({ jpeg: 'image/jpeg', png: 'image/png', webp: 'image/webp' } as Record<string, string>)[format ?? ''];
        if (!mime || file.mimetype !== mime) throw new Error('Invalid format');
        const image = await decoded.rotate().webp({ quality: 88 }).toBuffer();
        if (image.length > maxFileSize) throw new Error('Image too large');
        return image;
      } catch {
        throw new BadRequestException('Use a valid JPEG, PNG or WebP photo');
      }
    }));
    if (images.some((image, index) => images.slice(0, index).some(previous => previous.equals(image)))) {
      throw new BadRequestException('Choose different photos');
    }

    const bucket = new GridFSBucket(this.database.db);
    const ids: ObjectId[] = [];
    let attaching = false;
    try {
      for (const photo of images) {
        const stream = bucket.openUploadStream('product.webp', { metadata: {
          contentType: 'image/webp', visibility: 'product', productId,
        } });
        ids.push(stream.id);
        await new Promise<void>((resolve, reject) => stream.once('finish', resolve).once('error', reject).end(photo));
      }
      const urls = ids.map(objectId => '/api/images/' + objectId.toHexString());
      const imageURLs = replace ? oldImages.map(url => url === replace ? urls[0] : url) : [...oldImages, ...urls];
      attaching = true;
      return await this.update(product, { $set: { imageURLs, updatedAt: new Date() },
        ...(replace && product.draft !== true && { $addToSet: { priorPublicImages: replace } }) });
    } catch (error) {
      // An ambiguous network error can occur after MongoDB committed the attachment.
      // In that case retain the private files for an operator to reconcile.
      if (!attaching || error instanceof ConflictException) await Promise.allSettled(ids.map(objectId => bucket.delete(objectId)));
      throw error;
    }
  }
}
