// Contextual preset actions: the project page's onboarding and docs check, and
// the backlog page's grooming and re-onboarding. Each starts the preset in its
// mode with the preset's own opening prompt and opens the session. Where a
// preset appears comes from the daemon's placement (ListModes), never from
// preset names.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useModes, useProjects } from "../../api/queries";
import { paths } from "../../app/paths";
import { track } from "../../app/analytics";
import type { Preset } from "../../gen/ycc/v1/ycc_pb";
import { MenuButton } from "../../ui/Menu";
import { toast } from "../../ui/toast";
import { placedPresets, presetActionLabel, PLACEMENT } from "./model";

function usePresetActions(project: string, placements: readonly string[]) {
  const modes = useModes();
  const projects = useProjects();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [starting, setStarting] = useState(false);
  const needsOnboarding = projects.data?.find((p) => p.name === project)?.needsOnboarding;
  const presets = placedPresets(modes.data?.presets ?? [], placements);
  const start = async (p: Preset) => {
    if (starting) return;
    setStarting(true);
    track.action("preset.start", "", { placement: p.placement });
    try {
      const resp = await client.startSession({ project, mode: p.mode, preset: p.name });
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
      navigate(paths.session(project, resp.sessionId));
    } catch (err) {
      setStarting(false);
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      toast(`Couldn’t start ${p.title || p.name}: ${errorMessage(err)}`, "error", { op: "preset.start", err });
    }
  };
  return { presets, needsOnboarding, starting, start };
}

/** Buttons for a page head (the project page): one per placed preset. */
export function PresetButtons({ project, placements }: { project: string; placements: readonly string[] }) {
  const { presets, needsOnboarding, starting, start } = usePresetActions(project, placements);
  return (
    <>
      {presets.map((p) => (
        <button
          key={p.name}
          type="button"
          className={`btn${p.placement === PLACEMENT.onboarding && needsOnboarding ? " primary" : ""}`}
          disabled={starting}
          title={p.description}
          onClick={() => void start(p)}
        >
          {presetActionLabel(p, needsOnboarding)}
        </button>
      ))}
    </>
  );
}

/** The same actions folded into a "⋯" menu, for a page head that is already full (the backlog). */
export function PresetMenu({ project, placements, ariaLabel }: { project: string; placements: readonly string[]; ariaLabel: string }) {
  const { presets, needsOnboarding, starting, start } = usePresetActions(project, placements);
  if (!presets.length) return null;
  return (
    <MenuButton
      label="⋯"
      ariaLabel={ariaLabel}
      className="btn ghost"
      items={presets.map((p) => ({
        label: presetActionLabel(p, needsOnboarding),
        title: p.description,
        disabled: starting,
        onSelect: () => void start(p),
      }))}
    />
  );
}
