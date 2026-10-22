/// <reference types="vite/client" />

declare const __APP_VERSION__: string;
/** The commit the interface was built from, e.g. `45a57bb` or `45a57bb-dirty`; empty without git information. */
declare const __APP_COMMIT__: string;
/** When the interface was built, in ISO 8601, e.g. `2026-10-22T20:30:05.123Z`: the time Vite loaded its config. */
declare const __APP_BUILT_AT__: string;
