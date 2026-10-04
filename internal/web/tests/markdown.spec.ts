// markdown.ts is pure (no runes), so it is tested directly with no browser.
// These pin the safety contract: only http(s) links survive as links, and
// nothing in the source can become markup since the renderer only ever
// interpolates text and a validated href.
import { test, expect } from '@playwright/test';
import { parseInline, parseMarkdown, safeHref, type Inline } from '../src/lib/markdown';

// flat is the text of inlines, whatever they are wrapped in.
function flat(list: Inline[]): string {
  return list.map((t) => (t.kind === 'text' || t.kind === 'code' ? t.text : t.kind === 'break' ? '\n' : flat(t.inlines))).join('');
}
const kinds = (list: Inline[]): string[] => list.flatMap((t) => [t.kind, ...('inlines' in t ? kinds(t.inlines) : [])]);

test.describe('markdown: links', () => {
  for (const href of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,<b>x</b>', 'vbscript:x', '/relative', 'relative/path', '#frag', '//evil.example/x']) {
    test(`${href} is not a link`, () => {
      expect(safeHref(href)).toBeUndefined();
      const out = parseInline(`see [here](${href}) now`);
      expect(kinds(out)).not.toContain('link');
      expect(flat(out)).toContain('[here]');
    });
  }

  test('http and https links survive, normalised', () => {
    expect(parseInline('[a](https://example.com/x) [b](http://example.com)')).toEqual([
      { kind: 'link', href: 'https://example.com/x', inlines: [{ kind: 'text', text: 'a' }] },
      { kind: 'text', text: ' ' },
      { kind: 'link', href: 'http://example.com/', inlines: [{ kind: 'text', text: 'b' }] },
    ]);
  });

  test('a bare URL is a link to itself', () => {
    expect(parseInline('see https://example.com/x')).toEqual([
      { kind: 'text', text: 'see ' },
      { kind: 'link', href: 'https://example.com/x', inlines: [{ kind: 'text', text: 'https://example.com/x' }] },
    ]);
  });

  test('quotes and angle brackets in an href are percent-encoded, not attribute breakers', () => {
    const [link] = parseInline('[x](<https://example.com/"onmouseover="alert(1)<b>>)');
    expect(link?.kind).toBe('link');
    if (link?.kind !== 'link') return;
    expect(link.href).not.toContain('"');
    expect(link.href).not.toContain('<');
  });

  test('an image is a link to it, never loaded, and only over http(s)', () => {
    expect(parseInline('![a diagram](https://example.com/d.png)')).toEqual([
      { kind: 'link', href: 'https://example.com/d.png', inlines: [{ kind: 'text', text: 'a diagram' }] },
    ]);
    expect(kinds(parseInline('![x](javascript:alert(1))'))).toEqual(['text']);
  });
});

test.describe('markdown: blocks and inline', () => {
  test('html in the source stays the text it is, inline or as a block', () => {
    const html = '<img src=x onerror=alert(1)>';
    expect(flat(parseInline(`before ${html} after`))).toBe(`before ${html} after`);
    expect(kinds(parseInline(`before ${html} after`))).toEqual(expect.not.arrayContaining(['link']));
    expect(parseMarkdown('<script>alert(1)</script>\n\nafter')).toEqual([
      { kind: 'para', inlines: [{ kind: 'text', text: '<script>alert(1)</script>' }] },
      { kind: 'para', inlines: [{ kind: 'text', text: 'after' }] },
    ]);
  });

  test('code spans, bold, italic, strikethrough and fenced blocks', () => {
    expect(parseInline('use `x != nil` **now**, *really*, ~~later~~')).toEqual([
      { kind: 'text', text: 'use ' },
      { kind: 'code', text: 'x != nil' },
      { kind: 'text', text: ' ' },
      { kind: 'bold', inlines: [{ kind: 'text', text: 'now' }] },
      { kind: 'text', text: ', ' },
      { kind: 'italic', inlines: [{ kind: 'text', text: 'really' }] },
      { kind: 'text', text: ', ' },
      { kind: 'strike', inlines: [{ kind: 'text', text: 'later' }] },
    ]);
    expect(parseMarkdown('para one\n\n```go\nfunc f() {}\n```\nafter')).toEqual([
      { kind: 'para', inlines: [{ kind: 'text', text: 'para one' }] },
      { kind: 'code', lang: 'go', text: 'func f() {}' },
      { kind: 'para', inlines: [{ kind: 'text', text: 'after' }] },
    ]);
  });

  test('an unterminated fence swallows the rest as code', () => {
    expect(parseMarkdown('```\nx\ny')).toEqual([{ kind: 'code', lang: '', text: 'x\ny' }]);
  });

  test('headings, lists, task items, a quote and a rule', () => {
    expect(parseMarkdown('## Why\n\n1. first\n2. `second`\n\n- [x] done\n- [ ] to do\n\n> quoted\n\n---')).toEqual([
      { kind: 'heading', depth: 2, inlines: [{ kind: 'text', text: 'Why' }] },
      {
        kind: 'list',
        ordered: true,
        start: 1,
        items: [
          { blocks: [{ kind: 'para', inlines: [{ kind: 'text', text: 'first' }] }] },
          { blocks: [{ kind: 'para', inlines: [{ kind: 'code', text: 'second' }] }] },
        ],
      },
      {
        kind: 'list',
        ordered: false,
        start: 1,
        items: [
          { checked: true, blocks: [{ kind: 'para', inlines: [{ kind: 'text', text: 'done' }] }] },
          { checked: false, blocks: [{ kind: 'para', inlines: [{ kind: 'text', text: 'to do' }] }] },
        ],
      },
      { kind: 'quote', blocks: [{ kind: 'para', inlines: [{ kind: 'text', text: 'quoted' }] }] },
      { kind: 'rule' },
    ]);
  });

  test('a table keeps its alignment and its cells their inline marks', () => {
    expect(parseMarkdown('| a | b |\n|---|--:|\n| `1` | 2 |')).toEqual([
      {
        kind: 'table',
        align: [null, 'right'],
        header: [[{ kind: 'text', text: 'a' }], [{ kind: 'text', text: 'b' }]],
        rows: [[[{ kind: 'code', text: '1' }], [{ kind: 'text', text: '2' }]]],
      },
    ]);
  });
});
