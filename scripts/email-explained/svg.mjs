// The static image of each diagram, for readers who see the guide on GitHub.
// Archify renders the diagram's source, and headless Chrome takes the SVG its
// viewer's Export menu gives: vector, with its typefaces inside, and in the
// reader's light or dark theme. Its canvas is cropped to the drawing, and the
// file records the source it was made from and the Archify that drew it, so
// CI can tell when an image is older than its source (check.mjs).
//
//   CHROME=<chrome or chromium> node svg.mjs --archify <archify.mjs> [name ...]
//
// With no names, it exports every diagram in docs/email-explained/diagrams.
import { execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { GUIDE, TYPES } from "./bundle.mjs";

/** What an image records of where it came from: its source's hash and Archify. */
export const sourceHash = (bytes) => createHash("sha256").update(bytes).digest("hex");
export function provenanceOf(svg) {
  const root = svg.match(/<svg\b[^>]*>/)?.[0] ?? "";
  return { source: root.match(/\sdata-source-sha256="([0-9a-f]{64})"/)?.[1] ?? null, archify: root.match(/\sdata-archify="([^"]*)"/)?.[1] ?? null };
}

/**
 * Archify embeds its typeface in every subset Google Fonts makes of it
 * (Cyrillic, Greek, Vietnamese, Latin…). An image keeps only the subsets whose
 * characters it shows, which is most of its size.
 */
export function dropUnusedFonts(svg) {
  const text = svg
    .replace(/<style\b[\s\S]*?<\/style>/g, "")
    .replace(/<[^>]*>/g, "")
    .replace(/&#x([0-9a-f]+);/gi, (_, hex) => String.fromCodePoint(parseInt(hex, 16)))
    .replace(/&#(\d+);/g, (_, dec) => String.fromCodePoint(Number(dec)))
    .replace(/&(amp|lt|gt|quot|apos);/g, "");
  const used = [...new Set([...text].map((c) => c.codePointAt(0)))];
  return svg.replace(/@font-face\s*\{[^}]*\}\s*/g, (face) => {
    const range = face.match(/unicode-range:\s*([^;}]+)/)?.[1];
    if (!range) return face;
    const spans = range.split(",").map((part) => {
      const [from, to = from] = part.trim().replace(/^U\+/i, "").split("-");
      return [parseInt(from, 16), parseInt(to, 16)];
    });
    return used.some((c) => spans.some(([from, to]) => c >= from && c <= to)) ? face : "";
  });
}

/**
 * Archify's export, cropped to the drawing (x, y, width, height in the SVG's
 * own units), without the typeface subsets it does not use, and stamped with
 * where it came from.
 */
export function finish(svg, crop, { source, archify }) {
  const box = [crop.x, crop.y, crop.width, crop.height].join(" ");
  return dropUnusedFonts(svg).replace(/<svg\b[^>]*>/, (root) =>
    root
      .replace(/\sviewBox="[^"]*"/, ` viewBox="${box}"`)
      .replace(/\swidth="[^"]*"/, ` width="${crop.width}"`)
      .replace(/\sheight="[^"]*"/, ` height="${crop.height}"`)
      // The viewer's own sizing, which would override the crop.
      .replace(/\sstyle="[^"]*"/, "")
      .replace(/>$/, ` data-source-sha256="${source}" data-archify="${archify}">`),
  );
}

async function main() {
  const { values, positionals } = parseArgs({ options: { archify: { type: "string" } }, allowPositionals: true });
  if (!values.archify || !process.env.CHROME) throw new Error("usage: CHROME=<chrome> node svg.mjs --archify <archify.mjs> [name ...]");
  const { archify: pin } = JSON.parse(await readFile(new URL("package.json", import.meta.url), "utf8"));
  const root = execFileSync("git", ["rev-parse", "--show-toplevel"], { encoding: "utf8" }).trim();
  const dir = join(root, GUIDE, "diagrams");
  const sources = (await readdir(dir))
    .map((name) => name.match(/^([a-z0-9-]+)\.([a-z]+)\.json$/))
    .filter((m) => m && TYPES.has(m[2]) && (!positionals.length || positionals.includes(m[1])));
  if (positionals.length && sources.length !== positionals.length) throw new Error(`no diagram source for some of: ${positionals.join(", ")}`);

  const work = await mkdtemp(join(tmpdir(), "email-explained-svg-"));
  const chrome = spawn(
    process.env.CHROME,
    ["--headless=new", "--no-sandbox", "--disable-gpu", "--remote-debugging-port=0", `--user-data-dir=${join(work, "profile")}`, "about:blank"],
    { stdio: ["ignore", "ignore", "pipe"] },
  );
  try {
    const endpoint = await new Promise((ok, fail) => {
      let seen = "";
      chrome.stderr.on("data", (chunk) => {
        seen += chunk;
        const found = seen.match(/DevTools listening on (ws:\/\/\S+)/);
        if (found) ok(found[1]);
      });
      chrome.on("exit", () => fail(new Error(`Chrome exited before it was ready:\n${seen}`)));
    });
    const browser = new WebSocket(endpoint);
    await new Promise((ok) => browser.addEventListener("open", ok));
    let next = 0;
    const waiting = new Map();
    browser.addEventListener("message", ({ data }) => {
      const message = JSON.parse(data);
      const reply = waiting.get(message.id);
      if (!reply) return;
      waiting.delete(message.id);
      if (message.error) reply.fail(new Error(`${reply.method}: ${message.error.message}`));
      else reply.ok(message.result);
    });
    const send = (method, params = {}, sessionId) =>
      new Promise((ok, fail) => {
        const id = ++next;
        waiting.set(id, { ok, fail, method });
        browser.send(JSON.stringify({ id, method, params, sessionId }));
      });
    const { targetId } = await send("Target.createTarget", { url: "about:blank" });
    const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
    const page = (method, params) => send(method, params, sessionId);
    await page("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
    await page("Emulation.setDeviceMetricsOverride", { width: 1600, height: 1000, deviceScaleFactor: 1, mobile: false });

    for (const [file, name, type] of sources) {
      const bytes = await readFile(join(dir, file));
      const html = join(work, `${name}.html`);
      execFileSync(process.execPath, [values.archify, "render", type, join(dir, file), html], {
        env: { ...process.env, ARCHIFY_UPDATE_CHECK_DISABLED: "1" },
        stdio: ["ignore", "ignore", "inherit"],
      });
      await page("Page.navigate", { url: `${pathToFileURL(html)}?embed=1` });
      let exported = null;
      for (let i = 0; i < 100 && !exported; i++) {
        const { result } = await page("Runtime.evaluate", {
          expression: `(async () => {
            await document.fonts.ready;
            const svg = document.querySelector(".diagram-container svg");
            const button = document.querySelector('.export-menu button[data-format="svg"]');
            if (!svg || !button || document.readyState !== "complete" || !svg.getBoundingClientRect().width) return null;
            // The drawing: everything on the canvas but the background that
            // fills it, in the SVG's own units, with a margin.
            const canvas = svg.getBoundingClientRect();
            const toSvg = svg.getScreenCTM().inverse();
            const corners = [...svg.children]
              .filter((e) => !["defs", "style", "title", "desc", "metadata"].includes(e.localName))
              .map((e) => e.getBoundingClientRect())
              .filter((r) => (r.width > 0 || r.height > 0) && (r.width < canvas.width * 0.98 || r.height < canvas.height * 0.98))
              .flatMap((r) => [new DOMPoint(r.left, r.top).matrixTransform(toSvg), new DOMPoint(r.right, r.bottom).matrixTransform(toSvg)]);
            const vb = svg.viewBox.baseVal;
            const margin = 24;
            const x = Math.max(vb.x, Math.floor(Math.min(...corners.map((p) => p.x)) - margin));
            const y = Math.max(vb.y, Math.floor(Math.min(...corners.map((p) => p.y)) - margin));
            const right = Math.min(vb.x + vb.width, Math.ceil(Math.max(...corners.map((p) => p.x)) + margin));
            const bottom = Math.min(vb.y + vb.height, Math.ceil(Math.max(...corners.map((p) => p.y)) + margin));
            // Archify's own SVG export, the one its Export menu downloads.
            const blobs = [];
            const create = URL.createObjectURL;
            URL.createObjectURL = (blob) => (blobs.push(blob), create.call(URL, blob));
            HTMLAnchorElement.prototype.click = function () {};
            button.click();
            for (let i = 0; i < 100 && !blobs.length; i++) await new Promise((r) => setTimeout(r, 50));
            if (!blobs.length) throw new Error("Archify's export gave nothing");
            return { svg: await blobs[0].text(), crop: { x, y, width: right - x, height: bottom - y } };
          })()`,
          awaitPromise: true,
          returnByValue: true,
        });
        if (result.subtype === "error") throw new Error(`${name}: ${result.description}`);
        exported = result.value;
        if (!exported) await new Promise((ok) => setTimeout(ok, 100));
      }
      if (!exported) throw new Error(`${name}: no diagram drawn`);
      const out = finish(exported.svg, exported.crop, { source: sourceHash(bytes), archify: `${pin.version} ${pin.commit}` });
      await writeFile(join(dir, `${name}.svg`), out);
      console.log(`${GUIDE}/diagrams/${name}.svg: ${exported.crop.width}×${exported.crop.height}, ${Math.round(out.length / 1024)} KB`);
    }
    browser.close();
  } finally {
    const stopped = new Promise((ok) => (chrome.exitCode === null ? chrome.once("exit", ok) : ok()));
    chrome.kill();
    await stopped;
    // Chrome's helper processes can still be closing files in it for a moment.
    await rm(work, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  }
}

if (import.meta.url === `file://${process.argv[1]}`) await main();
