// ycc web client — dependency-free vanilla SPA (design docs/design/web-client.md
// §4–§7). No framework, no build step: this file is served verbatim via go:embed.
//
// The file is split in two halves:
//   1. Pure helpers (envelope encode, incremental frame parser, event folding /
//      seq-cursor rules). These have no DOM dependency and are exported for the
//      Node test (internal/web/app_test.js). To keep that test runnable on old
//      Node (v12), the shared code sticks to ES2019 — no optional chaining (?.),
//      no nullish coalescing (??).
//   2. The DOM app (token screen, session list, session view, live stream),
//      guarded behind `typeof document !== "undefined"` so require()ing the file
//      under Node never touches the browser globals.
(function () {
  "use strict";

  // ----------------------------------------------------------------------------
  // Pure helpers
  // ----------------------------------------------------------------------------

  // utf8encode returns the UTF-8 bytes of a string as a Uint8Array, without
  // relying on TextEncoder (not a global on Node 12).
  function utf8encode(str) {
    var enc = encodeURIComponent(String(str));
    var bytes = [];
    for (var i = 0; i < enc.length; i++) {
      var c = enc.charAt(i);
      if (c === "%") {
        bytes.push(parseInt(enc.substr(i + 1, 2), 16));
        i += 2;
      } else {
        bytes.push(c.charCodeAt(0));
      }
    }
    return new Uint8Array(bytes);
  }

  // utf8decode turns a Uint8Array (or array) of UTF-8 bytes back into a string.
  function utf8decode(bytes) {
    var s = "";
    for (var i = 0; i < bytes.length; i++) {
      var b = bytes[i] & 0xff;
      s += "%" + (b < 16 ? "0" : "") + b.toString(16);
    }
    try {
      return decodeURIComponent(s);
    } catch (e) {
      var r = "";
      for (var j = 0; j < bytes.length; j++) {
        r += String.fromCharCode(bytes[j] & 0xff);
      }
      return r;
    }
  }

  // encodeRequestEnvelope frames a request message as one Connect data frame:
  // flag byte 0x00 + big-endian u32 length + JSON payload bytes.
  function encodeRequestEnvelope(obj) {
    var payload = utf8encode(JSON.stringify(obj));
    var len = payload.length;
    var out = new Uint8Array(5 + len);
    out[0] = 0x00;
    out[1] = (len >>> 24) & 0xff;
    out[2] = (len >>> 16) & 0xff;
    out[3] = (len >>> 8) & 0xff;
    out[4] = len & 0xff;
    out.set(payload, 5);
    return out;
  }

  // makeFrameParser returns a push(chunk) function that accumulates arbitrary
  // Uint8Array chunks and invokes onFrame(flag, obj, text) once per complete
  // 5-byte-enveloped message. It tolerates split headers, split payloads, and
  // multiple frames coalesced into one chunk. onFrame receives the parsed JSON
  // (obj, null when unparsable) plus the raw text.
  function makeFrameParser(onFrame) {
    var buf = new Uint8Array(0);
    return function push(chunk) {
      if (!chunk || chunk.length === 0) {
        // still allow flushing what we have below
      } else {
        var merged = new Uint8Array(buf.length + chunk.length);
        merged.set(buf, 0);
        merged.set(chunk, buf.length);
        buf = merged;
      }
      while (buf.length >= 5) {
        var flag = buf[0];
        var len = ((buf[1] << 24) | (buf[2] << 16) | (buf[3] << 8) | buf[4]) >>> 0;
        if (buf.length < 5 + len) {
          break;
        }
        var payload = buf.slice(5, 5 + len);
        var text = utf8decode(payload);
        var obj = null;
        try {
          obj = JSON.parse(text);
        } catch (e) {
          obj = null;
        }
        buf = buf.slice(5 + len);
        onFrame(flag, obj, text);
      }
    };
  }

  // parseData performs the second JSON parse of Event.dataJson. It is tolerant:
  // an absent or unparsable dataJson yields {} so callers can read fields safely.
  function parseData(ev) {
    if (!ev || typeof ev.dataJson !== "string" || ev.dataJson === "") {
      return {};
    }
    try {
      var v = JSON.parse(ev.dataJson);
      if (v && typeof v === "object") {
        return v;
      }
      return {};
    } catch (e) {
      return {};
    }
  }

  // parseSeq turns the int64-as-JSON-string seq into a Number; missing / "0" /
  // unparsable all yield 0 (transient / seq-less events).
  function parseSeq(seq) {
    if (seq === undefined || seq === null || seq === "") {
      return 0;
    }
    var n = parseInt(seq, 10);
    if (isNaN(n) || n < 0) {
      return 0;
    }
    return n;
  }

  // makeFeed creates the durable-fold state: the resume cursor (highest persisted
  // seq folded), the per-actor live-tail snapshots, and the pending ask_user gate
  // (null when no question is open) that drives the answer sheet.
  function makeFeed() {
    return { cursor: 0, tails: {}, pending: null };
  }

  // asStr coerces any value to a string ("" for null/undefined). A pure-section
  // twin of the DOM helper strOf so pendingFromAsk can normalize prompts/options
  // without reaching into the browser half.
  function asStr(v) {
    if (v === undefined || v === null) {
      return "";
    }
    return (typeof v === "string") ? v : String(v);
  }

  // pendingFromAsk normalizes a durable question_asked event into the pending-gate
  // shape { questions:[{prompt,options[]}], batch, auto, seq }. It accepts both the
  // single ({question,options?,auto?}) and batch ({questions:[{question,options?}],
  // auto?}) payloads emitted by internal/session/interaction.go (askData /
  // askManyData). Returns null when no question can be parsed.
  function pendingFromAsk(ev, seq) {
    var d = parseData(ev);
    var qs = [];
    var batch = false;
    if (d.questions && d.questions.length) {
      batch = true;
      for (var i = 0; i < d.questions.length; i++) {
        var q = d.questions[i] || {};
        qs.push({ prompt: asStr(q.question), options: (q.options || []).map(asStr) });
      }
    } else if (d.question !== undefined && d.question !== null) {
      qs.push({ prompt: asStr(d.question), options: (d.options || []).map(asStr) });
    }
    if (qs.length === 0) {
      return null;
    }
    return { questions: qs, batch: batch, auto: d.auto === true, seq: seq };
  }

  // buildAnswerBody turns a pending gate plus the collected per-question answers
  // into the request body for AnswerQuestion (single) or AnswerQuestions (batch),
  // matching docs/remote-api.md. Each entry is { optionIndex, text }: optionIndex
  // >= 0 selects a suggested option (0-based); optionIndex -1 sends text as free
  // text. The caller adds sessionId. answers is positional (answers[i] answers
  // question i).
  function buildAnswerBody(pending, answers) {
    answers = answers || [];
    function norm(a) {
      a = a || {};
      var idx = (typeof a.optionIndex === "number") ? a.optionIndex : -1;
      return { optionIndex: idx, text: asStr(a.text) };
    }
    if (pending && pending.batch) {
      var out = [];
      for (var i = 0; i < pending.questions.length; i++) {
        out.push(norm(answers[i]));
      }
      return { answers: out };
    }
    return norm(answers[0]);
  }

  // feedIngest folds one Event into the feed state and returns an action telling
  // the renderer what to do. It embodies the reconnect discipline:
  //   - transient events (turn_delta, retry, seq "0") NEVER advance the cursor;
  //   - a durable event with seq <= cursor is a replayed duplicate → skipped;
  //   - turn_delta is a full-text snapshot rendered as a replaceable live tail,
  //     cleared by {"text":"","done":true} or by the durable model_turn.
  function feedIngest(feed, ev) {
    var type = ev ? ev.type : "";
    var actor = (ev && ev.actor) ? ev.actor : "";
    var transient = !!(ev && ev.transient === true);
    var seq = parseSeq(ev ? ev.seq : 0);

    if (type === "turn_delta") {
      var d = parseData(ev);
      var text = (typeof d.text === "string") ? d.text : "";
      var done = d.done === true;
      if (done || text === "") {
        delete feed.tails[actor];
        return { kind: "clearTail", actor: actor };
      }
      feed.tails[actor] = text;
      return { kind: "tail", actor: actor, text: text };
    }

    if (transient || seq === 0) {
      // Other transient hints (e.g. retry) or any seq-less event: render nothing
      // durable and never move the cursor.
      return { kind: "transient", ev: ev };
    }

    if (seq <= feed.cursor) {
      return { kind: "duplicate", ev: ev };
    }
    feed.cursor = seq;

    if (type === "question_asked") {
      var pend = pendingFromAsk(ev, seq);
      if (pend) {
        feed.pending = pend;
      }
    } else if (type === "question_answered" || type === "session_idle" ||
               type === "session_error" || type === "session_stopped") {
      // Terminal / answered events clear any open gate. question_answered is
      // authoritative even when the answer came from another client.
      feed.pending = null;
    }

    var clearedTail = null;
    if (type === "model_turn" && Object.prototype.hasOwnProperty.call(feed.tails, actor)) {
      delete feed.tails[actor];
      clearedTail = actor;
    }
    return { kind: "append", ev: ev, clearedTail: clearedTail };
  }

  // ----------------------------------------------------------------------------
  // Export pure helpers for the Node test.
  // ----------------------------------------------------------------------------
  if (typeof module !== "undefined" && module.exports) {
    module.exports = {
      utf8encode: utf8encode,
      utf8decode: utf8decode,
      encodeRequestEnvelope: encodeRequestEnvelope,
      makeFrameParser: makeFrameParser,
      parseData: parseData,
      parseSeq: parseSeq,
      makeFeed: makeFeed,
      feedIngest: feedIngest,
      pendingFromAsk: pendingFromAsk,
      buildAnswerBody: buildAnswerBody
    };
  }

  // Nothing below runs (or is even reached in a meaningful way) under Node: bail
  // out before touching browser globals.
  if (typeof document === "undefined") {
    return;
  }

  // ----------------------------------------------------------------------------
  // App state
  // ----------------------------------------------------------------------------

  var TOKEN_KEY = "ycc_token";
  var token = "";
  try {
    token = localStorage.getItem(TOKEN_KEY) || "";
  } catch (e) {
    token = "";
  }

  var projects = [];
  var currentProject = ""; // "" = all
  var sessionsById = {};   // id -> session meta from ListSessionHistory
  var sessionState = null; // active session-view state (streaming, feed, els)

  var app = null;

  // ----------------------------------------------------------------------------
  // Small DOM helpers
  // ----------------------------------------------------------------------------

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) {
      n.className = cls;
    }
    if (text !== undefined && text !== null) {
      n.textContent = String(text); // ALWAYS textContent — never innerHTML.
    }
    return n;
  }

  function clear(node) {
    while (node.firstChild) {
      node.removeChild(node.firstChild);
    }
  }

  function strOf(v) {
    if (v === undefined || v === null) {
      return "";
    }
    if (typeof v === "string") {
      return v;
    }
    return String(v);
  }

  function firstStr(d, keys) {
    for (var i = 0; i < keys.length; i++) {
      var v = d[keys[i]];
      if (typeof v === "string" && v !== "") {
        return v;
      }
    }
    return "";
  }

  function saveToken(t) {
    token = t;
    try {
      if (t) {
        localStorage.setItem(TOKEN_KEY, t);
      } else {
        localStorage.removeItem(TOKEN_KEY);
      }
    } catch (e) { /* private mode: keep in-memory */ }
  }

  // ----------------------------------------------------------------------------
  // RPC
  // ----------------------------------------------------------------------------

  function onAuthFail() {
    saveToken("");
    teardownSession();
    location.hash = "#/";
    renderTokenScreen("Session expired — enter the token again.");
  }

  // rpc issues a unary Connect call and resolves with the parsed JSON body. A 401
  // anywhere bounces to the token screen.
  function rpc(method, body) {
    return fetch("/ycc.v1.SessionService/" + method, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + token
      },
      body: JSON.stringify(body || {})
    }).then(function (resp) {
      if (resp.status === 401) {
        onAuthFail();
        throw new Error("unauthenticated");
      }
      if (!resp.ok) {
        return resp.text().then(function (t) {
          var msg = t;
          try {
            var j = JSON.parse(t);
            if (j && j.message) {
              msg = j.message;
            }
          } catch (e) { /* keep raw */ }
          throw new Error(msg || ("HTTP " + resp.status));
        });
      }
      return resp.json();
    });
  }

  // ----------------------------------------------------------------------------
  // Token screen (§4)
  // ----------------------------------------------------------------------------

  function renderTokenScreen(note) {
    teardownSession();
    clear(app);
    var wrap = el("div", "screen center");
    wrap.appendChild(el("h1", "brand", "ycc"));
    wrap.appendChild(el("p", "muted", "Enter your access token to connect."));

    var form = el("form", "token-form");
    var input = el("input", "field");
    input.type = "password";
    input.placeholder = "access token";
    input.autocomplete = "current-password";
    input.setAttribute("aria-label", "access token");

    var btn = el("button", "btn primary", "Connect");
    btn.type = "submit";

    var err = el("p", "error");
    if (note) {
      err.textContent = note;
    }

    form.appendChild(input);
    form.appendChild(btn);
    form.appendChild(err);
    wrap.appendChild(form);
    app.appendChild(wrap);
    input.focus();

    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var candidate = input.value.trim();
      if (!candidate) {
        err.textContent = "Token required.";
        return;
      }
      btn.disabled = true;
      err.textContent = "";
      btn.textContent = "Connecting…";
      probeToken(candidate).then(function (ok) {
        if (ok) {
          saveToken(candidate);
          location.hash = "#/";
          route();
        } else {
          btn.disabled = false;
          btn.textContent = "Connect";
          err.textContent = "Invalid token.";
          input.focus();
          input.select();
        }
      }).catch(function () {
        btn.disabled = false;
        btn.textContent = "Connect";
        err.textContent = "Could not reach the daemon.";
      });
    });
  }

  // probeToken validates a candidate token via ListProjects without touching the
  // stored token or the global auth-failure path.
  function probeToken(candidate) {
    return fetch("/ycc.v1.SessionService/ListProjects", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + candidate
      },
      body: "{}"
    }).then(function (resp) {
      if (resp.status === 401) {
        return false;
      }
      if (!resp.ok) {
        throw new Error("HTTP " + resp.status);
      }
      return resp.json().then(function (j) {
        projects = (j && j.projects) ? j.projects : [];
        return true;
      });
    });
  }

  // ----------------------------------------------------------------------------
  // Session list (§7a)
  // ----------------------------------------------------------------------------

  function renderList() {
    clear(app);

    var header = el("header", "topbar");
    header.appendChild(el("span", "title", "Sessions"));
    var refresh = el("button", "btn ghost", "↻");
    refresh.title = "Refresh";
    refresh.setAttribute("aria-label", "Refresh");
    refresh.addEventListener("click", function () { loadList(); });
    header.appendChild(refresh);
    app.appendChild(header);

    var chips = el("div", "chips");
    chips.id = "chips";
    app.appendChild(chips);

    var list = el("div", "list");
    list.id = "list";
    list.appendChild(el("p", "muted pad", "Loading…"));
    app.appendChild(list);

    loadProjectsThenList();
  }

  function loadProjectsThenList() {
    rpc("ListProjects", {}).then(function (j) {
      projects = (j && j.projects) ? j.projects : [];
      renderChips();
      loadList();
    }).catch(function () {
      // Even if project listing fails, still try the session list.
      loadList();
    });
  }

  function renderChips() {
    var chips = document.getElementById("chips");
    if (!chips) {
      return;
    }
    clear(chips);
    // Hidden when only one/none project.
    if (!projects || projects.length <= 1) {
      chips.style.display = "none";
      return;
    }
    chips.style.display = "";
    chips.appendChild(chip("all", "", currentProject === ""));
    for (var i = 0; i < projects.length; i++) {
      var name = projects[i].name || "";
      chips.appendChild(chip(name, name, currentProject === name));
    }
  }

  function chip(label, value, active) {
    var c = el("button", "chip" + (active ? " active" : ""), label);
    c.addEventListener("click", function () {
      currentProject = value;
      renderChips();
      loadList();
    });
    return c;
  }

  function loadList() {
    var list = document.getElementById("list");
    if (!list) {
      return;
    }
    var body = {};
    if (currentProject) {
      body.project = currentProject;
    }
    rpc("ListSessionHistory", body).then(function (j) {
      var sessions = (j && j.sessions) ? j.sessions : [];
      sessionsById = {};
      for (var i = 0; i < sessions.length; i++) {
        if (sessions[i] && sessions[i].sessionId) {
          sessionsById[sessions[i].sessionId] = sessions[i];
        }
      }
      renderRows(sessions);
    }).catch(function (err) {
      if (err && err.message === "unauthenticated") {
        return;
      }
      clear(list);
      list.appendChild(el("p", "error pad", "Could not load sessions."));
    });
  }

  function renderRows(sessions) {
    var list = document.getElementById("list");
    if (!list) {
      return;
    }
    clear(list);
    if (!sessions || sessions.length === 0) {
      list.appendChild(el("p", "muted pad", "No sessions yet."));
      return;
    }
    for (var i = 0; i < sessions.length; i++) {
      list.appendChild(sessionRow(sessions[i]));
    }
  }

  function sessionRow(s) {
    var needs = s.waitingInput === true;
    var row = el("a", "row" + (needs ? " needs" : ""));
    row.href = "#/s/" + encodeURIComponent(s.sessionId);

    var main = el("div", "row-main");
    var title = strOf(s.title) || strOf(s.sessionId);
    main.appendChild(el("div", "row-title", title));

    var meta = el("div", "row-meta");
    var status = strOf(s.status) || "idle";
    meta.appendChild(el("span", "badge status-" + status, status));
    if (s.live === true) {
      meta.appendChild(el("span", "badge live", "live"));
    }
    var turns = parseSeq(s.turns);
    meta.appendChild(el("span", "muted", turns + (turns === 1 ? " turn" : " turns")));
    main.appendChild(meta);
    row.appendChild(main);

    if (needs) {
      row.appendChild(el("span", "needs-tag", "needs answer"));
    }
    return row;
  }

  // ----------------------------------------------------------------------------
  // Session view (§6 / §7b)
  // ----------------------------------------------------------------------------

  function renderSession(id) {
    if (sessionState && sessionState.sessionId === id) {
      return; // already showing it
    }
    teardownSession();
    clear(app);

    var meta = sessionsById[id] || null;

    var header = el("header", "topbar");
    var back = el("a", "btn ghost", "‹ Back");
    back.href = "#/";
    header.appendChild(back);
    var titleText = meta ? (strOf(meta.title) || id) : id;
    header.appendChild(el("span", "title", titleText));
    var statusEl = el("span", "conn muted", "");
    header.appendChild(statusEl);
    app.appendChild(header);

    var feed = el("div", "feed");
    feed.id = "feed";
    var rows = el("div", "rows");
    rows.id = "rows";
    var tails = el("div", "rows tails");
    tails.id = "tails";
    feed.appendChild(rows);
    feed.appendChild(tails);
    app.appendChild(feed);

    sessionState = {
      sessionId: id,
      feed: makeFeed(),
      rowsEl: rows,
      tailsEl: tails,
      feedEl: feed,
      headerEl: header,
      statusEl: statusEl,
      tailEls: {},
      open: true,
      live: false,
      streaming: false,
      cleanEnd: false,
      backoff: 1000,
      controller: null,
      reconnectTimer: null,
      empty: true,
      following: true,
      pill: null,
      inputEl: null,
      sendBtn: null,
      sheetEl: null,
      sheetSeq: null,
      sheetTimer: null,
      menuEl: null,
      menuDocHandler: null
    };

    feed.addEventListener("scroll", function () {
      var st = sessionState;
      if (!st || st.feedEl !== feed) {
        return;
      }
      st.following = nearBottom(st);
      if (st.following) {
        hidePill(st);
      }
    });

    if (meta) {
      startSessionView(sessionState, meta.live === true);
    } else {
      // Deep link without cached meta: discover live-ness before choosing a path.
      setStatus(sessionState, "loading…");
      rpc("ListSessionHistory", {}).then(function (j) {
        var sessions = (j && j.sessions) ? j.sessions : [];
        var found = null;
        for (var i = 0; i < sessions.length; i++) {
          if (sessions[i] && sessions[i].sessionId === id) {
            found = sessions[i];
            break;
          }
        }
        if (found) {
          sessionsById[id] = found;
          if (sessionState) {
            setSessionTitle(found);
          }
        }
        if (sessionState && sessionState.sessionId === id) {
          startSessionView(sessionState, !!(found && found.live === true));
        }
      }).catch(function () {
        if (sessionState && sessionState.sessionId === id) {
          startSessionView(sessionState, false);
        }
      });
    }
  }

  function setSessionTitle(meta) {
    var t = app.querySelector(".topbar .title");
    if (t) {
      t.textContent = strOf(meta.title) || meta.sessionId;
    }
  }

  function startSessionView(state, live) {
    state.live = !!live;
    if (live) {
      installLiveChrome(state);
      openStream(state);
    } else {
      setStatus(state, "");
      rpc("GetSessionTranscript", { sessionId: state.sessionId }).then(function (j) {
        if (!sessionState || sessionState !== state) {
          return;
        }
        var events = (j && j.events) ? j.events : [];
        for (var i = 0; i < events.length; i++) {
          handleEvent(state, events[i]);
        }
        if (state.empty) {
          state.rowsEl.appendChild(el("p", "muted pad", "No events."));
        }
        scrollToBottom(state);
      }).catch(function (err) {
        if (err && err.message === "unauthenticated") {
          return;
        }
        setStatus(state, "");
        state.rowsEl.appendChild(el("p", "error pad", "Could not load transcript."));
      });
    }
  }

  function setStatus(state, text) {
    if (state && state.statusEl) {
      state.statusEl.textContent = text || "";
    }
  }

  // --- live streaming ---

  function openStream(state) {
    if (!state.open || state.streaming) {
      return;
    }
    state.streaming = true;
    state.cleanEnd = false;
    setStatus(state, "live");

    var controller = null;
    try {
      controller = new AbortController();
    } catch (e) {
      controller = null;
    }
    state.controller = controller;

    var parser = makeFrameParser(function (flag, obj) {
      if (!state.open || sessionState !== state) {
        return;
      }
      if (flag === 0x00 && obj) {
        state.backoff = 1000; // healthy stream: reset backoff
        handleEvent(state, obj);
      } else if (flag === 0x02) {
        if (obj && obj.error) {
          state.cleanEnd = false;
          setStatus(state, "error: " + (obj.error.message || "stream error"));
        } else {
          state.cleanEnd = true;
        }
      }
    });

    var opts = {
      method: "POST",
      headers: {
        "Content-Type": "application/connect+json",
        "Authorization": "Bearer " + token
      },
      body: encodeRequestEnvelope({ sessionId: state.sessionId, fromSeq: state.feed.cursor })
    };
    if (controller) {
      opts.signal = controller.signal;
    }

    fetch("/ycc.v1.SessionService/Subscribe", opts).then(function (resp) {
      if (resp.status === 401) {
        onAuthFail();
        return null;
      }
      if (!resp.ok || !resp.body) {
        throw new Error("stream HTTP " + resp.status);
      }
      return pump(resp.body.getReader(), parser, state);
    }).then(function () {
      state.streaming = false;
      if (!state.open || sessionState !== state) {
        return;
      }
      if (state.cleanEnd) {
        setStatus(state, "ended");
      } else {
        scheduleReconnect(state);
      }
    }).catch(function (err) {
      state.streaming = false;
      if (!state.open || sessionState !== state) {
        return;
      }
      if (err && err.name === "AbortError") {
        return;
      }
      scheduleReconnect(state);
    });
  }

  function pump(reader, parser, state) {
    return reader.read().then(function (r) {
      if (r.done) {
        return;
      }
      if (r.value) {
        parser(r.value);
      }
      if (!state.open || sessionState !== state) {
        try { reader.cancel(); } catch (e) { /* ignore */ }
        return;
      }
      return pump(reader, parser, state);
    });
  }

  function scheduleReconnect(state) {
    if (!state.open || state.streaming) {
      return;
    }
    var wait = state.backoff || 1000;
    state.backoff = Math.min(wait * 2, 5000);
    setStatus(state, "reconnecting…");
    if (state.reconnectTimer) {
      clearTimeout(state.reconnectTimer);
    }
    state.reconnectTimer = setTimeout(function () {
      if (state.open && sessionState === state) {
        openStream(state);
      }
    }, wait);
  }

  function teardownSession() {
    var state = sessionState;
    sessionState = null;
    if (!state) {
      return;
    }
    state.open = false;
    if (state.reconnectTimer) {
      clearTimeout(state.reconnectTimer);
      state.reconnectTimer = null;
    }
    if (state.sheetTimer) {
      clearTimeout(state.sheetTimer);
      state.sheetTimer = null;
    }
    closeMenu(state);
    if (state.controller) {
      try { state.controller.abort(); } catch (e) { /* ignore */ }
    }
  }

  // ----------------------------------------------------------------------------
  // Interactive chrome (§7): input bar, answer sheet, overflow menu, jump pill,
  // toasts. All live-session only; appended inside #app so route changes clear
  // them via clear(app).
  // ----------------------------------------------------------------------------

  function installLiveChrome(state) {
    // Overflow menu button in the topbar.
    var more = el("button", "btn ghost more", "⋯");
    more.title = "Actions";
    more.setAttribute("aria-label", "Session actions");
    more.addEventListener("click", function (ev) {
      ev.stopPropagation();
      toggleMenu(state, more);
    });
    state.headerEl.appendChild(more);
    state.moreBtn = more;

    // Jump-to-latest pill (hidden until the user scrolls up during new content).
    var pill = el("button", "pill", "↓ jump to latest");
    pill.style.display = "none";
    pill.addEventListener("click", function () {
      scrollToBottom(state);
    });
    app.appendChild(pill);
    state.pill = pill;

    // Answer sheet host (populated on demand from feed.pending).
    var sheet = el("div", "sheet");
    sheet.style.display = "none";
    app.appendChild(sheet);
    state.sheetEl = sheet;

    // Sticky bottom input bar → SendInput.
    var bar = el("form", "inputbar");
    var input = el("input", "field input-field");
    input.type = "text";
    input.placeholder = "Send a message…";
    input.setAttribute("aria-label", "Message");
    input.autocomplete = "off";
    var send = el("button", "btn primary send", "Send");
    send.type = "submit";
    bar.appendChild(input);
    bar.appendChild(send);
    app.appendChild(bar);
    state.inputEl = input;
    state.sendBtn = send;
    bar.addEventListener("submit", function (ev) {
      ev.preventDefault();
      sendInput(state);
    });
  }

  function sendInput(state) {
    if (!state.inputEl) {
      return;
    }
    var text = state.inputEl.value.trim();
    if (!text) {
      return;
    }
    state.inputEl.disabled = true;
    state.sendBtn.disabled = true;
    rpc("SendInput", { sessionId: state.sessionId, text: text }).then(function () {
      if (sessionState === state) {
        state.inputEl.value = "";
      }
    }).catch(function (err) {
      if (err && err.message === "unauthenticated") {
        return;
      }
      toast((err && err.message) || "Could not send.");
    }).then(function () {
      if (sessionState === state && state.inputEl) {
        state.inputEl.disabled = false;
        state.sendBtn.disabled = false;
        state.inputEl.focus();
      }
    });
  }

  // --- overflow menu ---

  function toggleMenu(state, anchor) {
    if (state.menuEl) {
      closeMenu(state);
      return;
    }
    var menu = el("div", "menu");
    menu.appendChild(menuItem("Interrupt", function () {
      closeMenu(state);
      controlAction(state, "Interrupt", "Interrupt sent");
    }));
    menu.appendChild(menuItem("Resume", function () {
      closeMenu(state);
      controlAction(state, "Resume", "Resume sent");
    }));
    var stop = menuItem("Stop session", null);
    stop.classList.add("danger");
    stop.addEventListener("click", function () {
      if (stop.getAttribute("data-armed") === "1") {
        closeMenu(state);
        controlAction(state, "StopSession", "Stop sent");
      } else {
        stop.setAttribute("data-armed", "1");
        stop.textContent = "Tap again to stop";
      }
    });
    menu.appendChild(stop);
    app.appendChild(menu);
    state.menuEl = menu;

    // Close on tap-outside / Escape.
    state.menuDocHandler = function (ev) {
      if (ev.type === "keydown") {
        if (ev.key === "Escape" || ev.keyCode === 27) {
          closeMenu(state);
        }
        return;
      }
      if (state.menuEl && !state.menuEl.contains(ev.target) && ev.target !== anchor) {
        closeMenu(state);
      }
    };
    // Defer so the opening click doesn't immediately close it.
    setTimeout(function () {
      if (state.menuDocHandler) {
        document.addEventListener("click", state.menuDocHandler, true);
        document.addEventListener("keydown", state.menuDocHandler, true);
      }
    }, 0);
  }

  function menuItem(label, onClick) {
    var b = el("button", "menu-item", label);
    if (onClick) {
      b.addEventListener("click", onClick);
    }
    return b;
  }

  function closeMenu(state) {
    if (state.menuDocHandler) {
      document.removeEventListener("click", state.menuDocHandler, true);
      document.removeEventListener("keydown", state.menuDocHandler, true);
      state.menuDocHandler = null;
    }
    if (state.menuEl && state.menuEl.parentNode) {
      state.menuEl.parentNode.removeChild(state.menuEl);
    }
    state.menuEl = null;
  }

  function controlAction(state, method, okMsg) {
    rpc(method, { sessionId: state.sessionId }).then(function () {
      toast(okMsg);
    }).catch(function (err) {
      if (err && err.message === "unauthenticated") {
        return;
      }
      toast((err && err.message) || (method + " failed"));
    });
  }

  // --- answer sheet ---

  function scheduleSheetSync(state) {
    if (state.sheetTimer) {
      return;
    }
    state.sheetTimer = setTimeout(function () {
      state.sheetTimer = null;
      if (sessionState === state) {
        syncSheet(state);
      }
    }, 50);
  }

  function syncSheet(state) {
    if (!state.sheetEl) {
      return;
    }
    var p = state.feed.pending;
    if (!p || p.auto) {
      hideSheet(state);
      return;
    }
    if (state.sheetSeq === p.seq && state.sheetEl.style.display !== "none") {
      return; // already showing this gate — don't churn / clobber typed text
    }
    buildSheet(state, p);
    state.sheetSeq = p.seq;
  }

  function hideSheet(state) {
    if (state.sheetEl) {
      clear(state.sheetEl);
      state.sheetEl.style.display = "none";
    }
    state.sheetSeq = null;
  }

  // buildSheet renders the bottom-sheet answer picker for the pending gate. It
  // must not move the feed's scroll position (it is a fixed overlay).
  function buildSheet(state, pending) {
    var sheet = state.sheetEl;
    clear(sheet);
    sheet.style.display = "";

    var head = el("div", "sheet-head", pending.batch ? "Answer these questions" : "Answer");
    sheet.appendChild(head);

    var body = el("div", "sheet-body");
    sheet.appendChild(body);

    // Per-question local selection state (batch collects; single sends on tap).
    var selections = [];
    for (var qi = 0; qi < pending.questions.length; qi++) {
      selections.push({ optionIndex: -1, textEl: null });
    }

    for (var i = 0; i < pending.questions.length; i++) {
      (function (idx) {
        var q = pending.questions[idx];
        var qWrap = el("div", "sheet-q");
        qWrap.appendChild(el("div", "q-prompt", q.prompt || "(question)"));

        var optWrap = el("div", "sheet-opts");
        var buttons = [];
        for (var o = 0; o < q.options.length; o++) {
          (function (optIdx) {
            var ob = el("button", "opt-btn", q.options[optIdx]);
            ob.type = "button";
            ob.addEventListener("click", function () {
              if (pending.batch) {
                selections[idx].optionIndex = optIdx;
                for (var b = 0; b < buttons.length; b++) {
                  if (b === optIdx) {
                    buttons[b].classList.add("selected");
                  } else {
                    buttons[b].classList.remove("selected");
                  }
                }
              } else {
                answerSingle(state, optIdx, "");
              }
            });
            buttons.push(ob);
            optWrap.appendChild(ob);
          })(o);
        }
        if (q.options.length) {
          qWrap.appendChild(optWrap);
        }

        var free = el("input", "field sheet-free");
        free.type = "text";
        free.placeholder = "Type an answer…";
        free.setAttribute("aria-label", "Free-text answer");
        selections[idx].textEl = free;
        qWrap.appendChild(free);

        if (!pending.batch) {
          // Single question: submit free text with Enter or the Send button.
          free.addEventListener("keydown", function (ev) {
            if (ev.key === "Enter" || ev.keyCode === 13) {
              ev.preventDefault();
              var t = free.value.trim();
              if (t) {
                answerSingle(state, -1, t);
              }
            }
          });
          var one = el("button", "btn primary sheet-send", "Send answer");
          one.type = "button";
          one.addEventListener("click", function () {
            var t = free.value.trim();
            if (t) {
              answerSingle(state, -1, t);
            }
          });
          qWrap.appendChild(one);
        }

        body.appendChild(qWrap);
      })(i);
    }

    if (pending.batch) {
      var sendAll = el("button", "btn primary sheet-send", "Send answers");
      sendAll.type = "button";
      sendAll.addEventListener("click", function () {
        var answers = [];
        for (var s = 0; s < selections.length; s++) {
          answers.push({
            optionIndex: selections[s].optionIndex,
            text: selections[s].textEl ? selections[s].textEl.value : ""
          });
        }
        answerBatch(state, answers);
      });
      sheet.appendChild(sendAll);
    }

    state.sheetControls = sheet.querySelectorAll("button, input");
  }

  function disableSheet(state, on) {
    var ctrls = state.sheetControls;
    if (!ctrls) {
      return;
    }
    for (var i = 0; i < ctrls.length; i++) {
      ctrls[i].disabled = on;
    }
  }

  function answerSingle(state, optionIndex, text) {
    var body = buildAnswerBody(state.feed.pending, [{ optionIndex: optionIndex, text: text }]);
    body.sessionId = state.sessionId;
    sendAnswer(state, "AnswerQuestion", body);
  }

  function answerBatch(state, answers) {
    var body = buildAnswerBody(state.feed.pending, answers);
    body.sessionId = state.sessionId;
    sendAnswer(state, "AnswerQuestions", body);
  }

  // sendAnswer posts an answer and leaves the sheet up: the durable
  // question_answered event dismisses it (also covering answers from another
  // client). On error it re-enables the controls and toasts the message.
  function sendAnswer(state, method, body) {
    disableSheet(state, true);
    rpc(method, body).then(function () {
      // Success: wait for question_answered to dismiss the sheet.
    }).catch(function (err) {
      if (err && err.message === "unauthenticated") {
        return;
      }
      if (sessionState === state) {
        disableSheet(state, false);
      }
      toast((err && err.message) || "Could not answer.");
    });
  }

  // --- jump-to-latest pill ---

  function showPill(state) {
    if (state.pill) {
      state.pill.style.display = "";
    }
  }

  function hidePill(state) {
    if (state.pill) {
      state.pill.style.display = "none";
    }
  }

  // --- toasts ---

  function toast(msg) {
    if (typeof document === "undefined") {
      return;
    }
    var host = document.getElementById("toasts");
    if (!host) {
      host = el("div", "toasts");
      host.id = "toasts";
      document.body.appendChild(host);
    }
    var t = el("div", "toast", strOf(msg) || "Error");
    host.appendChild(t);
    setTimeout(function () {
      if (t.parentNode) {
        t.parentNode.removeChild(t);
      }
    }, 4000);
  }

  // --- event → DOM ---

  function handleEvent(state, ev) {
    var action = feedIngest(state.feed, ev);
    switch (action.kind) {
      case "append":
        if (action.clearedTail) {
          removeTail(state, action.clearedTail);
        }
        var node = renderEventNode(ev);
        if (node) {
          appendRow(state, node);
        }
        break;
      case "tail":
        updateTail(state, action.actor, action.text);
        break;
      case "clearTail":
        removeTail(state, action.actor);
        break;
      default:
        // duplicate / transient: nothing to render.
        break;
    }
    // The answer sheet is a projection of feed.pending; re-sync it (debounced) so
    // an ask raises it and a question_answered (from any client) dismisses it,
    // without a flash when an ask+answer replay in quick succession.
    if (state.live) {
      scheduleSheetSync(state);
    }
  }

  function nearBottom(state) {
    var f = state.feedEl;
    return (f.scrollHeight - f.scrollTop - f.clientHeight) < 80;
  }

  function scrollToBottom(state) {
    state.feedEl.scrollTop = state.feedEl.scrollHeight;
    state.following = true;
    hidePill(state);
  }

  function appendRow(state, node) {
    if (state.empty) {
      clear(state.rowsEl); // drop any "No events." placeholder
      state.empty = false;
    }
    state.rowsEl.appendChild(node);
    if (state.following) {
      scrollToBottom(state);
    } else {
      showPill(state);
    }
  }

  function updateTail(state, actor, text) {
    var row = state.tailEls[actor];
    if (!row) {
      row = bubble("agent", actor || "agent", "", false);
      row.classList.add("streaming");
      state.tailEls[actor] = row;
      state.tailsEl.appendChild(row);
    }
    var body = row.querySelector(".bubble-body");
    if (body) {
      body.textContent = text;
    }
    if (state.following) {
      scrollToBottom(state);
    } else {
      showPill(state);
    }
  }

  function removeTail(state, actor) {
    var row = state.tailEls[actor];
    if (row && row.parentNode) {
      row.parentNode.removeChild(row);
    }
    delete state.tailEls[actor];
  }

  // renderEventNode folds one Event into a DOM node, or null when it renders
  // nothing (plumbing rows). All text goes in via textContent.
  function renderEventNode(ev) {
    var d = parseData(ev);
    var type = ev.type;
    switch (type) {
      case "user_input": {
        var utxt = strOf(d.text);
        if (!utxt) {
          return null;
        }
        return bubble("user", ev.actor || "user", utxt, d.queued === true);
      }
      case "model_turn": {
        var mtxt = strOf(d.text);
        if (!mtxt) {
          return null; // tool-only turn folds away
        }
        return bubble("agent", ev.actor || "agent", mtxt, false);
      }
      case "thinking":
        return foldRow("💭 thinking", strOf(d.text), "thinking");
      case "tool_call": {
        var name = strOf(d.name) || "tool";
        return foldRow("🔧 " + name, prettyArgs(d.args), "tool");
      }
      case "tool_result": {
        var isErr = (d.is_error === true) || (d.error === true) || (strOf(d.error) === "true");
        var head = isErr ? "⚠ tool error" : "↩ tool result";
        return foldRow(head, strOf(d.result), isErr ? "tool err" : "tool");
      }
      case "question_asked":
        return questionNode(d);
      case "question_answered":
        return systemRow("answer", firstStr(d, ["answer", "text"]));
      case "session_idle":
        return banner("done", firstStr(d, ["report", "text"]), "idle");
      case "session_error":
        return banner("error", firstStr(d, ["msg", "text"]), "error");
      case "user_input_delivered":
        return null; // plumbing
      default:
        return systemRow(prettyType(type), firstStr(d, ["text", "report", "msg", "plan", "summary", "role", "sha", "task"]));
    }
  }

  function prettyType(t) {
    return String(t || "event").replace(/_/g, " ");
  }

  function bubble(side, actor, text, queued) {
    var wrap = el("div", "msg " + side);
    var head = el("div", "bubble-head");
    head.appendChild(el("span", "actor", actor));
    if (queued) {
      head.appendChild(el("span", "tag queued", "queued"));
    }
    wrap.appendChild(head);
    wrap.appendChild(el("div", "bubble bubble-body", text));
    return wrap;
  }

  // foldRow renders a collapsed one-liner that expands to its body on tap, using
  // native <details>/<summary> (no JS). When there is no body it degrades to a
  // plain non-expandable row.
  function foldRow(summaryText, body, extraCls) {
    if (!body) {
      var flat = el("div", "sysrow " + (extraCls || ""));
      flat.appendChild(el("span", "sys-line", summaryText));
      return flat;
    }
    var det = el("details", "fold " + (extraCls || ""));
    var sum = el("summary", "", summaryText);
    det.appendChild(sum);
    det.appendChild(el("pre", "fold-body", body));
    return det;
  }

  function systemRow(label, detail) {
    var row = el("div", "sysrow");
    row.appendChild(el("span", "sys-line", detail ? (label + " — " + detail) : label));
    return row;
  }

  function banner(kind, text, cls) {
    var b = el("div", "banner " + (cls || ""));
    b.appendChild(el("span", "banner-label", kind));
    if (text) {
      b.appendChild(el("div", "banner-body", text));
    }
    return b;
  }

  // questionNode renders an ask_user gate as a read-only "needs answer" block:
  // the question(s) and any options as a static list. Answering lands in a later
  // task.
  function questionNode(d) {
    var wrap = el("div", "banner needs-answer");
    wrap.appendChild(el("span", "banner-label", "needs answer"));

    var qs = [];
    if (d.questions && d.questions.length) {
      for (var i = 0; i < d.questions.length; i++) {
        var q = d.questions[i] || {};
        qs.push({ prompt: strOf(q.question), options: q.options || [] });
      }
    } else if (d.question) {
      qs.push({ prompt: strOf(d.question), options: d.options || [] });
    }

    if (qs.length === 0) {
      wrap.appendChild(el("div", "banner-body", "(question)"));
      return wrap;
    }

    for (var k = 0; k < qs.length; k++) {
      var item = qs[k];
      wrap.appendChild(el("div", "q-prompt", item.prompt));
      if (item.options && item.options.length) {
        var ul = el("ul", "q-options");
        for (var o = 0; o < item.options.length; o++) {
          ul.appendChild(el("li", "", strOf(item.options[o])));
        }
        wrap.appendChild(ul);
      }
    }
    return wrap;
  }

  function prettyArgs(args) {
    if (args === undefined || args === null) {
      return "";
    }
    if (typeof args === "string") {
      try {
        return JSON.stringify(JSON.parse(args), null, 2);
      } catch (e) {
        return args;
      }
    }
    try {
      return JSON.stringify(args, null, 2);
    } catch (e) {
      return String(args);
    }
  }

  // ----------------------------------------------------------------------------
  // Routing + bootstrap
  // ----------------------------------------------------------------------------

  function route() {
    var hash = location.hash || "#/";
    if (!token) {
      renderTokenScreen();
      return;
    }
    if (hash.indexOf("#/s/") === 0) {
      renderSession(decodeURIComponent(hash.slice(4)));
    } else {
      teardownSession();
      renderList();
    }
  }

  function onVisible() {
    if (document.visibilityState !== "visible") {
      return;
    }
    if (sessionState && sessionState.open && !sessionState.streaming) {
      if (sessionState.reconnectTimer) {
        clearTimeout(sessionState.reconnectTimer);
        sessionState.reconnectTimer = null;
      }
      sessionState.backoff = 1000;
      openStream(sessionState);
    } else if (!sessionState && token && (location.hash === "" || location.hash === "#/")) {
      loadList();
    }
  }

  function boot() {
    app = document.getElementById("app");
    window.addEventListener("hashchange", route);
    window.addEventListener("focus", onVisible);
    document.addEventListener("visibilitychange", onVisible);
    route();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();
