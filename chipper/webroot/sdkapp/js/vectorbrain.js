// vectorbrain.js - Vector Brain dashboard for sdkapp/settings.html.
//
// Owns the new card surface only: identity header, battery readout, camera frame,
// live log with its level chips, and the settings drawer. The sixteen legacy tiles
// inside #vbDrawer are still driven by main.js / settings.js / faces.js / heyvector.js,
// so nothing in here may throw into those: every entry point is guarded and a missing
// element means "this page does not have that part" rather than an error.
//
// Reuses two existing globals instead of reimplementing them:
//   getBatteryStatus(serial)     - sdkapp/js/common.js
//   getBatteryPercentage(volts)  - ../js/battery.js  (mirrored by bwBatteryPercent in
//                                  batterywatchdog.go; keep the single source of truth)

(function () {
  "use strict";

  var STATUS_POLL_MS = 2000;
  var BATTERY_POLL_MS = 5000;
  var LOG_POLL_MS = 1000;
  var LOG_MAX_ROWS = 12;
  var CAM_RETRY_MS = 20000;
  // A multipart camera response that ends cleanly fires no "error" event, so a dead
  // stream has to be spotted by frames drying up instead.
  var CAM_STALL_MS = 12000;
  // A stream that never delivers its FIRST frame - which is what a docked, sleeping
  // robot does - also fires no "error"; the request just hangs open. Without a
  // separate deadline for that case camFrames stays 0, the camFrames >= 2 guard in
  // camStalled never trips, and the panel sits black with no explanation.
  var CAM_FIRST_FRAME_MS = 8000;
  var STATUS_FAIL_LIMIT = 3;
  // Matches the default of APIConfig.Battery.GoHomePercent (batterywatchdog.go), so
  // the readout turns red before the watchdog sends the robot home, not after.
  var BATT_LOW_PCT = 25;

  // Built from char codes rather than written literally so this file stays pure ASCII:
  // settings.html declares no <meta charset>, so nothing here depends on how it is decoded.
  var MIDDOT = String.fromCharCode(183);
  var EMDASH = String.fromCharCode(8212);
  var BOLT = String.fromCharCode(9889);

  // Matches the plan's tokens: accent #00ff80, alert #ff6b6b, amber for the middle state.
  // The status values the server can report. Anything else is folded into
  // "unknown" so the value is safe to interpolate into a class name.
  var KNOWN_STATUS = {
    online: true,
    offline: true,
    disconnected: true,
    unknown: true
  };

  // 1x1 transparent GIF. Assigning this aborts an in-flight multipart camera stream
  // without the browser re-requesting the document URL, which img.src = "" does.
  var BLANK_GIF =
    "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";

  // main.js:13 already assigns the implicit global "esn"; keep our own scoped copy.
  var vbEsn = "";
  try {
    vbEsn = new URLSearchParams(window.location.search).get("serial") || "";
  } catch (e) {
    vbEsn = "";
  }

  function el(id) {
    return document.getElementById(id);
  }

  // Server-supplied strings end up in class names (level-INFO, comp-stt); keep them
  // to a safe alphabet so a stray value cannot smuggle extra selectors in.
  function safeToken(v) {
    if (!v) {
      return "";
    }
    return String(v).replace(/[^A-Za-z0-9_-]/g, "");
  }

  function two(n) {
    return ("0" + n).slice(-2);
  }

  // webroot/js/main.js defines logTimeString, but that file is not loaded on this page.
  // Use it when something else has provided it, otherwise format locally.
  function timeString(t) {
    if (typeof window.logTimeString === "function") {
      try {
        return window.logTimeString(t);
      } catch (e) {
        // fall through to the local formatter
      }
    }
    var d = new Date(t);
    if (isNaN(d.getTime())) {
      return "";
    }
    return two(d.getHours()) + ":" + two(d.getMinutes()) + ":" + two(d.getSeconds());
  }

  function fetchJSON(url) {
    return fetch(url).then(function (response) {
      if (!response.ok) {
        throw new Error("http " + response.status);
      }
      return response.json();
    });
  }

  // Self-rescheduling poller. Chains on completion rather than using setInterval so a
  // slow call (get_battery opens an SDK connection and waits up to 15s) can never stack.
  function makePoller(fn, ms) {
    var timer = null;
    var busy = false;

    function schedule(delay) {
      if (timer !== null) {
        clearTimeout(timer);
      }
      timer = setTimeout(run, delay);
    }

    function run() {
      timer = null;
      if (busy || document.hidden) {
        schedule(ms);
        return;
      }
      busy = true;
      var done = function () {
        busy = false;
        schedule(ms);
      };
      var result = null;
      try {
        result = fn();
      } catch (e) {
        result = null;
      }
      if (result && typeof result.then === "function") {
        result.then(done, done);
      } else {
        done();
      }
    }

    return {
      start: function () {
        schedule(0);
      },
      kick: function () {
        if (!busy) {
          schedule(0);
        }
      }
    };
  }

  // ---------------------------------------------------------------- identity + status

  var statusFails = 0;
  var lastStatus = "unknown";

  function lastHeardText(bot) {
    if (!bot || typeof bot.timesince !== "number" || bot.timesince < 0) {
      return "last heard unknown";
    }
    return "last heard " + bot.timesince + "s ago";
  }

  // The unwired items get the same amber TODO chip the header readouts use, rather
  // than the words "TODO" inline in the sentence.
  function todoBadge() {
    var badge = document.createElement("span");
    badge.className = "vb-badge vb-badge-todo";
    badge.textContent = "TODO";
    return badge;
  }

  function renderSubline(bot) {
    var sub = el("vbSubline");
    if (!sub) {
      return;
    }
    sub.textContent = "";
    if (!vbEsn) {
      sub.appendChild(
        document.createTextNode(
          "no robot selected " + MIDDOT + " open this page as settings.html?serial=<esn>"
        )
      );
      return;
    }
    var sep = " " + MIDDOT + " ";
    var frag = document.createDocumentFragment();
    frag.appendChild(document.createTextNode((bot && bot.ip ? bot.ip : "no ip") + sep + "firmware "));
    frag.appendChild(todoBadge());
    frag.appendChild(document.createTextNode(sep + "uptime "));
    frag.appendChild(todoBadge());
    frag.appendChild(document.createTextNode(sep + lastHeardText(bot)));
    sub.appendChild(frag);
  }

  function renderStatus(bot) {
    var serialEl = el("vbSerial");
    if (serialEl) {
      serialEl.textContent = vbEsn || "";
    }

    var status = bot && bot.status ? safeToken(bot.status) : "unknown";
    if (!KNOWN_STATUS[status]) {
      status = "unknown";
    }
    lastStatus = status;

    var textEl = el("vbStatusText");
    if (textEl) {
      // Without ?serial= nothing on this page can resolve to a robot; say so instead
      // of reporting "Unknown" forever.
      textEl.textContent = vbEsn
        ? status.charAt(0).toUpperCase() + status.slice(1)
        : "No serial";
    }

    var pill = el("vbStatusPill");
    if (pill) {
      // style.css carries .vb-pill-offline / -disconnected / -unknown; the bare
      // .vb-pill rule is already the online (green) state, so vb-pill-online
      // deliberately has no rule of its own. The dot is coloured by those same
      // rules, which is why nothing is set inline here.
      pill.className = "vb-pill vb-pill-" + status;
      pill.setAttribute("data-status", status);
    }

    renderSubline(bot);
  }

  function pollStatus() {
    if (!el("vbStatusPill") && !el("vbStatusText") && !el("vbSubline")) {
      return null;
    }
    if (!vbEsn) {
      renderStatus(null);
      return null;
    }
    return fetchJSON("/api/get_bot_status")
      .then(function (bots) {
        statusFails = 0;
        var bot = null;
        // The endpoint answers the JSON literal null when no robots are known.
        if (Array.isArray(bots)) {
          for (var i = 0; i < bots.length; i++) {
            if (bots[i] && bots[i].esn === vbEsn) {
              bot = bots[i];
              break;
            }
          }
        }
        renderStatus(bot);
        maybeRetryCam();
      })
      .catch(function () {
        // Hold the last good reading briefly, then admit we have lost the server
        // rather than leaving a stale "Online" pill up forever.
        statusFails++;
        if (statusFails >= STATUS_FAIL_LIMIT) {
          renderStatus(null);
        }
      });
  }

  // ------------------------------------------------------------------------- battery

  function renderBattery(status) {
    var pctEl = el("vbBattPct");
    var voltsEl = el("vbBattVolts");

    if (!status) {
      if (pctEl) {
        pctEl.textContent = "--";
        pctEl.classList.remove("vb-batt-low");
      }
      if (voltsEl) {
        voltsEl.textContent = "";
      }
      return;
    }

    var volts = typeof status.battery_volts === "number" ? status.battery_volts : null;

    var percent = null;
    // Only called with a real reading. getBatteryPercentage answers a flat 70 for a
    // missing voltage (battery.js "assume a reasonable battery percentage"), and the
    // protobuf JSON omits battery_volts entirely for a genuine 0.0V, so feeding it a
    // null would print a fabricated 70% on exactly the robots that are in trouble.
    if (volts !== null && typeof window.getBatteryPercentage === "function") {
      try {
        // Deliberately reused, not reimplemented: the Go watchdog mirrors this curve.
        percent = window.getBatteryPercentage(volts);
      } catch (e) {
        percent = null;
      }
    }

    if (pctEl) {
      pctEl.textContent = percent === null ? "--" : percent + "%";
      pctEl.classList.toggle("vb-batt-low", percent !== null && percent <= BATT_LOW_PCT);
    }

    if (voltsEl) {
      var parts = [];
      if (volts !== null) {
        parts.push(volts.toFixed(2) + "V");
      }
      if (status.is_on_charger_platform) {
        parts.push(BOLT);
      }
      voltsEl.textContent = parts.join(" ");
    }
  }

  function pollBattery() {
    if (!vbEsn || (!el("vbBattPct") && !el("vbBattVolts"))) {
      return null;
    }
    if (typeof window.getBatteryStatus !== "function") {
      return null;
    }
    // getBatteryStatus calls .json() unconditionally, so it throws on the plain-text
    // "error: ..." body /api-sdk/ returns for an unreachable robot.
    return Promise.resolve()
      .then(function () {
        return window.getBatteryStatus(vbEsn);
      })
      .then(function (status) {
        renderBattery(status && typeof status === "object" ? status : null);
      })
      .catch(function () {
        renderBattery(null);
      });
  }

  // -------------------------------------------------------------------------- camera

  var camStarted = false;
  var camErrored = false;
  var camStopping = false;
  var camLastTry = 0;
  var camFrames = 0;
  var camLastFrame = 0;
  var camFailures = 0;

  function setCamOff(off) {
    var frame = el("vbCamFrame");
    if (frame) {
      frame.classList.toggle("vb-cam-off", !!off);
    }
  }

  function startCam() {
    var img = el("vbCam");
    if (!img || !vbEsn) {
      return;
    }
    // Always cache-busted: a retry must issue a fresh request rather than let the
    // browser reuse the aborted one.
    var src = "/cam-stream?serial=" + encodeURIComponent(vbEsn) + "&_=" + Date.now();
    camStarted = true;
    camStopping = false;
    camErrored = false;
    camFrames = 0;
    camLastFrame = Date.now();
    camLastTry = camLastFrame;
    setCamOff(false);
    img.src = src;
  }

  function stopCam() {
    var img = el("vbCam");
    if (!img) {
      return;
    }
    // Aborts the multipart response so the Go handler's r.Context().Done() branch runs
    // and calls EnableImageStreaming(false).
    camStopping = true;
    try {
      img.src = BLANK_GIF;
      img.removeAttribute("src");
    } catch (e) {
      // ignore
    }
    camStarted = false;
    camFrames = 0;
    // Left in the errored state on purpose: every restart path (pageshow after a
    // bfcache restore, the tab becoming visible, the status poll) checks this flag,
    // and a torn-down stream that claims to be healthy never comes back.
    camErrored = true;
    setCamOff(true);
  }

  // A /cam-stream response that ends cleanly - which is what the Go handler does to
  // the first request as soon as a second tab or a reload asks for the same robot -
  // fires no "error" event, so the browser just keeps painting the last frame.
  // Frames arriving is the only reliable liveness signal. The camFrames >= 2 guard
  // means a browser that fires "load" once for the whole stream instead of once per
  // part is never falsely restarted.
  function camStalled() {
    return (
      camStarted &&
      !camStopping &&
      camFrames >= 2 &&
      Date.now() - camLastFrame > CAM_STALL_MS
    );
  }

  function camNeverArrived() {
    return (
      camStarted &&
      !camStopping &&
      camFrames === 0 &&
      Date.now() - camLastTry > CAM_FIRST_FRAME_MS
    );
  }

  function maybeRetryCam() {
    if (document.hidden || lastStatus !== "online") {
      return;
    }
    if (camStalled() || camNeverArrived()) {
      if (!camErrored) {
        camFailures++;
      }
      camErrored = true;
      setCamOff(true);
    }
    if (!camErrored) {
      return;
    }
    // Back off as failures repeat. Every attempt against a robot whose camera will
    // not wake holds a hung request and a gRPC stream open server-side, so retrying
    // at a flat 20s forever is not free. Caps at six times the base interval.
    if (Date.now() - camLastTry < CAM_RETRY_MS * Math.min(camFailures || 1, 6)) {
      return;
    }
    startCam();
  }

  function initCam() {
    var img = el("vbCam");
    if (!img) {
      return;
    }
    img.addEventListener("error", function () {
      if (camStopping) {
        return;
      }
      camErrored = true;
      setCamOff(true);
    });
    img.addEventListener("load", function () {
      // stopCam assigns a blank GIF to abort the stream; that load is not a frame.
      if (camStopping) {
        return;
      }
      camFrames++;
      camLastFrame = Date.now();
      camErrored = false;
      camFailures = 0;
      setCamOff(false);
    });
    startCam();
  }

  // ------------------------------------------------------------------------ live log

  var logLevel = "";
  var logSince = 0;
  // Keys of the entries already shown at exactly logSince, see pollLogs.
  var logBoundary = {};
  var logRows = [];
  // Bumped by selectChip so a response for the previous filter, already in flight
  // when the chip was clicked, can be discarded instead of polluting the buffer.
  var logGen = 0;

  function entryKey(entry) {
    return [entry.t, entry.level, entry.comp, entry.bot, entry.msg].join("\u0001");
  }

  function makeCell(className, text) {
    var span = document.createElement("span");
    span.className = className;
    span.textContent = text;
    return span;
  }

  function makeLogRow(entry) {
    var row = document.createElement("div");
    row.className = "vb-log-row";

    row.appendChild(makeCell("vb-lt", typeof entry.t === "number" ? timeString(entry.t) : ""));

    var level = safeToken(entry.level);
    row.appendChild(makeCell("vb-ll" + (level ? " level-" + level : ""), level));

    var comp = safeToken(entry.comp);
    row.appendChild(makeCell("vb-lc" + (comp ? " comp-" + comp : ""), comp || EMDASH));

    row.appendChild(makeCell("vb-lb", entry.bot ? String(entry.bot) : EMDASH));
    row.appendChild(makeCell("vb-lm", entry.msg ? String(entry.msg) : ""));

    return row;
  }

  function renderLogs() {
    var body = el("vbLogBody");
    if (!body) {
      return;
    }
    // Every tick rebuilds the rows; only re-pin to the bottom if the reader was
    // already there, otherwise scrolling back through history is impossible.
    var pinned = body.scrollHeight - body.scrollTop - body.clientHeight <= 4;
    var prevTop = body.scrollTop;
    body.textContent = "";

    // A chip click clears the buffer, so the box would otherwise sit blank
    // until the next poll returns.
    if (logRows.length === 0) {
      body.appendChild(makeCell("vb-log-empty", "waiting for log output..."));
      return;
    }

    var frag = document.createDocumentFragment();
    for (var i = 0; i < logRows.length; i++) {
      frag.appendChild(makeLogRow(logRows[i]));
    }
    body.appendChild(frag);
    body.scrollTop = pinned ? body.scrollHeight : prevTop;
  }

  function pollLogs() {
    if (!el("vbLogBody")) {
      return null;
    }
    var gen = logGen;
    // The server filters with a strict "e.TimeMS > since", so anything written in the
    // same millisecond as the newest entry of a response - but after that response was
    // built - would be skipped forever. Ask from one millisecond earlier and drop the
    // entries already shown at that boundary.
    var since = logSince > 0 ? logSince - 1 : 0;
    return fetchJSON(
      "/api/get_logs_json?level=" + encodeURIComponent(logLevel) + "&since=" + since
    )
      .then(function (logs) {
        // A chip click since this request went out already cleared the buffer for a
        // different level; this response belongs to the old filter.
        if (gen !== logGen) {
          return;
        }
        if (!Array.isArray(logs) || logs.length === 0) {
          return;
        }
        var added = 0;
        var maxT = logSince;
        for (var i = 0; i < logs.length; i++) {
          var entry = logs[i];
          if (!entry) {
            continue;
          }
          var t = typeof entry.t === "number" ? entry.t : 0;
          if (t < logSince) {
            continue;
          }
          if (t === logSince && logBoundary[entryKey(entry)]) {
            continue;
          }
          logRows.push(entry);
          added++;
          if (t > maxT) {
            maxT = t;
          }
        }
        if (added === 0) {
          return;
        }
        if (maxT !== logSince) {
          logSince = maxT;
          logBoundary = {};
        }
        for (var j = 0; j < logs.length; j++) {
          if (logs[j] && logs[j].t === logSince) {
            logBoundary[entryKey(logs[j])] = true;
          }
        }
        while (logRows.length > LOG_MAX_ROWS) {
          logRows.shift();
        }
        renderLogs();
      })
      .catch(function () {
        // transient; the next tick retries from the same cursor
      });
  }

  function selectChip(chip, chips) {
    // Re-clicking the active chip would otherwise re-download the server's whole
    // 500-entry ring just to keep the last twelve rows.
    if (chip.classList.contains("vb-chip-on")) {
      return;
    }
    for (var i = 0; i < chips.length; i++) {
      chips[i].classList.remove("vb-chip-on");
    }
    chip.classList.add("vb-chip-on");

    logLevel = chip.getAttribute("data-level") || "";
    // Reset the cursor and the buffer so the new filter applies to history immediately
    // instead of only to rows that arrive from now on. The generation bump makes any
    // in-flight response for the old level a no-op, which is what stops it from
    // re-filling the buffer and advancing the cursor past the history just discarded.
    logGen++;
    logSince = 0;
    logBoundary = {};
    logRows = [];
    renderLogs();
    logPoller.kick();
  }

  function initLogChips() {
    var wrap = el("vbLogChips");
    if (!wrap) {
      return;
    }
    var chips = Array.prototype.slice.call(wrap.querySelectorAll(".vb-chip"));
    if (!chips.length) {
      return;
    }

    var active = null;
    for (var i = 0; i < chips.length; i++) {
      (function (chip) {
        chip.addEventListener("click", function () {
          selectChip(chip, chips);
        });
      })(chips[i]);
      if (chips[i].classList.contains("vb-chip-on")) {
        active = chips[i];
      }
    }

    if (!active) {
      // Default to the "All" chip, which is the one with an empty data-level.
      for (var j = 0; j < chips.length; j++) {
        if (!chips[j].getAttribute("data-level")) {
          active = chips[j];
          break;
        }
      }
      if (!active) {
        active = chips[0];
      }
      active.classList.add("vb-chip-on");
    }
    logLevel = active.getAttribute("data-level") || "";
  }

  // -------------------------------------------------------------------------- drawer

  var drawerOpen = false;

  function setDrawer(open) {
    var drawer = el("vbDrawer");
    var scrim = el("vbScrim");
    var gear = el("vbGear");

    if (drawer) {
      drawer.classList.toggle("vb-open", open);
      drawer.setAttribute("aria-hidden", open ? "false" : "true");
      // aria-hidden on its own leaves the sixteen tiles and every control in the
      // fourteen sections focusable; inert (with visibility:hidden in style.css as
      // the fallback) actually takes the closed drawer out of the tab order.
      if (open) {
        drawer.removeAttribute("inert");
      } else {
        drawer.setAttribute("inert", "");
      }
    }
    if (scrim) {
      scrim.classList.toggle("vb-open", open);
    }
    if (gear) {
      gear.setAttribute("aria-expanded", open ? "true" : "false");
    }
    // The drawer is a fixed full-height overlay; without this the card keeps
    // scrolling behind it.
    if (document.body && document.body.classList) {
      document.body.classList.toggle("vb-locked", open);
    }
    drawerOpen = open;
  }

  function initDrawer() {
    var gear = el("vbGear");
    var closeBtn = el("vbDrawerClose");
    var scrim = el("vbScrim");

    if (gear) {
      gear.addEventListener("click", function (ev) {
        ev.preventDefault();
        setDrawer(!drawerOpen);
      });
    }
    if (closeBtn) {
      closeBtn.addEventListener("click", function (ev) {
        ev.preventDefault();
        setDrawer(false);
      });
    }
    if (scrim) {
      scrim.addEventListener("click", function () {
        setDrawer(false);
      });
    }

    document.addEventListener("keydown", function (ev) {
      if (!drawerOpen) {
        return;
      }
      if (ev.key === "Escape" || ev.key === "Esc" || ev.keyCode === 27) {
        setDrawer(false);
      }
    });

    setDrawer(false);
  }

  // ---------------------------------------------------------------------------- init

  var statusPoller = makePoller(pollStatus, STATUS_POLL_MS);
  var batteryPoller = makePoller(pollBattery, BATTERY_POLL_MS);
  var logPoller = makePoller(pollLogs, LOG_POLL_MS);

  function init() {
    // Only the settings dashboard has this card; anywhere else, do nothing at all.
    if (!el("vbCard")) {
      return;
    }

    renderStatus(null);
    renderBattery(null);
    initDrawer();
    initLogChips();
    renderLogs();
    initCam();

    statusPoller.start();
    batteryPoller.start();
    logPoller.start();

    document.addEventListener("visibilitychange", function () {
      if (document.hidden) {
        // The robot keeps encoding and sending frames for as long as the stream is
        // open, so a backgrounded tab is a real drain on a robot this fork also
        // watches for low battery. Drop the stream and pick it up again on return.
        stopCam();
        return;
      }
      startCam();
      statusPoller.kick();
      batteryPoller.kick();
      logPoller.kick();
    });

    window.addEventListener("beforeunload", stopCam);
    window.addEventListener("pagehide", stopCam);
    // A bfcache restore (browser Back) resurrects the page with the camera torn down
    // by pagehide and no load or error event to notice it.
    window.addEventListener("pageshow", function (ev) {
      if (ev && ev.persisted) {
        startCam();
      }
    });

    window.vectorBrain = {
      serial: vbEsn,
      refresh: function () {
        statusPoller.kick();
        batteryPoller.kick();
        logPoller.kick();
      },
      openDrawer: function () {
        setDrawer(true);
      },
      closeDrawer: function () {
        setDrawer(false);
      }
    };
  }

  function safeInit() {
    try {
      init();
    } catch (e) {
      // This script loads alongside main.js; an exception here must never take the
      // legacy tiles down with it.
      if (window.console && console.error) {
        console.error("vectorbrain: init failed", e);
      }
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", safeInit);
  } else {
    safeInit();
  }
})();
