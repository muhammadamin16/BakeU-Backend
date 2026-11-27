import { Auth, AuthController } from './auth.js';
import { Controller, Get, Module, ServiceUnavailableException } from '@nestjs/common';
import { CatalogController } from './catalog.js';
import { Database } from './database.js';
import { MediaController } from './media.js';

@Controller()
class HealthController {
  constructor(private readonly database: Database) {}

  @Get('health')
  health() { return { status: 'ok' }; }

  @Get('readyz')
  async ready() {
    try {
      await this.database.db.command({ ping: 1 });
    } catch {
      throw new ServiceUnavailableException('MongoDB unavailable');
    }
    return { status: 'ready' };
  }
}

@Module({ controllers: [HealthController, CatalogController, MediaController, AuthController], providers: [Database, Auth] })
export class AppModule {}
