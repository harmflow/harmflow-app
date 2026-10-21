// Renders every derived icon in src-tauri/icons/ from the SVG masters in src-tauri/icons/source/, so a logo change is
// one command: `make icons`. Needs rsvg-convert (`brew install librsvg`) and iconutil, which ships with macOS.
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const icons = fileURLToPath(new URL("../src-tauri/icons/", import.meta.url));
const source = join(icons, "source");
const work = mkdtempSync(join(tmpdir(), "harmflow-icons-"));

function render(svg, size, out) {
  execFileSync("rsvg-convert", ["-w", String(size), "-h", String(size), "-o", out, svg]);
  return out;
}

// The full master from 64 px up; below, the simplified one, whose H and wave stay legible at 16 and 32 px.
function appIcon(size, out) {
  return render(join(source, size < 64 ? "app-icon-small.svg" : "app-icon.svg"), size, out);
}

// An .ico of PNG images: a 6-byte header, a 16-byte entry per image, then the images in order.
function writeIco(out, images) {
  const header = Buffer.alloc(6 + 16 * images.length);
  header.writeUInt16LE(1, 2); // type: icon
  header.writeUInt16LE(images.length, 4);
  let offset = header.length;
  images.forEach(({ size, png }, i) => {
    const entry = 6 + 16 * i;
    header.writeUInt8(size % 256, entry); // width and height; 0 stands for 256
    header.writeUInt8(size % 256, entry + 1);
    header.writeUInt16LE(1, entry + 4); // color planes
    header.writeUInt16LE(32, entry + 6); // bits per pixel
    header.writeUInt32LE(png.length, entry + 8);
    header.writeUInt32LE(offset, entry + 12);
    offset += png.length;
  });
  writeFileSync(out, Buffer.concat([header, ...images.map(({ png }) => png)]));
}

try {
  // The PNGs bundle.icon lists in tauri.conf.json, and a 1024 px export
  const pngs = [
    [32, "32x32.png"],
    [64, "64x64.png"],
    [128, "128x128.png"],
    [256, "128x128@2x.png"],
    [512, "icon.png"],
    [1024, "app-icon-1024.png"],
  ];
  for (const [size, name] of pngs) appIcon(size, join(icons, name));

  // icon.icns, through an iconset: each point size at 1x and 2x
  const iconset = join(work, "icon.iconset");
  mkdirSync(iconset);
  for (const pt of [16, 32, 128, 256, 512]) {
    appIcon(pt, join(iconset, `icon_${pt}x${pt}.png`));
    appIcon(pt * 2, join(iconset, `icon_${pt}x${pt}@2x.png`));
  }
  execFileSync("iconutil", ["-c", "icns", "-o", join(icons, "icon.icns"), iconset]);

  // Windows and Store sizes: outside bundle.icon until the Windows build, kept in step with the rest
  for (const size of [30, 44, 71, 89, 107, 142, 150, 284, 310]) appIcon(size, join(icons, `Square${size}x${size}Logo.png`));
  appIcon(50, join(icons, "StoreLogo.png"));
  const icoSizes = [16, 24, 32, 48, 64, 256];
  writeIco(
    join(icons, "icon.ico"),
    icoSizes.map((size) => ({ size, png: readFileSync(appIcon(size, join(work, `ico-${size}.png`))) })),
  );

  // Menu-bar template icons: 22 pt at 2x
  for (const state of ["idle", "executing"]) render(join(source, `tray-${state}.svg`), 44, join(icons, `tray-${state}.png`));
} finally {
  rmSync(work, { recursive: true, force: true });
}
