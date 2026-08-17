import path from "node:path";
import { fileURLToPath } from "node:url";
import { createMDX } from "fumadocs-mdx/next";

const withMDX = createMDX({
  configPath: "source.config.ts",
});

const root = path.dirname(fileURLToPath(import.meta.url));

// Static export, like www — the docs are a second Next app on the same GitHub
// Pages site, copied into www/out/docs by .github/workflows/pages.yml.
//
// basePath carries the whole prefix (/tennis/docs) rather than just /tennis,
// so this app owns its own /_next and cannot collide with the marketing site's
// assets when the two builds are merged into one artifact. That is why the
// pages live at the app root and Fumadocs' baseUrl is "/" — the "docs" segment
// is in basePath already, and nesting it twice would give /tennis/docs/docs.
/** @type {import("next").NextConfig} */
const nextConfig = {
  output: "export",
  basePath: "/tennis/docs",
  reactStrictMode: true,
  outputFileTracingRoot: root,
};

export default withMDX(nextConfig);
