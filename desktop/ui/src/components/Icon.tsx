// Line icons drawn for HarmFlow on a 24px grid; stroke follows the text color.
import { useId, type ReactNode, type SVGProps } from "react";

const paths: Record<string, ReactNode> = {
  grid: (
    <>
      <rect x="3.5" y="3.5" width="7" height="7" rx="1.5" />
      <rect x="13.5" y="3.5" width="7" height="7" rx="1.5" />
      <rect x="3.5" y="13.5" width="7" height="7" rx="1.5" />
      <rect x="13.5" y="13.5" width="7" height="7" rx="1.5" />
    </>
  ),
  home: <path d="M3.5 11.5 12 4l8.5 7.5M6 10v9.5h4.5V14h3v5.5H18V10" />,
  play: <path d="M7 4.8v14.4a.6.6 0 0 0 .9.5l11.2-7.2a.6.6 0 0 0 0-1L7.9 4.3a.6.6 0 0 0-.9.5Z" />,
  playFilled: <path d="M7 4.8v14.4a.6.6 0 0 0 .9.5l11.2-7.2a.6.6 0 0 0 0-1L7.9 4.3a.6.6 0 0 0-.9.5Z" fill="currentColor" />,
  list: (
    <>
      <rect x="4" y="3.5" width="16" height="17" rx="2" />
      <path d="M8 8h8M8 12h8M8 16h5" />
    </>
  ),
  clock: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7.5V12l3 2" />
    </>
  ),
  history: (
    <>
      <path d="M4 12a8 8 0 1 0 2.4-5.7L4 8.6" />
      <path d="M4 4v4.6h4.6M12 8v4.2l2.8 1.8" />
    </>
  ),
  settings: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M12 2.8v2.4M12 18.8v2.4M4.1 6.6l2 1.2M17.9 16.2l2 1.2M4.1 17.4l2-1.2M17.9 7.8l2-1.2M2.8 12h2.4M18.8 12h2.4" />
      <circle cx="12" cy="12" r="6.2" />
    </>
  ),
  plus: <path d="M12 5v14M5 12h14" />,
  more: (
    <>
      <circle cx="6" cy="12" r="1.3" fill="currentColor" />
      <circle cx="12" cy="12" r="1.3" fill="currentColor" />
      <circle cx="18" cy="12" r="1.3" fill="currentColor" />
    </>
  ),
  grip: (
    <>
      <circle cx="9" cy="6.5" r="1.3" fill="currentColor" />
      <circle cx="15" cy="6.5" r="1.3" fill="currentColor" />
      <circle cx="9" cy="12" r="1.3" fill="currentColor" />
      <circle cx="15" cy="12" r="1.3" fill="currentColor" />
      <circle cx="9" cy="17.5" r="1.3" fill="currentColor" />
      <circle cx="15" cy="17.5" r="1.3" fill="currentColor" />
    </>
  ),
  check: <path d="m5 12.5 4.5 4.5L19 7.5" />,
  x: <path d="M6.5 6.5l11 11M17.5 6.5l-11 11" />,
  stop: <rect x="6.5" y="6.5" width="11" height="11" rx="1.5" fill="currentColor" stroke="none" />,
  terminal: (
    <>
      <rect x="3" y="4.5" width="18" height="15" rx="2" />
      <path d="m7 9.5 3 2.5-3 2.5M12.5 15H17" />
    </>
  ),
  prompt: <path d="m5 7 5 5-5 5M12 17h7" />,
  code: <path d="m8.5 7-5 5 5 5M15.5 7l5 5-5 5M13.5 5l-3 14" />,
  file: (
    <>
      <path d="M14 3.5H7a1.5 1.5 0 0 0-1.5 1.5v14A1.5 1.5 0 0 0 7 20.5h10a1.5 1.5 0 0 0 1.5-1.5V8L14 3.5Z" />
      <path d="M14 3.5V8h4.5M9 13h6M9 16.5h4" />
    </>
  ),
  folder: <path d="M3.5 7a1.5 1.5 0 0 1 1.5-1.5h4.2l2 2.2H19a1.5 1.5 0 0 1 1.5 1.5v8.3A1.5 1.5 0 0 1 19 19H5a1.5 1.5 0 0 1-1.5-1.5V7Z" />,
  repo: (
    <>
      <path d="M6 4.5h11.5v13H7a1.5 1.5 0 0 0-1.5 1.5V6A1.5 1.5 0 0 1 7 4.5" />
      <path d="M5.5 19A1.5 1.5 0 0 0 7 20.5h10.5v-3" />
    </>
  ),
  branch: (
    <>
      <circle cx="7" cy="5.5" r="2" />
      <circle cx="7" cy="18.5" r="2" />
      <circle cx="17" cy="7.5" r="2" />
      <path d="M7 7.5v9M17 9.5c0 4-10 2.5-10 7" />
    </>
  ),
  issue: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <circle cx="12" cy="12" r="1.6" fill="currentColor" />
    </>
  ),
  pr: (
    <>
      <circle cx="6.5" cy="5.5" r="2" />
      <circle cx="6.5" cy="18.5" r="2" />
      <circle cx="17.5" cy="18.5" r="2" />
      <path d="M6.5 7.5v9M17.5 16.5V9.5a2.5 2.5 0 0 0-2.5-2.5h-4M12.5 5l-2 2 2 2" />
    </>
  ),
  pencil: <path d="M4.5 19.5 5.5 15 15.8 4.7a1.8 1.8 0 0 1 2.5 0l1 1a1.8 1.8 0 0 1 0 2.5L9 18.5l-4.5 1ZM14 6.5l3.5 3.5" />,
  search: (
    <>
      <circle cx="11" cy="11" r="6.5" />
      <path d="m16 16 4.5 4.5" />
    </>
  ),
  test: <path d="M9.5 3.5h5M10.5 3.5v6L5 18.5a1.5 1.5 0 0 0 1.3 2h11.4a1.5 1.5 0 0 0 1.3-2l-5.5-9v-6M7.5 14.5h9" />,
  switch: <path d="M5 8.5h13.5l-3-3M19 15.5H5.5l3 3" />,
  alert: (
    <>
      <path d="M12 4 2.8 19.5h18.4L12 4Z" />
      <path d="M12 10v4.2M12 17.2v.1" />
    </>
  ),
  shield: (
    <>
      <path d="M12 3.5 5 6v5.5c0 4.2 3 7.6 7 9 4-1.4 7-4.8 7-9V6l-7-2.5Z" />
      <path d="M12 8.5v4M12 15.3v.1" />
    </>
  ),
  message: <path d="M4.5 6A1.5 1.5 0 0 1 6 4.5h12A1.5 1.5 0 0 1 19.5 6v9A1.5 1.5 0 0 1 18 16.5H9.5L5.5 20v-3.5H6A1.5 1.5 0 0 1 4.5 15V6Z" />,
  agent: (
    <>
      <rect x="5" y="7.5" width="14" height="11" rx="3" />
      <path d="M12 4v3.5M9 12.5v1.5M15 12.5v1.5M2.5 12.5v2M21.5 12.5v2" />
    </>
  ),
  dot: <circle cx="12" cy="12" r="3.2" fill="currentColor" stroke="none" />,
  circle: <circle cx="12" cy="12" r="7" />,
  refresh: (
    <>
      <path d="M19.5 12a7.5 7.5 0 0 1-13.2 4.9M4.5 12a7.5 7.5 0 0 1 13.2-4.9" />
      <path d="M17.7 3.5v3.6h-3.6M6.3 20.5v-3.6h3.6" />
    </>
  ),
  bell: (
    <>
      <path d="M6 16.5V11a6 6 0 0 1 12 0v5.5l1.5 2h-15l1.5-2Z" />
      <path d="M10 20.5a2.2 2.2 0 0 0 4 0" />
    </>
  ),
  copy: (
    <>
      <rect x="8.5" y="8.5" width="11" height="11" rx="2" />
      <path d="M15.5 8.5V6a1.5 1.5 0 0 0-1.5-1.5H6A1.5 1.5 0 0 0 4.5 6v8A1.5 1.5 0 0 0 6 15.5h2.5" />
    </>
  ),
  chevronDown: <path d="m6.5 9.5 5.5 5.5 5.5-5.5" />,
  chevronRight: <path d="m9.5 6.5 5.5 5.5-5.5 5.5" />,
  chevronLeft: <path d="m14.5 6.5-5.5 5.5 5.5 5.5" />,
  arrowRight: <path d="M5 12h14M13.5 6.5 19 12l-5.5 5.5" />,
  external: <path d="M14 4.5h5.5V10M19.5 4.5 11 13M17.5 14v4a1.5 1.5 0 0 1-1.5 1.5H6A1.5 1.5 0 0 1 4.5 18V8A1.5 1.5 0 0 1 6 6.5h4" />,
  upload: <path d="M12 15.5v-11M7.5 9 12 4.5 16.5 9M5 15v3.5A1.5 1.5 0 0 0 6.5 20h11a1.5 1.5 0 0 0 1.5-1.5V15" />,
  download: <path d="M12 4.5v11M7.5 11l4.5 4.5 4.5-4.5M5 15v3.5A1.5 1.5 0 0 0 6.5 20h11a1.5 1.5 0 0 0 1.5-1.5V15" />,
  trash: <path d="M5 7h14M9.5 7V5a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1v2M7 7l.8 12a1.5 1.5 0 0 0 1.5 1.4h5.4a1.5 1.5 0 0 0 1.5-1.4L17 7M10.5 11v5.5M13.5 11v5.5" />,
  layers: (
    <>
      <path d="m12 4 8.5 4.5L12 13 3.5 8.5 12 4Z" />
      <path d="m3.5 12.5 8.5 4.5 8.5-4.5M3.5 16.5 12 21l8.5-4.5" />
    </>
  ),
  cpu: (
    <>
      <rect x="6.5" y="6.5" width="11" height="11" rx="1.5" />
      <path d="M9.5 3.5v3M14.5 3.5v3M9.5 17.5v3M14.5 17.5v3M3.5 9.5h3M3.5 14.5h3M17.5 9.5h3M17.5 14.5h3" />
    </>
  ),
  power: <path d="M12 3.5v8M7.2 6.5a7.5 7.5 0 1 0 9.6 0" />,
  globe: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M3.5 12h17M12 3.5c2.5 2.6 3.5 5.4 3.5 8.5s-1 5.9-3.5 8.5c-2.5-2.6-3.5-5.4-3.5-8.5s1-5.9 3.5-8.5Z" />
    </>
  ),
  keyboard: (
    <>
      <rect x="3" y="6.5" width="18" height="11" rx="2" />
      <path d="M7 10h.01M10 10h.01M13 10h.01M16 10h.01M7 13.5h.01M16 13.5h.01M9.5 13.5h4" />
    </>
  ),
  key: (
    <>
      <circle cx="8" cy="15.5" r="3.5" />
      <path d="m10.5 13 8-8M15.5 8l2.5 2.5M17.5 6l2 2" />
    </>
  ),
  sparkle: <path d="M12 3.5 13.8 10l6.7 2-6.7 2L12 20.5 10.2 14l-6.7-2 6.7-2L12 3.5Z" />,
  coin: (
    <>
      <ellipse cx="12" cy="7" rx="7" ry="3" />
      <path d="M5 7v5c0 1.66 3.13 3 7 3s7-1.34 7-3V7M5 12v5c0 1.66 3.13 3 7 3s7-1.34 7-3v-5" />
    </>
  ),
  sidebarCollapse: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M9 4.5v15M15.5 9.5 13 12l2.5 2.5" />
    </>
  ),
  sidebarExpand: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M9 4.5v15M13 9.5l2.5 2.5-2.5 2.5" />
    </>
  ),
};

