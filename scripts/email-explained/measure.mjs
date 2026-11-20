// What a reader of the live guide sees, measured in headless Chrome (#2120 AC01,
// the measurements #822 asks for): the smallest node title of each framed
// diagram in rendered pixels, whether the page itself scrolls sideways, and
// whether a sideways swipe over a diagram scrolls the diagram and not the page.
//
//   used by approved.mjs deployed --measure, with CHROME=<chrome or chromium>
//
// Titles are measured as the computed font size times the SVG's screen
// transform, after the frame's fonts load, not read off the source: Archify fits
// long titles smaller, so only the rendered size says what a reader gets.
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const IN_FRAMES = `(async () => {
  const out = {};
  for (const frame of document.querySelectorAll("iframe")) {
    const name = frame.getAttribute("src")?.match(/diagrams\\/([a-z0-9-]+)\\.html/)?.[1];
    const doc = frame.contentDocument;
    if (!name || !doc) continue;
    await doc.fonts.ready;
    const sizes = [...doc.querySelectorAll("text[data-node-label]")].map((t) => {
      const m = t.ownerSVGElement.getScreenCTM();
      return parseFloat(getComputedStyle(t).fontSize) * Math.hypot(m.c, m.d);
    });
    const box = frame.closest(".guide-diagram-body") ?? frame.parentElement;
    out[name] = { titles: sizes.length, minTitlePx: sizes.length ? Math.min(...sizes) : null, box: box ? { scrollWidth: box.scrollWidth, clientWidth: box.clientWidth } : null };
  }
  return { frames: out, pageOverflowPx: Math.max(0, document.documentElement.scrollWidth - window.innerWidth) };
})()`;

/** A browser, started and later stopped, with one page and a send(method, params) to it. */
async function launch(chrome) {
  if (!chrome) throw new Error("--measure needs CHROME=<chrome or chromium>");
  const work = await mkdtemp(join(tmpdir(), "email-explained-measure-"));
  const child = spawn(chrome, ["--headless=new", "--no-sandbox", "--disable-gpu", "--remote-debugging-port=0", `--user-data-dir=${join(work, "profile")}`, "about:blank"], { stdio: ["ignore", "ignore", "pipe"] });
  const endpoint = await new Promise((ok, fail) => {
    let seen = "";
    child.stderr.on("data", (chunk) => {
      seen += chunk;
      const found = seen.match(/DevTools listening on (ws:\/\/\S+)/);
      if (found) ok(found[1]);
    });
    child.on("exit", () => fail(new Error(`Chrome exited before it was ready:\n${seen}`)));
  });
  const socket = new WebSocket(endpoint);
  await new Promise((ok) => socket.addEventListener("open", ok));
  let next = 0;
  const waiting = new Map();
  socket.addEventListener("message", ({ data }) => {
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
      socket.send(JSON.stringify({ id, method, params, sessionId }));
    });
  const { targetId } = await send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
  return {
    page: (method, params) => send(method, params, sessionId),
    async close() {
      socket.close();
      const exited = new Promise((done) => child.once("exit", done));
      child.kill();
      await exited;
      // Chrome may still be writing its profile as it goes; a leftover scratch directory is not a failed run.
      await rm(work, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }).catch(() => {});
    },
  };
}

async function evaluate(browser, expression) {
  const { result, exceptionDetails } = await browser.page("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (exceptionDetails) throw new Error(`in the page: ${exceptionDetails.text} ${exceptionDetails.exception?.description ?? ""}`);
  return result.value;
}

async function open(browser, url, { width, phone }) {
  await browser.page("Emulation.setDeviceMetricsOverride", { width, height: phone ? 844 : 900, deviceScaleFactor: 1, mobile: phone });
  await browser.page("Emulation.setTouchEmulationEnabled", { enabled: phone });
  await browser.page("Page.navigate", { url });
  // The chapter's frames load after the page does.
  for (let i = 0; i < 100; i++) {
    const ready = await evaluate(browser, `document.readyState === "complete" && [...document.querySelectorAll("iframe")].every((f) => f.contentDocument?.readyState === "complete")`);
    if (ready) return;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`${url}: the page and its frames did not finish loading in 25 seconds`);
}

/** Where a diagram's frame is once it is in view, and how far its box and the page are scrolled. */
const frameState = (name) => `(() => {
  const frame = [...document.querySelectorAll("iframe")].find((f) => f.getAttribute("src")?.includes("/${name}.html"));
  frame.scrollIntoView({ block: "center" });
  const box = frame.closest(".guide-diagram-body") ?? frame.parentElement;
  const rect = frame.getBoundingClientRect();
  return { pageX: scrollX, frameX: box.scrollLeft, x: rect.left + Math.min(rect.width, window.innerWidth) / 2, y: rect.top + rect.height / 2 };
})()`;

async function swipe(browser, name) {
  const before = await evaluate(browser, frameState(name));
  const touch = (type, x) => browser.page("Input.dispatchTouchEvent", { type, touchPoints: type === "touchEnd" ? [] : [{ x, y: before.y, id: 1, radiusX: 1, radiusY: 1, force: 1 }] });
  const pause = (ms) => new Promise((r) => setTimeout(r, ms));
  await touch("touchStart", before.x + 120);
  await pause(50);
  for (let i = 1; i <= 8; i++) {
    await touch("touchMove", before.x + 120 - i * 30);
    await pause(30);
  }
  await touch("touchEnd");
  await pause(400);
  const after = await evaluate(browser, frameState(name));
  return { frameScrolled: after.frameX - before.frameX, pageScrolledX: after.pageX - before.pageX };
}

/**
 * Measures each diagram's frame in the chapter that shows it, at each width.
 * `frameChapters` maps a diagram to the URL of a chapter that frames it.
 * Returns { laptop: { name: { minTitlePx, pageOverflowPx } }, phone: { ..., first: swipe } }.
 */
export async function measureSite({ chrome, frameChapters, widths }) {
  const browser = await launch(chrome);
  try {
    const measured = {};
    for (const [view, width] of Object.entries(widths)) {
      measured[view] = {};
      for (const [name, url] of Object.entries(frameChapters)) {
        if (!url) continue;
        await open(browser, url, { width, phone: view === "phone" });
        const { frames, pageOverflowPx } = await evaluate(browser, IN_FRAMES);
        const m = frames[name];
        if (!m) continue;
        measured[view][name] = { titles: m.titles, minTitlePx: m.minTitlePx, pageOverflowPx, box: m.box };
        if (view === "phone" && name === Object.keys(frameChapters)[0]) measured[view][name].swipe = await swipe(browser, name);
      }
    }
    return measured;
  } finally {
    await browser.close();
  }
}
