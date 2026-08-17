import type { ReactNode } from "react";
import { RootProvider } from "fumadocs-ui/provider/next";
import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { source } from "../lib/source";
import { wordmark } from "./fonts";
import "./global.css";

// Every route in this app is a docs page, so DocsLayout lives here rather than
// in a nested layout. fetch() does not get basePath applied for us, so the
// static search index is named in full; keep it in step with next.config.mjs.
const SEARCH_API = "/tennis/docs/api/search";

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" className={wordmark.variable} suppressHydrationWarning>
      <body>
        <RootProvider
          search={{ options: { type: "static", api: SEARCH_API } }}
        >
          <DocsLayout
            tree={source.getPageTree()}
            nav={{ title: <span className="tennis-nav-brand">Tennis</span>, url: "/" }}
            links={[
              { text: "GitHub", url: "https://github.com/satoricorp/tennis" },
              { text: "Discord", url: "https://discord.gg/JpAggvxJJ" },
            ]}
          >
            {children}
          </DocsLayout>
        </RootProvider>
      </body>
    </html>
  );
}

export const metadata = {
  title: "Tennis",
  description:
    "Local hybrid search. Keyword and semantic in one ranking, one binary, one SQLite file, no server.",
};
