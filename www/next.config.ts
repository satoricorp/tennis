import type { NextConfig } from "next";

// Static export to GitHub Pages. The site is a project page, so it is served
// from satoricorp.github.io/tennis and every asset needs that prefix —
// assetPrefix is derived from basePath, so setting it here is enough.
const nextConfig: NextConfig = {
  output: "export",
  basePath: "/tennis",
};

export default nextConfig;
