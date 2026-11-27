const assert = require('node:assert/strict');
const { test } = require('node:test');

test('prices share exact minor-unit limits for listing and checkout', async () => {
  const { priceInMinorUnits } = await import('../dist/seller-products.js');
  assert.equal(priceInMinorUnits(10.29), 1029);
  assert.equal(priceInMinorUnits(1e12), 100000000000000);
  for (const value of [0, -1, '10', Infinity, NaN, 10.001, 1e12 + 1]) {
    assert.throws(() => priceInMinorUnits(value), /Invalid price/);
  }
});

test('reporting uses Tashkent boundaries and rejects invalid dates', async () => {
  const { salesBounds, activeStatuses } = await import('../dist/analytics.js');
  const week = salesBounds('week', '2026-11-27');
  assert.equal(week.from, '2026-11-23');
  assert.equal(week.through, '2026-11-29');
  assert.equal(week.start.toISOString(), '2026-11-22T19:00:00.000Z');
  assert.equal(week.end.toISOString(), '2026-11-29T19:00:00.000Z');
  assert.throws(() => salesBounds('day', '2026-02-30'), /Invalid date/);
  assert.ok(activeStatuses.includes('out_for_delivery'));
});
