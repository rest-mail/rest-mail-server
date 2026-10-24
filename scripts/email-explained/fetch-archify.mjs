// Fetches Archify, at the commit package.json pins, into a directory, and
// prints the path of its command line. CI renders and validates the guide's
// diagrams with it; run it locally to get the same version:
//
//   node fetch-archify.mjs <directory>    prints <directory>/archify/bin/archify.mjs
//
// Archify has no dependencies of its own, so nothing is installed.
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";

const { archify } = JSON.parse(readFileSync(new URL("package.json", import.meta.url), "utf8"));
const dir = resolve(process.argv[2] ?? ".archify");
const git = (...args) => execFileSync("git", ["-C", dir, ...args], { stdio: ["ignore", "ignore", "inherit"] });

mkdirSync(dir, { recursive: true });
if (!existsSync(join(dir, ".git"))) git("init", "-q");
git("fetch", "-q", "--depth", "1", `https://github.com/${archify.repository}`, archify.commit);
git("checkout", "-q", "--detach", archify.commit);
const bin = join(dir, "archify", "bin", "archify.mjs");
if (!existsSync(bin)) throw new Error(`no ${bin} at ${archify.repository}@${archify.commit}`);
console.log(bin);
