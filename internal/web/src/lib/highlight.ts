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
  bash: () => import('shiki/dist/langs/bash.mjs'),
  css: () => import('shiki/dist/langs/css.mjs'),
  diff: () => import('shiki/dist/langs/diff.mjs'),
  dockerfile: () => import('shiki/dist/langs/dockerfile.mjs'),
  go: () => import('shiki/dist/langs/go.mjs'),
  hcl: () => import('shiki/dist/langs/hcl.mjs'),
  html: () => import('shiki/dist/langs/html.mjs'),
  javascript: () => import('shiki/dist/langs/javascript.mjs'),
  json: () => import('shiki/dist/langs/json.mjs'),
  jsx: () => import('shiki/dist/langs/jsx.mjs'),
  markdown: () => import('shiki/dist/langs/markdown.mjs'),
  python: () => import('shiki/dist/langs/python.mjs'),
  rust: () => import('shiki/dist/langs/rust.mjs'),
  sql: () => import('shiki/dist/langs/sql.mjs'),
  svelte: () => import('shiki/dist/langs/svelte.mjs'),
  toml: () => import('shiki/dist/langs/toml.mjs'),
  tsx: () => import('shiki/dist/langs/tsx.mjs'),
  typescript: () => import('shiki/dist/langs/typescript.mjs'),
  yaml: () => import('shiki/dist/langs/yaml.mjs'),
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
