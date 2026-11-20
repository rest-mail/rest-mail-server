// Whether the five customer guides are published where a customer can find them
// (#2120 AC02): in the bundle the website takes, and on the site itself.
//
//   node published.mjs [--site https://rest-mail.com] [--registry ghcr.io/rest-mail/email-explained]
//
// The repository publishes the bundle (email-explained.yml); the website takes it
// by itself and deploys it. Each step can lag the one before it, so this asks
// each: the newest bundle, and the live site. It reads, and changes nothing.
import { execFileSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parseArgs } from "node:util";
import { CUSTOMER_GUIDES } from "./walk.mjs";

/** The chapter slugs a bundle holds the five guides under, and what is missing from `slugs`. */
export const guideSlugs = () => CUSTOMER_GUIDES.map((slug) => `guides/${slug}`);

/** What stops the five guides being found: `bundleSlugs` is the chapters of a bundle, `siteStatus` slug -> HTTP status. */
export function unpublished({ bundleSlugs, siteStatus }) {
  const problems = [];
  for (const slug of guideSlugs()) {
    if (bundleSlugs && !bundleSlugs.includes(slug)) problems.push(`${slug}: not a chapter of the newest published bundle`);
    if (siteStatus && siteStatus.get(slug) !== 200) problems.push(`${slug}: the site answers HTTP ${siteStatus.get(slug) ?? "nothing"}, not 200`);
  }
  return problems;
}

async function newestBundle(registry) {
  const [host, ...path] = registry.split("/");
  const repo = path.join("/");
  const json = async (url, headers = {}) => {
    const response = await fetch(url, { headers, signal: AbortSignal.timeout(60_000) });
    if (!response.ok) throw new Error(`${url}: HTTP ${response.status}`);
    return response;
  };
  const { token } = await (await json(`https://${host}/token?scope=repository:${repo}:pull`)).json();
  const auth = { Authorization: `Bearer ${token}` };
  const { tags } = await (await json(`https://${host}/v2/${repo}/tags/list`, auth)).json();
  const newest = tags.filter((t) => /^\d+\.\d+\.\d+$/.test(t)).sort((a, b) => Number(a.split(".")[2]) - Number(b.split(".")[2])).at(-1);
  const manifest = await (await json(`https://${host}/v2/${repo}/manifests/${newest}`, { ...auth, Accept: "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" })).json();
  const layer = manifest.layers[0].digest;
  const work = await mkdtemp(join(tmpdir(), "email-explained-published-"));
  try {
    await writeFile(join(work, "layer.tar.gz"), Buffer.from(await (await json(`https://${host}/v2/${repo}/blobs/${layer}`, auth)).arrayBuffer()));
    execFileSync("tar", ["-xzf", join(work, "layer.tar.gz"), "-C", work, "bundle.json"]);
    const bundle = JSON.parse(await readFile(join(work, "bundle.json"), "utf8"));
    return { version: newest, commit: bundle.source?.commit, slugs: bundle.chapters.map((c) => c.slug) };
  } finally {
    await rm(work, { recursive: true, force: true });
  }
}

async function main() {
  const { values } = parseArgs({ options: { site: { type: "string", default: "https://rest-mail.com" }, registry: { type: "string", default: "ghcr.io/rest-mail/email-explained" } } });
  const bundle = await newestBundle(values.registry);
  const siteStatus = new Map();
  for (const slug of guideSlugs()) siteStatus.set(slug, (await fetch(`${values.site}/email-explained/${slug}`, { signal: AbortSignal.timeout(30_000) })).status);
  console.log(`newest bundle ${bundle.version}, built from ${bundle.commit}`);
  const problems = unpublished({ bundleSlugs: bundle.slugs, siteStatus });
  for (const p of problems) console.log(`NOT PUBLISHED ${p}`);
  console.log(problems.length ? `${problems.length} not published` : "all five guides are in the newest bundle and on the site");
  process.exitCode = problems.length ? 1 : 0;
}

if (import.meta.url === `file://${process.argv[1]}`) await main();