export type IconName = keyof typeof paths;

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, "name"> {
  name: IconName | string;
  size?: number;
  title?: string;
}

export function Icon({ name, size = 18, title, ...rest }: IconProps) {
  const drawn = Object.hasOwn(paths, name) ? name : "dot";
  return (
    <svg
      data-icon={drawn}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.7}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden={title ? undefined : true}
      role={title ? "img" : undefined}
      {...rest}
    >
      {title ? <title>{title}</title> : null}
      {paths[drawn]}
    </svg>
  );
}

/** The marks of the harnesses: an original glyph each, not a vendor logo. */
export function HarnessMark({ harness, size = 16 }: { harness?: string; size?: number }) {
  if (harness === "codex") {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" className="harness-mark harness-mark--codex">
        <path
          d="M12 3.2 19.6 7.6v8.8L12 20.8l-7.6-4.4V7.6L12 3.2Z"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
          strokeLinejoin="round"
        />
        <path d="m9 10 2.4 2L9 14M12.8 14.3h2.6" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" />
      </svg>
    );
  }
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" className="harness-mark harness-mark--claude">
      {Array.from({ length: 8 }, (_, i) => (
        <path
          key={i}
          d="M12 12 12 3.6"
          stroke="currentColor"
          strokeWidth="2.2"
          strokeLinecap="round"
          transform={`rotate(${i * 45 + 8} 12 12)`}
        />
      ))}
    </svg>
  );
}

