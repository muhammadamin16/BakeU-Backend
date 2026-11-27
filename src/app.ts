import { Admin } from './admin.js';
import { Analytics } from './analytics.js';
import { Community, CommunityController } from './community.js';
import { Commerce } from './commerce.js';
import { SellerProducts } from './seller-products.js';
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
      const server = await this.database.db.command({ hello: 1 });
      if (process.env.TELEGRAM_BOT_USERNAME && !(server.isWritablePrimary && (server.setName || server.msg === 'isdbgrid'))) {
        throw new Error('Transactional MongoDB required');
      }
    } catch {
      throw new ServiceUnavailableException('MongoDB unavailable');
    }
    return { status: 'ready' };
  }
}

@Module({ controllers: [HealthController, CatalogController, MediaController, AuthController, SellerProducts, Commerce, CommunityController, Analytics, Admin], providers: [Database, Auth, Community] })
export class AppModule {}
