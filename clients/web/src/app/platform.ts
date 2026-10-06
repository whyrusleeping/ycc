// Platform facts the UI labels depend on (keyboard glyphs on macOS).
export const IS_MAC =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test((navigator as Navigator).platform || navigator.userAgent || "");