// The logo's H and wave, in the coordinates of the app icon (src-tauri/icons/source/app-icon.svg).
const LOGO_STEMS = "M150 128V384M362 128V384";
const LOGO_WAVE = "M44 256q53 104 106 0t106 0t106 0t106 0";

/** The HarmFlow logo: a neon H whose crossbar is a wave, drawn like the app icon's mark without its tile and ring. */
export function Logo({ size = 34 }: { size?: number }) {
  // Ids per copy: a duplicate id resolves to the first copy, and stops resolving while that copy is hidden.
  const id = useId().replace(/[^\w-]/g, "");
  const ref = (name: string) => `url(#${id}-${name})`;
  // The view box holds the whole wave fade and bloom, which a tighter box would cut at its edges
  return (
    <svg width={size} height={size} viewBox="48 48 416 416" aria-hidden="true" className="logo">
      <defs>
        {/* The colors run left to right, as on the app icon */}
        <linearGradient id={`${id}-neon`} gradientUnits="userSpaceOnUse" x1="128" y1="0" x2="384" y2="0">
          <stop offset="0" stopColor="var(--hf-color-logo-cyan)" />
          <stop offset="0.5" stopColor="var(--hf-color-logo-violet)" />
          <stop offset="1" stopColor="var(--hf-color-logo-magenta)" />
        </linearGradient>
        <linearGradient id={`${id}-core`} gradientUnits="userSpaceOnUse" x1="128" y1="0" x2="384" y2="0">
          <stop offset="0" stopColor="var(--hf-color-logo-core-cyan)" />
          <stop offset="0.5" stopColor="var(--hf-color-logo-core-violet)" />
          <stop offset="1" stopColor="var(--hf-color-logo-core-magenta)" />
        </linearGradient>
        {/* The wave fades in before the H and out after it */}
        <linearGradient id={`${id}-fade`} gradientUnits="userSpaceOnUse" x1="56" y1="0" x2="456" y2="0">
          <stop offset="0" stopColor="#fff" stopOpacity="0" />
          <stop offset="0.235" stopColor="#fff" stopOpacity="1" />
          <stop offset="0.765" stopColor="#fff" stopOpacity="1" />
          <stop offset="1" stopColor="#fff" stopOpacity="0" />
        </linearGradient>
        <mask id={`${id}-flow`} maskUnits="userSpaceOnUse" x="0" y="0" width="512" height="512">
          <rect x="56" width="400" height="512" fill={ref("fade")} />
        </mask>
        {/* A user-space region: the stems are vertical lines, whose bounding box has no width */}
        <filter id={`${id}-bloom`} filterUnits="userSpaceOnUse" x="0" y="0" width="512" height="512">
          <feGaussianBlur stdDeviation="16" />
        </filter>
      </defs>
      <g fill="none" strokeLinecap="round">
        <g stroke={ref("neon")} opacity="0.7" filter={ref("bloom")}>
          <path d={LOGO_STEMS} strokeWidth="56" />
          <path d={LOGO_WAVE} strokeWidth="44" mask={ref("flow")} />
        </g>
        <g stroke={ref("neon")}>
          <path d={LOGO_STEMS} strokeWidth="44" />
          <path d={LOGO_WAVE} strokeWidth="32" mask={ref("flow")} />
        </g>
        <g stroke={ref("core")}>
          <path d={LOGO_STEMS} strokeWidth="15" />
          <path d={LOGO_WAVE} strokeWidth="10" mask={ref("flow")} />
        </g>
      </g>
    </svg>
  );
}

