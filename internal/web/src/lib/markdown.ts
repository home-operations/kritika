// Markdown for model-written prose (finding explanations, the review
// take). marked reads it; what it returns is turned here into a small tree
// of our own kinds, which the Markdown component renders through ordinary
// Svelte text interpolation. Every character of API content is escaped:
// there is no HTML path at all. HTML in the source is shown as the text it
// is, a link survives only with an http(s) href, and an image is a link to
// it, since loading one would tell its host who reads the review.
import { lexer, type Token, type Tokens } from 'marked';

export type Inline =
  | { kind: 'text'; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'break' }
  | { kind: 'bold'; inlines: Inline[] }
  | { kind: 'italic'; inlines: Inline[] }
  | { kind: 'strike'; inlines: Inline[] }
  | { kind: 'link'; href: string; inlines: Inline[] };

export interface ListItem {
  // checked is a task item's box; undefined for an ordinary item.
  checked?: boolean;
  blocks: Block[];
}

export type Align = 'left' | 'center' | 'right' | null;

export type Block =
  | { kind: 'para'; inlines: Inline[] }
  | { kind: 'code'; lang: string; text: string }
  | { kind: 'heading'; depth: number; inlines: Inline[] }
  | { kind: 'list'; ordered: boolean; start: number; items: ListItem[] }
  | { kind: 'quote'; blocks: Block[] }
  | { kind: 'table'; align: Align[]; header: Inline[][]; rows: Inline[][][] }
  | { kind: 'rule' };

export function safeHref(href: string): string | undefined {
  try {
    const u = new URL(href);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : undefined;
  } catch {
    return undefined;
  }
}

const text = (s: string): Inline[] => (s ? [{ kind: 'text', text: s }] : []);

function inlines(tokens: Token[] | undefined): Inline[] {
  const out: Inline[] = [];
  for (const t of tokens ?? []) {
    switch (t.type) {
      case 'codespan':
        out.push({ kind: 'code', text: (t as Tokens.Codespan).text });
        break;
      case 'strong':
        out.push({ kind: 'bold', inlines: inlines((t as Tokens.Strong).tokens) });
        break;
      case 'em':
        out.push({ kind: 'italic', inlines: inlines((t as Tokens.Em).tokens) });
        break;
      case 'del':
        out.push({ kind: 'strike', inlines: inlines((t as Tokens.Del).tokens) });
        break;
      case 'br':
        out.push({ kind: 'break' });
        break;
      case 'link':
      case 'image': {
        const l = t as Tokens.Link | Tokens.Image;
        const href = safeHref(l.href);
        // One that is not an http(s) link stays the source it was written as.
        if (!href) out.push(...text(l.raw));
        else out.push({ kind: 'link', href, inlines: l.type === 'link' ? inlines(l.tokens) : text(l.text || href) });
        break;
      }
      case 'checkbox':
        break;
      default: {
        const nested = (t as Tokens.Generic).tokens;
        out.push(...(nested?.length ? inlines(nested) : text('text' in t && typeof t.text === 'string' ? t.text : t.raw)));
      }
    }
  }
  return out;
}

function blocks(tokens: Token[]): Block[] {
  const out: Block[] = [];
  for (const t of tokens) {
    switch (t.type) {
      case 'space':
      case 'def':
      // A task item's box is its item's checked.
      case 'checkbox':
        break;
      case 'code':
        out.push({ kind: 'code', lang: (t as Tokens.Code).lang ?? '', text: (t as Tokens.Code).text });
        break;
      case 'heading':
        out.push({ kind: 'heading', depth: (t as Tokens.Heading).depth, inlines: inlines((t as Tokens.Heading).tokens) });
        break;
      case 'hr':
        out.push({ kind: 'rule' });
        break;
      case 'blockquote':
        out.push({ kind: 'quote', blocks: blocks((t as Tokens.Blockquote).tokens) });
        break;
      case 'list': {
        const l = t as Tokens.List;
        out.push({
          kind: 'list',
          ordered: l.ordered,
          start: l.start || 1,
          items: l.items.map((i) => ({ ...(i.task ? { checked: i.checked === true } : {}), blocks: blocks(i.tokens) })),
        });
        break;
      }
      case 'table': {
        const tb = t as Tokens.Table;
        out.push({
          kind: 'table',
          align: tb.align,
          header: tb.header.map((c) => inlines(c.tokens)),
          rows: tb.rows.map((r) => r.map((c) => inlines(c.tokens))),
        });
        break;
      }
      case 'html':
        out.push({ kind: 'para', inlines: text((t as Tokens.HTML).text.trimEnd()) });
        break;
      default: {
        // A paragraph, a list item's text, or anything marked adds later.
        const nested = (t as Tokens.Generic).tokens;
        const ins = nested?.length ? inlines(nested) : text(t.raw);
        if (ins.length) out.push({ kind: 'para', inlines: ins });
      }
    }
  }
  return out;
}

export function parseInline(src: string): Inline[] {
  const [first] = blocks(lexer(src, { gfm: true }));
  return first && 'inlines' in first ? first.inlines : [];
}

export function parseMarkdown(src: string): Block[] {
  return blocks(lexer(src, { gfm: true }));
}
