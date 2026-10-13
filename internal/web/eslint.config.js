import js from '@eslint/js';
import { defineConfig, globalIgnores } from 'eslint/config';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';
import ts from 'typescript-eslint';
import svelteConfig from './svelte.config.js';

export default defineConfig(
  globalIgnores(['dist/', 'dist-mock/', 'playwright-report/', 'test-results/']),
  js.configs.recommended,
  ts.configs.recommended,
  svelte.configs.recommended,
  {
    rules: {
      // Local indexes, request parameters, and event listeners do not need reactive collections.
      'svelte/prefer-svelte-reactivity': 'off',
      'svelte/no-useless-mustaches': ['error', { ignoreStringEscape: true }],
    },
  },
  {
    languageOptions: {
      globals: globals.browser,
      parserOptions: { extraFileExtensions: ['.svelte'] },
    },
  },
  {
    files: ['*.{js,ts}', 'tests/**/*.ts'],
    languageOptions: { globals: globals.node },
  },
  {
    files: ['**/*.svelte', '**/*.svelte.{js,ts}'],
    languageOptions: {
      parserOptions: { parser: ts.parser, svelteConfig },
    },
  },
);
