import { readdirSync } from "node:fs";

import { satteri } from "@astrojs/markdown-satteri";
import starlight from "@astrojs/starlight";
import { defineConfig } from "astro/config";
import starlightLinksValidator from "starlight-links-validator";

import { markdownLinks } from "./src/markdown-links.mjs";

const sidebar = [
  { label: "Start here", link: "/" },
  { label: "Setup", slug: "setup" },
  {
    label: "Using kritika",
    items: [
      { label: "Reviews and commands", slug: "reviews" },
      { label: "Dashboard", slug: "dashboard" },
      { label: "MCP endpoint", slug: "mcp" },
      { label: "Repository settings", slug: "repository-config" },
      { label: "Confidence and approvals", slug: "confidence" },
    ],
  },
  {
    label: "Operations",
    items: [
      { label: "Database", slug: "database" },
      { label: "Security", slug: "security" },
      { label: "Monitoring", slug: "metrics" },
    ],
  },
  {
    label: "Reference",
    items: [
      { label: "Configuration", slug: "configuration" },
      { label: "Git forges", slug: "forges" },
      { label: "Models and providers", slug: "models" },
      { label: "Helm values", slug: "chart-values" },
    ],
  },
  { label: "Development", slug: "development" },
];

// A page left out of the sidebar still builds, with nothing leading to it.
const listed = new Set(
  sidebar.flatMap((entry) => entry.items ?? [entry]).map((entry) => entry.slug ?? "index"),
);
const unlisted = readdirSync(new URL("./src/content/docs/", import.meta.url))
  .map((file) => file.replace(/\.md$/, ""))
  .filter((slug) => !listed.has(slug));
if (unlisted.length > 0) {
  throw new Error(`docs pages missing from the sidebar: ${unlisted.join(", ")}`);
}

export default defineConfig({
  site: "https://kritika.home-operations.com",
  // The repository's site/, which the shared docs-build action publishes.
  outDir: "../site",
  markdown: {
    // Straight quotes, as the pages are written: prose quotes values to copy.
    processor: satteri({
      mdastPlugins: [markdownLinks],
      features: { smartPunctuation: false },
    }),
  },
  integrations: [
    starlight({
      title: "kritika",
      description: "Repository-aware AI pull request review for GitHub, Forgejo, GitLab and Gitea.",
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/home-operations/kritika",
        },
      ],
      // Shiki has no PromQL grammar.
      expressiveCode: { shiki: { langAlias: { promql: "txt" } } },
      editLink: {
        baseUrl: "https://github.com/home-operations/kritika/edit/main/docs/",
      },
      sidebar,
      plugins: [starlightLinksValidator()],
    }),
  ],
});
