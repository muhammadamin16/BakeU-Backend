import { Controller, Get, Header, NotFoundException, Param, StreamableFile } from '@nestjs/common';
import { GridFSBucket, ObjectId } from 'mongodb';
import { Database } from './database.js';

const inlineTypes = new Set(['image/jpeg', 'image/png', 'image/gif', 'image/webp', 'image/avif']);

@Controller('images')
export class MediaController {
  constructor(private readonly database: Database) {}

  @Get(':id')
  @Header('X-Content-Type-Options', 'nosniff')
  async image(@Param('id') id: string) {
    if (!/^[a-f\d]{24}$/i.test(id)) throw new NotFoundException('Image not found');
    const objectId = new ObjectId(id);
    const bucket = new GridFSBucket(this.database.db);
    const file = await bucket.find({ _id: objectId, 'metadata.visibility': { $ne: 'private' } }).next();
    if (!file) throw new NotFoundException('Image not found');
    const mime = file.metadata?.contentType;
    const inline = inlineTypes.has(mime);
    return new StreamableFile(bucket.openDownloadStream(objectId), {
      type: inline ? mime : 'application/octet-stream',
      disposition: inline ? 'inline' : 'attachment',
      length: file.length,
    });
  }
}
