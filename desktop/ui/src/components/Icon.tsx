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
  key: (
    <>
      <circle cx="8" cy="15.5" r="3.5" />
      <path d="m10.5 13 8-8M15.5 8l2.5 2.5M17.5 6l2 2" />
    </>
  ),
  sparkle: <path d="M12 3.5 13.8 10l6.7 2-6.7 2L12 20.5 10.2 14l-6.7-2 6.7-2L12 3.5Z" />,
  // PROTOTYPE — the Costs tab's icon (desktop/ui/src/components/prototype/costs).
  coin: (
    <>
      <ellipse cx="12" cy="7" rx="7" ry="3" />
      <path d="M5 7v5c0 1.66 3.13 3 7 3s7-1.34 7-3V7M5 12v5c0 1.66 3.13 3 7 3s7-1.34 7-3v-5" />
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
  return (
    <svg
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
      {paths[name] ?? paths.dot}
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
