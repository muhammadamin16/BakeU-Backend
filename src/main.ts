import 'reflect-metadata';
import { NestFactory } from '@nestjs/core';
import { DocumentBuilder, SwaggerModule } from '@nestjs/swagger';
import { AppModule } from './app.js';

async function bootstrap() {
  const app = await NestFactory.create(AppModule);
  app.setGlobalPrefix('api', { exclude: ['health', 'readyz'] });
  app.enableShutdownHooks();
  const spec = new DocumentBuilder().setTitle('BakeU Marketplace').setVersion('0.1').build();
  SwaggerModule.setup('openapi', app, () => SwaggerModule.createDocument(app, spec));
  await app.listen(Number(process.env.PORT ?? 3000), '0.0.0.0');
}

void bootstrap().catch(() => {
  console.error('Unable to start BakeU API');
  process.exitCode = 1;
});
