import { MAX_PICTURES, type DraftPicture } from "../attachments/attachments";
import type { NewSessionDraft } from "./model";

const emptyDraft = (): NewSessionDraft => ({ project: "", mode: "", model: "", prompt: "", preset: "", pictures: [] });

interface Snapshot {
  draft: NewSessionDraft;
  projectSeeded: boolean;
  starting: boolean;
  pendingPictures: number;
  pictureError: string | null;
}

/** Tab-local owner of the draft and its previews, independent of page mounts. */
export class NewSessionDraftStore {
  private snapshot: Snapshot = { draft: emptyDraft(), projectSeeded: false, starting: false, pendingPictures: 0, pictureError: null };
  private listeners = new Set<() => void>();
  private generation = 0;

  constructor(
    private load: (files: File[], current: number) => Promise<{ pictures: DraftPicture[]; errors: string[] }>,
    private revoke: (pictures: readonly DraftPicture[]) => void,
  ) {}

  getSnapshot = () => this.snapshot;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  private publish(patch: Partial<Snapshot>) {
    this.snapshot = { ...this.snapshot, ...patch };
    for (const listener of this.listeners) listener();
  }

  update = (change: (draft: NewSessionDraft) => NewSessionDraft) => {
    this.publish({ draft: change(this.snapshot.draft) });
  };

  seedProject(project: string) {
    if (this.snapshot.projectSeeded) return;
    this.publish({ projectSeeded: true, draft: { ...this.snapshot.draft, project } });
  }

  add = async (files: File[]) => {
    if (!files.length) return;
    const generation = this.generation;
    this.publish({ pendingPictures: this.snapshot.pendingPictures + 1 });
    try {
      const { pictures, errors } = await this.load(files, this.snapshot.draft.pictures.length);
      // A completed start discarded this draft while the file was being read.
      if (generation !== this.generation) {
        this.revoke(pictures);
        return;
      }
      const current = this.snapshot.draft.pictures;
      const room = Math.max(0, MAX_PICTURES - current.length);
      this.revoke(pictures.slice(room));
      this.publish({
        draft: { ...this.snapshot.draft, pictures: [...current, ...pictures.slice(0, room)] },
        pictureError: errors.length ? errors.join(" ") : null,
      });
    } catch {
      if (generation === this.generation) this.publish({ pictureError: "Couldn’t attach the pictures." });
    } finally {
      if (generation === this.generation) this.publish({ pendingPictures: this.snapshot.pendingPictures - 1 });
    }
  };

  remove = (id: string) => {
    const current = this.snapshot.draft.pictures;
    this.revoke(current.filter((p) => p.id === id));
    this.publish({ draft: { ...this.snapshot.draft, pictures: current.filter((p) => p.id !== id) }, pictureError: null });
  };

  beginStart(): boolean {
    if (this.snapshot.starting) return false;
    this.publish({ starting: true });
    return true;
  }

  startFailed() {
    this.publish({ starting: false });
  }

  clear() {
    this.generation++;
    this.revoke(this.snapshot.draft.pictures);
    this.publish({ draft: emptyDraft(), projectSeeded: false, starting: false, pendingPictures: 0, pictureError: null });
  }
}
