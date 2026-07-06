// ycc web client — placeholder shell.
// The real single-page app (session list, live event stream, prod controls)
// lands in a later task. This stub only confirms the assets are served.
(function () {
  "use strict";
  document.addEventListener("DOMContentLoaded", function () {
    const app = document.getElementById("app");
    if (app) {
      const p = app.querySelector("p");
      if (p) p.textContent = "ycc web client shell — SPA coming soon.";
    }
  });
})();
