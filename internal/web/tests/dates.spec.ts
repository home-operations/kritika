// The dashboard's one way of writing dates and times (src/lib/dates.ts):
// plain functions, called directly, with no browser.
import { test, expect } from '@playwright/test';
import { day, relative, timestamp } from '../src/lib/dates';

const now = Date.parse('2026-09-15T12:00:00Z');
const at = (seconds: number) => new Date(now + seconds * 1000).toISOString();

test.describe('dates: relative()', () => {
  const cases: [string, number, string | RegExp][] = [
    ['a moment ago', -30, 'just now'],
    ['a moment from now', 30, 'just now'],
    ['minutes ago', -5 * 60, '5m ago'],
    ['minutes from now', 10 * 60, 'in 10m'],
    ['hours ago', -3 * 3600, '3h ago'],
    ['hours from now', 2 * 3600, 'in 2h'],
    ['days ago', -6 * 86_400, '6d ago'],
    ['days from now', 2 * 86_400, 'in 2d'],
    // Past a week it is the date, in the viewer's zone: noon UTC is the
    // 7th or the 8th somewhere.
    ['over a week ago is its date', -7.5 * 86_400, /^Sep [78]$/],
    ['over a week from now is its date', 8 * 86_400, /^Sep 2[34]$/],
    ['another year carries the year', -300 * 86_400, /^Nov 1[89], 2025$/],
  ];
  for (const [name, offset, want] of cases) {
    test(name, () => {
      const got = relative(at(offset), now);
      if (typeof want === 'string') expect(got).toBe(want);
      else expect(got).toMatch(want);
    });
  }
  test('nothing, or something that is no date, is empty', () => {
    expect(relative(null, now)).toBe('');
    expect(relative('soon', now)).toBe('');
  });
});

test('dates: timestamp() is the full moment, 24-hour, with its zone', () => {
  expect(timestamp('2026-09-01T14:05:09Z')).toMatch(/^(Aug 31|Sep [12]), 2026, \d\d:\d\d:09 \S+$/);
  expect(timestamp(undefined)).toBe('');
  expect(timestamp('soon')).toBe('');
});

test('dates: day() is a UTC day, with the year unless short', () => {
  expect(day('2026-09-01')).toBe('Sep 1, 2026');
  expect(day('2026-09-01', true)).toBe('Sep 1');
  expect(day('acme/large')).toBe('acme/large');
});
