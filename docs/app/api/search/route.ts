import { createFromSource } from "fumadocs-core/search/server";
import { source } from "../../../lib/source";

// staticGET, not GET: `output: "export"` has no server to run a search endpoint,
// so the index is written out at build time and queried in the browser.
export const revalidate = false;

const server = createFromSource(source);
export const GET = server.staticGET;