// The wordmark's outlines, in the coordinates of its master (src-tauri/icons/source/wordmark.svg): the name in Inter,
// converted to paths, so no font sets it. design.test.ts holds them, and their colors, equal to the master's.
const WORDMARK_BOX = { x: 12, y: -186.25, width: 1236.12, height: 189.25 };
const WORDMARK_HARM =
  "M12 0V-186.25H57V-113.88H127.88V-186.25H172.88V0H127.88V-75.62H57V0ZM237.25 2.25Q224 2.25 213.81 -2.06Q203.62 -6.38 197.81 -15.06Q192 -23.75 192 -36.88Q192 -48 196 -55.56Q200 -63.12 207.06 -67.88Q214.12 -72.62 223.44 -75.06Q232.75 -77.5 243.25 -78.38Q255 -79.38 262.12 -80.56Q269.25 -81.75 272.38 -83.94Q275.5 -86.12 275.5 -90.12V-90.75Q275.5 -94.75 273.25 -97.69Q271 -100.62 266.88 -102.31Q262.75 -104 257.12 -104Q251.12 -104 246.75 -102.19Q242.38 -100.38 239.88 -97.06Q237.38 -93.75 236.88 -89.25H196.88Q197.88 -103.62 205.25 -113.81Q212.62 -124 226.06 -129.44Q239.5 -134.88 258.62 -134.88Q273 -134.88 284.06 -131.81Q295.12 -128.75 302.75 -122.81Q310.38 -116.88 314.25 -108.31Q318.12 -99.75 318.12 -88.88V0H276.12V-18.5H275.62Q271.62 -11.38 266.25 -6.81Q260.88 -2.25 253.75 0Q246.62 2.25 237.25 2.25ZM250.38 -25.88Q257.5 -25.88 263.31 -28.62Q269.12 -31.38 272.5 -36.19Q275.88 -41 275.88 -47.5V-59.5Q274.12 -58.75 271.75 -58Q269.38 -57.25 266.44 -56.62Q263.5 -56 260 -55.38Q256.5 -54.75 252.5 -54Q246.75 -53.12 242.31 -51.25Q237.88 -49.38 235.31 -46.31Q232.75 -43.25 232.75 -38.88Q232.75 -34.75 235 -31.88Q237.25 -29 241.19 -27.44Q245.12 -25.88 250.38 -25.88ZM342.12 0V-132H383.75V-108.75H384.12Q387.75 -121.38 395.44 -126.94Q403.12 -132.5 416.38 -132.5Q419.88 -132.5 422.69 -132.44Q425.5 -132.38 428.12 -132.25V-96.75Q425.75 -96.88 420.81 -97Q415.88 -97.12 411.12 -97.12Q403.5 -97.12 397.62 -93.69Q391.75 -90.25 388.44 -83.62Q385.12 -77 385.12 -67.62V0ZM445.75 0V-132H487.12V-101.25H485.12Q488.38 -112.75 494.75 -120.19Q501.12 -127.62 509.62 -131.25Q518.12 -134.88 527.5 -134.88Q542.5 -134.88 553.75 -126.19Q565 -117.5 567.25 -101.25H563.5Q565.88 -112.12 572.38 -119.62Q578.88 -127.12 588.19 -131Q597.5 -134.88 608.12 -134.88Q621.12 -134.88 631.19 -129.25Q641.25 -123.62 647 -113.06Q652.75 -102.5 652.75 -87.88V0H609.62V-78.38Q609.62 -88.75 604.06 -94.38Q598.5 -100 589.75 -100Q583.75 -100 579.25 -97.25Q574.75 -94.5 572.25 -89.5Q569.75 -84.5 569.75 -77.75V0H528.62V-78.88Q528.62 -88.5 523.25 -94.25Q517.88 -100 509 -100Q503.25 -100 498.69 -97.25Q494.12 -94.5 491.44 -89.38Q488.75 -84.25 488.75 -76.88V0Z";
