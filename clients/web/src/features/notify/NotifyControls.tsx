// Notification settings (Settings → Notifications) and the one-time sidebar
// prompt. Permission is requested only from these buttons (a user gesture).
import { dismissPrompt, enableNotifications, setNotificationsEnabled, showNotification, useNotifyState } from "./notifier";
import { toast } from "../../ui/toast";

function origin(): string {
  return typeof location === "undefined" ? "" : location.origin;
}

export async function enableFromGesture() {
  const result = await enableNotifications();
  switch (result) {
    case "granted":
      toast("Desktop notifications are on.", "info");
      return;
    case "denied":
      toast("Notifications are blocked for this site; allow them in the browser’s site settings.");
      return;
    case "insecure":
      toast("Browsers only allow notifications on https:// or localhost pages.");
      return;
    case "unsupported":
      toast("This browser doesn’t support notifications.");
      return;
    default:
      toast("Notifications were not allowed.", "info");
  }
}

export function sendTestNotification() {
  const ok = showNotification(
    { key: `test:${Date.now()}`, title: "ycc notifications work", body: "You’ll be notified like this when a session needs you." },
    { force: true },
  );
  if (!ok) toast("Couldn’t show a notification (check the browser’s and the system’s notification settings).");
}

export function NotificationControls() {
  const st = useNotifyState();
  const explain = (
    <p className="muted small">
      A notification appears when a session asks a question, finishes, or stops on an error while this tab is in the background or another
      session is open — one per event, even with several tabs open — and clicking it opens that session. While hidden, the tab checks the
      session list every 15 seconds (browsers may slow that to about once a minute for tabs hidden a long time). This setting is kept in this
      browser only. The tab title always counts sessions waiting for an answer, e.g. “(2) ycc”.
    </p>
  );
  switch (st.support) {
    case "unsupported":
      return (
        <>
          <p className="warn small">This browser doesn’t support desktop notifications.</p>
          {explain}
        </>
      );
    case "insecure":
      return (
        <>
          <p className="warn small notify-insecure">
            Browsers only allow notifications on secure pages: <span className="mono">https://</span> or{" "}
            <span className="mono">http://localhost</span> / <span className="mono">127.0.0.1</span>. This page is{" "}
            <span className="mono">{origin()}</span>, so notifications are unavailable here. To get them, open ycc through an HTTPS proxy (for
            example <span className="mono">tailscale serve</span>) or an SSH tunnel to localhost.
          </p>
          {explain}
        </>
      );
    case "denied":
      return (
        <>
          <p className="warn small">
            Notifications are blocked for this site. Allow them in the browser’s site settings (the icon left of the address), then return to
            this page.
          </p>
          {explain}
        </>
      );
    case "default":
      return (
        <>
          <div className="settings-line">
            <div>
              <strong>Desktop notifications</strong>
              <div className="muted small">The browser will ask for permission.</div>
            </div>
            <button type="button" className="btn primary" onClick={() => void enableFromGesture()}>
              Enable notifications…
            </button>
          </div>
          {explain}
        </>
      );
    case "granted":
      return (
        <>
          <div className="settings-line">
            <label className="check">
              <input type="checkbox" checked={st.enabled} onChange={(e) => setNotificationsEnabled(e.target.checked)} />
              <span>
                <strong>Desktop notifications</strong> for questions, finished turns, errors, and work loops
              </span>
            </label>
            <button type="button" className="btn" onClick={sendTestNotification}>
              Send a test notification
            </button>
          </div>
          {explain}
        </>
      );
  }
}

/** A one-time sidebar offer to turn notifications on (only while the browser could still ask). */
export function NotificationPrompt() {
  const st = useNotifyState();
  if (st.support !== "default" || st.promptDismissed || st.enabled) return null;
  return (
    <div className="notify-prompt" role="note">
      <span className="small">Get a desktop notification when a session needs you.</span>
      <span className="notify-prompt-actions">
        <button type="button" className="btn primary small" onClick={() => void enableFromGesture()}>
          Enable
        </button>
        <button type="button" className="btn ghost small" onClick={dismissPrompt}>
          Not now
        </button>
      </span>
    </div>
  );
}
