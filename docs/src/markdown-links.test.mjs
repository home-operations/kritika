import assert from "node:assert/strict";
import { test } from "node:test";

import { markdownLinks } from "./markdown-links.mjs";

const page = new URL("./content/docs/security.md", import.meta.url);

function rewritten(visitor, url, fileURL = page) {
  const plugin = markdownLinks({ fileURL });
  let got = url;
  plugin[visitor](
    { url },
    { setProperty: (_node, key, value) => (got = key === "url" ? value : got) },
  );
  return got;
}

for (const visitor of ["link", "definition"]) {
  test(`${visitor}: rewrites relative .md links to site paths`, () => {
    for (const [url, want] of [
      ["setup.md", "/setup/"],
      ["setup.md#deploy", "/setup/#deploy"],
      ["./setup.md", "/setup/"],
      ["index.md", "/"],
      ["index.md#how-it-works", "/#how-it-works"],
    ]) {
      assert.equal(rewritten(visitor, url), want, url);
    }
  });

  test(`${visitor}: leaves other links alone`, () => {
    for (const url of [
      "https://github.com/home-operations/kritika/blob/main/CHANGELOG.md",
      "/setup/",
      "#name-lookups",
      "setup/",
      "https://kritika.home-operations.com/kritika.schema.json",
    ]) {
      assert.equal(rewritten(visitor, url), url, url);
    }
  });
}

test("skips content without a file or outside the docs collection", () => {
  assert.equal(markdownLinks({ fileURL: undefined }), undefined);
  assert.equal(markdownLinks({ fileURL: new URL("./other.md", import.meta.url) }), undefined);
});
