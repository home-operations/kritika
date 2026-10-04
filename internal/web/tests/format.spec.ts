import { test, expect } from '@playwright/test';
import { duration, usd } from '../src/lib/format';

test.describe('duration', () => {
  for (const [ms, want] of [
    [null, ''],
    [450, '450ms'],
    [1500, '1.5s'],
    [45_000, '45s'],
    [60_000, '1m'],
    [90_000, '1m 30s'],
    [3_600_000, '1h'],
    [5_400_000, '1h 30m'],
    [129_600_000, '36h'],
  ] as const) {
    test(`${ms} is ${want || 'empty'}`, () => {
      expect(duration(ms)).toBe(want);
    });
  }
});

test.describe('usd', () => {
  for (const [n, want] of [
    [0, '$0'],
    [0.0004, '$0.0004'],
    [0.1, '$0.10'],
    [12.5, '$12.50'],
    [1234.56, '$1,234.56'],
    [1234567, '$1,234,567.00'],
  ] as const) {
    test(`${n} is ${want}`, () => {
      expect(usd(n)).toBe(want);
    });
  }
});
