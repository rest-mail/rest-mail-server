// The static image of a diagram, for readers who see the guide on GitHub:
// Archify's rendered page, opened in headless Chrome in its embed mode (the
// diagram alone), light theme, motion stopped, captured as a PNG.
//
//   CHROME=<chrome or chromium> node png.mjs <diagram.html> <diagram.png>
//
// The page is rendered by `archify finalize` (WRITING.md); this only takes
// its picture, so the image never needs editing by hand.
import { spawn } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const [html, out] = process.argv.slice(2);
if (!html || !out || !process.env.CHROME) throw new Error("usage: CHROME=<chrome> node png.mjs <diagram.html> <diagram.png>");

// Wide enough that the diagram is drawn at its full size, as on a desktop.
const WIDTH = 1600;
const MIN_WIDTH = 760;
const SCALE = 1.25;

const profile = await mkdtemp(join(tmpdir(), "email-explained-png-"));
const chrome = spawn(
  process.env.CHROME,
  ["--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars", "--remote-debugging-port=0", `--user-data-dir=${profile}`, "about:blank"],
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

  // The page at a given width, once the diagram has been laid out and the
  // typefaces have loaded: where the diagram is, and how wide it was drawn.
  const draw = async (width) => {
    await page("Emulation.setDeviceMetricsOverride", { width, height: 1000, deviceScaleFactor: SCALE, mobile: false });
    await page("Page.navigate", { url: `${pathToFileURL(resolve(html))}?embed=1&theme=light` });
    for (let i = 0; i < 100; i++) {
      const { result } = await page("Runtime.evaluate", {
        expression: `(async () => {
          await document.fonts.ready;
          const box = document.querySelector(".diagram-container");
          const svg = box?.querySelector("svg");
          if (!svg || document.readyState !== "complete") return null;
          const r = box.getBoundingClientRect();
          const drawn = svg.viewBox.baseVal?.width || r.width;
          return r.width > 0 ? { x: r.left + scrollX, y: r.top + scrollY, width: r.width, height: r.height, drawn } : null;
        })()`,
        awaitPromise: true,
        returnByValue: true,
      });
      if (result.value) return result.value;
      await new Promise((ok) => setTimeout(ok, 100));
    }
    throw new Error(`${html}: no diagram drawn`);
  };

  // A diagram fills the page's width, so a narrow one, such as a sequence of
  // three participants, is drawn on a narrower page rather than blown up.
  let { drawn, ...box } = await draw(WIDTH);
  const fitted = Math.max(MIN_WIDTH, Math.min(WIDTH, Math.round(drawn * 1.4)));
  if (fitted < WIDTH) ({ drawn, ...box } = await draw(fitted));
  const { data } = await page("Page.captureScreenshot", { format: "png", captureBeyondViewport: true, clip: { ...box, scale: 1 } });
  await writeFile(out, Buffer.from(data, "base64"));
  console.log(`${out}: ${Math.round(box.width * SCALE)}×${Math.round(box.height * SCALE)}`);
  browser.close();
} finally {
  // Its profile can go once Chrome has stopped writing to it.
  const stopped = new Promise((ok) => (chrome.exitCode === null ? chrome.once("exit", ok) : ok()));
  chrome.kill();
  await stopped;
  await rm(profile, { recursive: true, force: true });
}
