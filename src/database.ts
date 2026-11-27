import { Injectable, OnModuleDestroy } from '@nestjs/common';
import { Db, MongoClient } from 'mongodb';

@Injectable()
export class Database implements OnModuleDestroy {
  private readonly client: MongoClient;
  readonly db: Db;

  constructor() {
    const { MONGODB_URI, MONGODB_DB } = process.env;
    if (!MONGODB_URI || !MONGODB_DB) throw new Error('MONGODB_URI and MONGODB_DB are required');
    this.client = new MongoClient(MONGODB_URI, { serverSelectionTimeoutMS: 5000 });
    this.db = this.client.db(MONGODB_DB);
  }

  async onModuleDestroy() {
    await this.client.close();
  }
}
