// The Settings page's sections (its contents bar, deep links like
// /settings#models, and the command palette's "Settings ›" entries), in page
// order: what a new user needs first (connecting models) comes first.
export const SETTINGS_SECTIONS = [
  { id: "models", title: "Models" },
  { id: "roles", title: "Who does what" },
  { id: "reviews", title: "Code review" },
  { id: "quickReplies", title: "Quick replies" },
  { id: "notifications", title: "Notifications" },
  { id: "budget", title: "Spending limits" },
  { id: "health", title: "Status" },
] as const;

/** Older section links that now land inside another section. */
export const SECTION_ALIASES: Record<string, string> = { thinking: "roles", modes: "models" };
