import { test, expect } from '@playwright/test';
import { duration } from '../src/lib/format';

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
