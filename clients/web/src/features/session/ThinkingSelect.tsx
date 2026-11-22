// The thinking select shown beside a model select: it offers only the
// selected model's levels (plus a current level outside them, so showing a
// saved value never rewrites it) and, where the RPC allows it, a blank "model
// default" choice.
import { levelChoices, thinkingTitle, type ThinkingLevel } from "./settings";

export function ThinkingSelect({
  value,
  levels,
  onChange,
  defaultLabel,
  label,
  busy = false,
  disabled = false,
  title,
}: {
  /** The selected level; "" selects the default choice. */
  value: string;
  /** The selected model's supported levels. */
  levels: readonly ThinkingLevel[];
  onChange: (level: string) => void;
  /** Offers "" (the model's default) under this label when set. */
  defaultLabel?: string;
  /** Accessible name. */
  label: string;
  /** Saving: announced with aria-disabled so the select keeps focus. */
  busy?: boolean;
  disabled?: boolean;
  title?: string;
}) {
  return (
    <select
      aria-label={label}
      aria-disabled={busy}
      value={value}
      disabled={disabled}
      title={title}
      onChange={(e) => {
        if (!busy) onChange(e.target.value);
      }}
    >
      {defaultLabel !== undefined && <option value="">{defaultLabel}</option>}
      {levelChoices(levels, value).map((l) => (
        <option key={l} value={l}>
          {thinkingTitle(l)}
        </option>
      ))}
    </select>
  );
}
