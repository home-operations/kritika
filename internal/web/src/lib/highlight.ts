// Syntax highlighting for the code the dashboard shows, with Shiki. It
// yields tokens, never markup, so highlighted code reaches the page through
// text interpolation like everything else a review wrote. Shiki, its
// JavaScript regex engine (the content security policy admits no
// WebAssembly) and each language load on first use, so a page with no code
// pays for none of it.
import type { HighlighterCore, LanguageRegistration } from 'shiki/core';

type Grammar = { default: LanguageRegistration[] };

// The languages highlighted, by Shiki's name. Anything else stays plain.
const LANGUAGES: Record<string, () => Promise<Grammar>> = {
  bash: () => import('@shikijs/langs/bash'),
  css: () => import('@shikijs/langs/css'),
  diff: () => import('@shikijs/langs/diff'),
  dockerfile: () => import('@shikijs/langs/dockerfile'),
  go: () => import('@shikijs/langs/go'),
  hcl: () => import('@shikijs/langs/hcl'),
  html: () => import('@shikijs/langs/html'),
  javascript: () => import('@shikijs/langs/javascript'),
  json: () => import('@shikijs/langs/json'),
  jsx: () => import('@shikijs/langs/jsx'),
  markdown: () => import('@shikijs/langs/markdown'),
  python: () => import('@shikijs/langs/python'),
  rust: () => import('@shikijs/langs/rust'),
  sql: () => import('@shikijs/langs/sql'),
  svelte: () => import('@shikijs/langs/svelte'),
  toml: () => import('@shikijs/langs/toml'),
  tsx: () => import('@shikijs/langs/tsx'),
  typescript: () => import('@shikijs/langs/typescript'),
  yaml: () => import('@shikijs/langs/yaml'),
};

// Other names a language goes by: a fence's tag, or a file's extension.
const ALIASES: Record<string, string> = {
  cjs: 'javascript',
  golang: 'go',
  htm: 'html',
  js: 'javascript',
  md: 'markdown',
  mjs: 'javascript',
  py: 'python',
  rs: 'rust',
  sh: 'bash',
  shell: 'bash',
  tf: 'hcl',
  ts: 'typescript',
  yml: 'yaml',
  zsh: 'bash',
};

// Past this much text highlighting is not worth its time on the main thread.
const MAX_BYTES = 200_000;

// languageOf is the highlighted language a name gives: a language's name
// or alias, or a path, by its extension or, for a Dockerfile, its name.
// undefined when it is none of them.
export function languageOf(name: string | undefined): string | undefined {
  if (!name) return undefined;
  const base = name.slice(name.lastIndexOf('/') + 1).toLowerCase();
  if (base === 'dockerfile' || base.startsWith('dockerfile.')) return 'dockerfile';
  const key = base.includes('.') ? base.slice(base.lastIndexOf('.') + 1) : base;
  const lang = ALIASES[key] ?? key;
  return lang in LANGUAGES ? lang : undefined;
}

// A run of text in one colour: a CSS colour, a var(--code-…) that app.css
// defines for both themes, or none for the text colour.
export interface Token {
  text: string;
  color?: string;
  italic?: boolean;
}

let core: Promise<HighlighterCore> | undefined;
const loaded = new Map<string, Promise<void>>();

async function highlighter(lang: string): Promise<HighlighterCore> {
  core ??= (async () => {
    const [{ createHighlighterCore, createCssVariablesTheme }, { createJavaScriptRegexEngine }] = await Promise.all([
      import('shiki/core'),
      import('shiki/engine/javascript'),
    ]);
    return createHighlighterCore({
      themes: [createCssVariablesTheme({ name: 'kritika', variablePrefix: '--code-' })],
      langs: [],
      engine: createJavaScriptRegexEngine(),
    });
  })();
  const h = await core;
  let ready = loaded.get(lang);
  if (!ready) {
    ready = LANGUAGES[lang]!().then((g) => h.loadLanguage(g.default));
    loaded.set(lang, ready);
  }
  await ready;
  return h;
}

// highlight splits code into lines of tokens, one array per line of code.
// undefined when the language is not one highlighted, or the code is too
// long: the caller shows it plain.
export async function highlight(code: string, lang: string | undefined): Promise<Token[][] | undefined> {
  if (!lang || !(lang in LANGUAGES) || code.length > MAX_BYTES) return undefined;
  const h = await highlighter(lang);
  const { tokens, fg } = h.codeToTokens(code, { lang, theme: 'kritika' });
  return tokens.map((line) =>
    line.map((t) => ({
      text: t.content,
      color: t.color && t.color !== fg ? t.color : undefined,
      italic: ((t.fontStyle ?? 0) & 1) === 1 || undefined,
    })),
  );
}

// highlightDiff highlights a file's diff lines: an array as long as lines,
// holding each code line's tokens and undefined for a hunk header or a
// note. The new side (context and additions) and the old (context and
// deletions) are each read as one text, so a construct that spans lines
// keeps its colours; a hunk starts mid-file, so its first lines may be read
// out of context. undefined when the language is not one highlighted.
export async function highlightDiff(
  lines: readonly { kind: 'add' | 'del' | 'ctx' | 'hunk' | 'meta'; text: string }[],
  lang: string | undefined,
): Promise<(Token[] | undefined)[] | undefined> {
  const side = (kinds: string[]) => lines.filter((l) => kinds.includes(l.kind)).map((l) => l.text).join('\n');
  const [fresh, old] = await Promise.all([highlight(side(['ctx', 'add']), lang), highlight(side(['ctx', 'del']), lang)]);
  if (!fresh || !old) return undefined;
  let n = 0;
  let o = 0;
  return lines.map((l) => {
    switch (l.kind) {
      case 'add':
        return fresh[n++];
      case 'del':
        return old[o++];
      case 'ctx':
        o++;
        return fresh[n++];
      default:
        return undefined;
    }
  });
}