const WORDMARK_FLOW =
  "M676.62 0V-186.25H811.12V-148H721.62V-108.38H805.5V-71.38H721.62V0ZM871.38 -186.25V0H828.38V-186.25ZM959.38 3Q938.75 3 923.06 -5.5Q907.38 -14 898.62 -29.5Q889.88 -45 889.88 -66Q889.88 -86.88 898.62 -102.38Q907.38 -117.88 923.06 -126.44Q938.75 -135 959.38 -135Q980.12 -135 995.69 -126.44Q1011.25 -117.88 1020 -102.38Q1028.75 -86.88 1028.75 -66Q1028.75 -45 1020 -29.5Q1011.25 -14 995.69 -5.5Q980.12 3 959.38 3ZM959.38 -31.25Q966.88 -31.25 972.69 -35.31Q978.5 -39.38 981.88 -47.12Q985.25 -54.88 985.25 -66Q985.25 -77.12 981.88 -84.88Q978.5 -92.62 972.69 -96.69Q966.88 -100.75 959.38 -100.75Q951.88 -100.75 946 -96.69Q940.12 -92.62 936.81 -84.88Q933.5 -77.12 933.5 -66Q933.5 -54.88 936.81 -47.12Q940.12 -39.38 946 -35.31Q951.88 -31.25 959.38 -31.25ZM1073.38 0 1032.75 -132H1078L1088.25 -90.75Q1091.75 -76.25 1095 -60.56Q1098.25 -44.88 1101.5 -29.62H1095.75Q1099.38 -44.88 1103 -60.56Q1106.62 -76.25 1110.5 -90.75L1121.38 -132H1159.5L1170.38 -90.75Q1174.25 -76.25 1177.88 -60.56Q1181.5 -44.88 1185.12 -29.62H1179.5Q1182.62 -44.88 1185.75 -60.5Q1188.88 -76.12 1192.25 -90.75L1202.12 -132H1248.12L1207.5 0H1163.12L1148.5 -49.38Q1146.5 -56.5 1144.56 -64Q1142.62 -71.5 1140.69 -79.19Q1138.75 -86.88 1136.75 -94.5H1144.25Q1142.25 -86.88 1140.31 -79.19Q1138.38 -71.5 1136.44 -64Q1134.5 -56.5 1132.38 -49.38L1117.75 0Z";

