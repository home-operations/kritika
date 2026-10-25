import starlight from "@astrojs/starlight";
import { defineConfig } from "astro/config";
import starlightLinksValidator from "starlight-links-validator";

export default defineConfig({
  site: "https://kritika.home-operations.com",
  // The repository's site/, which the shared docs-build action publishes.
  outDir: "../site",
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
      tableOfContents: { maxHeadingLevel: 6 },
      // Shiki has no PromQL grammar.
      expressiveCode: { shiki: { langAlias: { promql: "txt" } } },
      editLink: {
        baseUrl: "https://github.com/home-operations/kritika/edit/main/docs/",
      },
      sidebar: [
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
      ],
      plugins: [starlightLinksValidator()],
    }),
  ],
});
