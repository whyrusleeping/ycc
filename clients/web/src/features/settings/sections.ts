// The Settings page's sections (its contents bar, deep links like
// /settings#models, and the command palette's "Settings ›" entries).
export const SETTINGS_SECTIONS = [
  { id: "accounts", title: "Accounts" },
  { id: "notifications", title: "Notifications" },
  { id: "roles", title: "Default roles" },
  { id: "thinking", title: "Reasoning" },
  { id: "work", title: "Work" },
  { id: "reviews", title: "Review tiers" },
  { id: "models", title: "Models" },
  { id: "modes", title: "Modes" },
  { id: "budget", title: "Spend guard" },
] as const;
