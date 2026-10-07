// Keys handled inside particular surfaces (the composer, transcript search,
// the backlog table and board, the palette) rather than through the action registry.
// The help overlay lists them beside every registered action's shortcut.
import { IS_MAC } from "./platform";

export interface ShortcutDoc {
  /** Same id as the registered action it documents, when there is one (the live registration wins). */
  id: string;
  title: string;
  group: string;
  keys: string[];
}

export function contextShortcuts(mac: boolean = IS_MAC): ShortcutDoc[] {
  const mod = mac ? "⌘" : "Ctrl+";
  const shift = mac ? "⇧" : "Shift+";
  return [
    { id: "session.search", title: "Search this session’s transcript", group: "Session", keys: [`${mod}F`] },
    { id: "doc.send", title: "Send the message", group: "Session", keys: ["Enter"] },
    { id: "doc.newline", title: "New line in the message", group: "Session", keys: [`${shift}Enter`] },
    { id: "doc.searchNext", title: "Search: older / newer match", group: "Session", keys: ["Enter", `${shift}Enter`] },
    { id: "doc.searchClose", title: "Close search", group: "Session", keys: ["Esc"] },
    { id: "doc.backlogMove", title: "Move the row cursor", group: "Backlog", keys: ["J", "K", "↓", "↑"] },
    { id: "doc.boardMove", title: "Board: move across columns", group: "Backlog", keys: ["H", "L", "←", "→"] },
    { id: "doc.boardShift", title: "Board: move the card to the previous / next status", group: "Backlog", keys: [`${shift}←`, `${shift}→`] },
    { id: "doc.backlogOpen", title: "Open the task under the cursor", group: "Backlog", keys: ["Enter"] },
    { id: "doc.backlogFilter", title: "Filter tasks", group: "Backlog", keys: ["/"] },
    { id: "doc.backlogEsc", title: "Clear the filter, or close the task", group: "Backlog", keys: ["Esc"] },
    { id: "doc.taskSave", title: "Save task edits", group: "Backlog", keys: [`${mod}Enter`] },
    { id: "doc.paletteMove", title: "Move through results", group: "Command palette", keys: ["↑", "↓"] },
    { id: "doc.paletteRun", title: "Open or run the selected result", group: "Command palette", keys: ["Enter"] },
    { id: "doc.palettePrefix", title: "Only actions / sessions / tasks", group: "Command palette", keys: [">", "@", "#"] },
    { id: "doc.escape", title: "Close a dialog, menu, or this overlay", group: "General", keys: ["Esc"] },
  ];
}
