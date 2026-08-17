/**
 * Emit machine-readable docs from content/docs.
 *
 * Juice serves these from Next route handlers; this site is a static export, so
 * there is no server to run them and the files are written at build time
 * instead. Page order and titles come from meta.json and the frontmatter, so
 * adding a page to meta.json is all it takes to appear here.
 *
 *   node scripts/export-llms.mjs              after `next build` — writes into out/
 *   node scripts/export-llms.mjs --tree-only  refresh the copies kept in git
 */
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const contentDir = join(root, "content/docs");
const outDir = join(root, "out");
const ORIGIN = "https://satoricorp.github.io/tennis/docs";

const treeOnly = process.argv.includes("--tree-only");

/** Frontmatter is a flat `key: "value"` map here; no need for a YAML parser. */
function splitFrontmatter(raw) {
  if (!raw.startsWith("---")) return { meta: {}, body: raw };
  const end = raw.indexOf("\n---", 3);
  if (end === -1) return { meta: {}, body: raw };

  const meta = {};
  for (const line of raw.slice(4, end).split("\n")) {
    const match = /^(\w+):\s*"?(.*?)"?\s*$/.exec(line);
    if (match) meta[match[1]] = match[2];
  }
  return { meta, body: raw.slice(end + 4).replace(/^\s*\n/, "") };
}

/** Drop MDX/JSX chrome agents do not need; keep headings, code, lists, links.
 *  Do not strip arbitrary `<…>` — placeholders like `<name>` are intentional. */
function toAgentMarkdown(body) {
  return body
    .replace(
      /<Callout\s+type="[^"]*"\s+title="([^"]*)">([\s\S]*?)<\/Callout>/g,
      (_m, title, inner) =>
        `> **${title.trim()}:** ${inner.trim().replace(/\n+/g, " ")}`
    )
    .replace(/<Cards>([\s\S]*?)<\/Cards>/g, (_m, inner) =>
      [
        ...inner.matchAll(
          /<Card\s+title="([^"]*)"\s+href="([^"]*)"\s+description="([^"]*)"\s*\/>/g
        )
      ]
        .map(([, title, href, description]) => `- [${title}](${href}): ${description}`)
        .join("\n")
    )
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

const order = JSON.parse(await readFile(join(contentDir, "meta.json"), "utf8")).pages;
const known = new Set(
  (await readdir(contentDir))
    .filter((f) => f.endsWith(".mdx"))
    .map((f) => f.replace(/\.mdx$/, ""))
);

// A page in content/ but not in meta.json is invisible in the sidebar; failing
// here is better than shipping an llms.txt that disagrees with the site.
const missing = [...known].filter((slug) => !order.includes(slug));
if (missing.length > 0) {
  throw new Error(`content/docs has pages missing from meta.json: ${missing.join(", ")}`);
}

const pages = [];
for (const slug of order) {
  const raw = await readFile(join(contentDir, `${slug}.mdx`), "utf8");
  const { meta, body } = splitFrontmatter(raw);
  pages.push({
    slug,
    url: slug === "index" ? "/" : `/${slug}`,
    file: slug === "index" ? "index.md" : `${slug}.md`,
    title: meta.title ?? slug,
    description: meta.description ?? "",
    markdown: toAgentMarkdown(body)
  });
}

const index = [
  "# Tennis",
  "",
  "> Local hybrid search. Keyword and semantic in one ranking, one binary, one SQLite file, no server.",
  "",
  "## Docs",
  "",
  ...pages.map((p) => `- [${p.title}](${ORIGIN}${p.url}): ${p.description}`),
  "",
  "## Machine-readable",
  "",
  `- [llms-full.txt](${ORIGIN}/llms-full.txt): Full docs in one file`,
  `- Append \`.md\` to any docs URL for Markdown (e.g. ${ORIGIN}/cli.md)`,
  ""
].join("\n");

const full = pages
  .map((p) => `# ${p.title} (${p.url})\n\n${p.markdown}\n`)
  .join("\n");

async function emit(dir) {
  await mkdir(dir, { recursive: true });
  await writeFile(join(dir, "llms.txt"), index);
  await writeFile(join(dir, "llms-full.txt"), full);
}

// The git tree keeps llms.txt and llms-full.txt so they are reviewable in a
// diff; out/ additionally gets the per-page .md, which only exists once built.
await emit(root);
if (!treeOnly) {
  await emit(outDir);
  for (const page of pages) {
    await writeFile(
      join(outDir, page.file),
      `# ${page.title} (${page.url})\n\n${page.markdown}\n`
    );
  }
}

console.log(
  treeOnly
    ? `Wrote llms.txt and llms-full.txt (${pages.length} pages)`
    : `Wrote llms.txt, llms-full.txt and ${pages.length} .md pages into out/`
);
