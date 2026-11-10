import { execFile } from "node:child_process";
import { stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";

import { t } from "../i18n.js";
import { concatScript } from "./reel.js";

const run = promisify(execFile);
const FFMPEG = process.env.FFMPEG_PATH || "ffmpeg";
const ENCODE_TIMEOUT_MS = 10 * 60 * 1000;
const FPS = 30;
const MAX_COMPARE_WIDTH = 2560;

const even = (n) => Math.max(2, Math.floor(n / 2) * 2);

function fit(size, maxWidth) {
  if (size.width <= maxWidth) return { width: even(size.width), height: even(size.height) };
  const k = maxWidth / size.width;
  return { width: even(size.width * k), height: even(size.height * k) };
}

function frameFilter({ width, height }) {
  return `fps=${FPS},scale=${width}:${height}:force_original_aspect_ratio=decrease:flags=lanczos,` +
    `pad=${width}:${height}:(ow-iw)/2:(oh-ih)/2:color=0x0B1220,setsar=1`;
}

// The fps filter stretches the last frame by a guess (ffmpeg 8 added seconds to a 7 s reel), so set
// the length from the reel.
function length(entries) {
  return ["-t", (entries.reduce((s, e) => s + e.duration, 0) / 1000).toFixed(3)];
}

const H264 = ["-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-an"];

async function ffmpeg(args) {
  try {
    await run(FFMPEG, ["-hide_banner", "-v", "error", "-y", ...args], { timeout: ENCODE_TIMEOUT_MS, maxBuffer: 8 << 20 });
  } catch (err) {
    const detail = (err.stderr || err.message || "").toString().trim().split("\n").slice(-3).join(" | ");
    throw new Error(t("errors.ffmpeg_failed", { detail }));
  }
}

export async function ffmpegAvailable() {
  try {
    await run(FFMPEG, ["-version"], { timeout: 10_000 });
    return true;
  } catch {
    return false;
  }
}

async function script(dir, name, entries) {
  const file = path.join(dir, name);
  await writeFile(file, concatScript(entries));
  return file;
}

export async function encode(entries, { dir, out, size }) {
  const list = await script(dir, "frames.ffconcat", entries);
  const box = { width: even(size.width), height: even(size.height) };
  await ffmpeg(["-f", "concat", "-safe", "0", "-i", list, "-vf", frameFilter(box), ...length(entries), ...H264, out]);
  return out;
}

export async function encodeSideBySide(left, right, { dir, out, size }) {
  const half = fit(size, MAX_COMPARE_WIDTH / 2);
  const l = await script(dir, "left.ffconcat", left);
  const r = await script(dir, "right.ffconcat", right);
  const graph = `[0:v]${frameFilter(half)}[l];[1:v]${frameFilter(half)}[r];` +
    `[l][r]hstack=inputs=2:shortest=1,format=yuv420p[v]`;
  await ffmpeg(["-f", "concat", "-safe", "0", "-i", l, "-f", "concat", "-safe", "0", "-i", r,
    "-filter_complex", graph, "-map", "[v]", ...length(left), ...H264, out]);
  return out;
}

export async function extractFrame(video, out, atSec = 1) {
  await ffmpeg(["-ss", Math.max(0, atSec).toFixed(3), "-i", video, "-frames:v", "1", "-q:v", "3", out]);
  try {
    const { size } = await stat(out);
    return size > 0 ? out : null;
  } catch {
    return null;
  }
}
