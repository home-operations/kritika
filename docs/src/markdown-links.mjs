import path from "node:path";
import { fileURLToPath } from "node:url";

const contentDir = fileURLToPath(new URL("./content/docs/", import.meta.url));

// Pages link to each other as relative .md files, which GitHub follows when
// it renders them; the site serves each page at /<slug>/ instead.
export function markdownLinks({ fileURL }) {
  if (!fileURL) return;
  const file = fileURLToPath(fileURL);
  if (!file.startsWith(contentDir)) return;
  return {
    name: "markdown-links",
    link(node, ctx) {
      const match = /^([^#?:]+)\.md(#.*)?$/.exec(node.url);
      if (!match || node.url.startsWith("/")) return;
      const slug = path
        .relative(contentDir, path.resolve(path.dirname(file), match[1]))
        .replace(/(^|\/)index$/, "");
      ctx.setProperty(node, "url", `/${slug ? `${slug}/` : ""}${match[2] ?? ""}`);
    },
  };
}