/**
 * The HarmFlow wordmark: the name in outlines, the same image under either typeface. Sized by its height, its width
 * following the master's aspect ratio; screen readers read it as its label.
 */
export function Wordmark({ height = 14, label }: { height?: number; label: string }) {
  // Ids per copy, as the logo's: a duplicate id resolves to the first copy.
  const id = useId().replace(/[^\w-]/g, "");
  const { x, y, width, height: boxHeight } = WORDMARK_BOX;
  return (
    <svg
      width={(height * width) / boxHeight}
      height={height}
      viewBox={`${x} ${y} ${width} ${boxHeight}`}
      role="img"
      aria-label={label}
      className="wordmark"
    >
      <defs>
        {/* "Flow" carries the mark's colors, left to right */}
        <linearGradient id={`${id}-flow`} gradientUnits="userSpaceOnUse" x1="676.62" y1="0" x2="1248.12" y2="0">
          <stop offset="0" stopColor="var(--hf-color-logo-cyan)" />
          <stop offset="0.5" stopColor="var(--hf-color-logo-violet)" />
          <stop offset="1" stopColor="var(--hf-color-logo-magenta)" />
        </linearGradient>
      </defs>
      <path fill="var(--hf-color-logo-core-violet)" d={WORDMARK_HARM} />
      <path fill={`url(#${id}-flow)`} d={WORDMARK_FLOW} />
    </svg>
  );
}
