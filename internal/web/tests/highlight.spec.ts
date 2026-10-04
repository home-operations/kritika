// Syntax highlighting (src/lib/highlight.ts): plain functions, called
// directly, with no browser.
import { test, expect } from '@playwright/test';
import { highlight, languageOf } from '../src/lib/highlight';

test('highlight: languageOf() reads a name, an alias, an extension or a Dockerfile', () => {
  const cases: [string | undefined, string | undefined][] = [
    ['go', 'go'],
    ['golang', 'go'],
    ['internal/web/src/main.ts', 'typescript'],
    ['charts/kritika/values.yml', 'yaml'],
    ['Dockerfile', 'dockerfile'],
    ['build/Dockerfile.tools', 'dockerfile'],
    ['result.json', 'json'],
    ['replacement', undefined],
    ['runner log', undefined],
    ['LICENSE', undefined],
    ['', undefined],
    [undefined, undefined],
  ];
  for (const [name, want] of cases) expect(languageOf(name), String(name)).toBe(want);
});

test('highlight: code comes back as lines of tokens whose text is the code, in the palette\'s colours', async () => {
  const code = 'package a\n\n// f does nothing.\nfunc f() string { return "x" }';
  const lines = await highlight(code, 'go');
  expect(lines?.map((l) => l.map((t) => t.text).join('')).join('\n')).toBe(code);
  const tokens = lines!.flat();
  const colorOf = (text: string) => tokens.find((t) => t.text.trim() === text)?.color;
  expect(colorOf('func')).toBe('var(--code-token-keyword)');
  expect(colorOf('"x"')).toMatch(/^var\(--code-token-string/);
  expect(tokens.find((t) => t.text.includes('f does nothing'))?.color).toBe('var(--code-token-comment)');
  for (const t of tokens) if (t.color) expect(t.color).toMatch(/^var\(--code-[a-z-]+\)$/);
});

test('highlight: a language it does not know, or code too long, is left plain', async () => {
  expect(await highlight('x', undefined)).toBeUndefined();
  expect(await highlight('x', 'cobol')).toBeUndefined();
  expect(await highlight('a'.repeat(200_001), 'go')).toBeUndefined();
});
