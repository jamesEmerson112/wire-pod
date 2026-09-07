// vectorbrain.js - the "Vector Brain" dashboard.
//
// Renders into #botStats on index.html, replacing the battery-card strip that
// battery.js drew there. It only shows values wire-pod can actually serve today:
// connection status, battery, robot lifetime stats, the live log, and an opt-in
// camera feed. Anything the design mockup showed that has no backing data
// (SLAM map, throughput, RAG memory) is deliberately absent rather than faked.
//
// battery.js is still loaded on this page and is still the only renderer for
// #botStats on sdkapp/settings.html, so nothing here modifies it. In particular
// getBatteryPercentage() is reused rather than reimplemented: the Go battery
// watchdog (pkg/wirepod/sdkapp/batterywatchdog.go) mirrors that same curve on
// purpose, and a second copy here would be a third thing to keep in sync.

// /api/get_bot_status is cheap - it reads in-memory heartbeat state and never
// opens an SDK connection, so it can poll fast. Everything under /api-sdk/ dials
// the robot over gRPC and holds the connection open for 300s, so those poll slowly.
const VB_STATUS_MS = 2000;
const VB_BATTERY_MS = 5000;
const VB_STATS_MS = 60000;
const VB_LOG_MS = 1000;
const VB_LOG_ROWS = 12;

let vbBots = [];
let vbLog = [];
let vbLogSince = 0;
let vbCamEsn = null;
let vbTimers = [];

function vbEl(tag, cls, text) {
  const el = document.createElement(tag);
  if (cls) {
    el.className = cls;
  }
  if (text !== undefined) {
    el.textContent = text;
  }
  return el;
}

function vbCard(esn) {
  const mount = document.getElementById("botStats");
  return mount ? mount.querySelector('.vb-bot[data-esn="' + esn + '"]') : null;
}

// /api-sdk/ handlers answer 200 with a plain-text body starting "error: " when
// the robot is unreachable, so a failed call still looks like a successful fetch.
function vbSdkFailed(text) {
  return typeof text !== "string" || text.startsWith("error: ");
}

// ===== bot cards =====

function vbBuildCard(bot) {
  const card = vbEl("div", "vb-bot");
  card.dataset.esn = bot.esn;

  const head = vbEl("div", "vb-bot-head");
  head.appendChild(vbEl("span", "status-dot"));
  head.appendChild(vbEl("span", "vb-esn", bot.esn));
  head.appendChild(vbEl("span", "vb-sub vb-conn"));
  card.appendChild(head);

  const readouts = vbEl("div", "vb-readouts");
  const batt = vbEl("div", "vb-readout");
  batt.appendChild(vbEl("div", "vb-label", "BATTERY"));
  batt.appendChild(vbEl("div", "vb-value vb-batt", "--"));
  batt.appendChild(vbEl("div", "vb-sub vb-volts", ""));
  readouts.appendChild(batt);

  const charger = vbEl("div", "vb-readout");
  charger.appendChild(vbEl("div", "vb-label", "CHARGER"));
  charger.appendChild(vbEl("div", "vb-value vb-charge", "--"));
  charger.appendChild(vbEl("div", "vb-sub vb-chargesub", ""));
  readouts.appendChild(charger);
  card.appendChild(readouts);

  const bar = vbEl("div", "vb-bar");
  bar.appendChild(vbEl("div", "vb-bar-fill"));
  card.appendChild(bar);

  card.appendChild(vbEl("div", "vb-label vb-stats-label", "LIFETIME STATS"));
  card.appendChild(vbEl("div", "vb-stats"));

  const cam = vbEl("div", "vb-cam");
  const btn = vbEl("button", "vb-cam-btn", "Start camera");
  // Streaming calls EnableImageStreaming on the robot, which keeps its camera
  // powered for as long as the feed is open. That is opt-in per robot, never
  // automatic, and only one robot streams at a time.
  btn.onclick = function () {
    vbToggleCam(bot.esn);
  };
  cam.appendChild(btn);
  cam.appendChild(vbEl("div", "vb-cam-frame"));
  card.appendChild(cam);

  return card;
}

function vbRenderBots(bots) {
  const mount = document.getElementById("botStats");
  if (!mount) {
    return;
  }
  const grid = mount.querySelector(".vb-grid");
  const empty = mount.querySelector(".vb-empty");
  if (!grid || !empty) {
    return;
  }

  if (!bots || bots.length === 0) {
    grid.innerHTML = "";
    empty.textContent = "no robots known yet - pair a robot from Bot Setup";
    empty.style.display = "block";
    return;
  }
  empty.style.display = "none";

  const seen = {};
  let created = false;
  bots.forEach(function (bot) {
    seen[bot.esn] = true;
    let card = vbCard(bot.esn);
    if (!card) {
      card = vbBuildCard(bot);
      grid.appendChild(card);
      created = true;
    }
    card.querySelector(".status-dot").className = "status-dot status-" + bot.status;
    const conn = card.querySelector(".vb-conn");
    if (bot.status === "online") {
      conn.textContent = bot.ip + " · " + bot.timesince + "s ago";
    } else {
      conn.textContent = bot.ip + " · " + bot.status;
    }
    card.classList.toggle("vb-bot-down", bot.status !== "online");
  });

  // Drop cards for robots that vanished from the payload rather than leaving
  // stale ones behind. Cards are matched by ESN, never by position, so a robot
  // appearing or disappearing cannot shift another robot's readings onto it.
  Array.prototype.slice.call(grid.children).forEach(function (card) {
    if (!seen[card.dataset.esn]) {
      if (vbCamEsn === card.dataset.esn) {
        vbStopCam();
      }
      card.remove();
    }
  });

  // Cards can only be built once /api/get_bot_status has answered, so the slow
  // endpoints are primed here rather than at init, where vbBots is still empty.
  // Without this a new robot shows blank stats until the next 60s tick.
  if (created) {
    vbPollBattery();
    vbPollStats();
  }
}

function vbPollStatus() {
  fetch("/api/get_bot_status")
    .then(function (r) {
      return r.json();
    })
    .then(function (bots) {
      // The endpoint used to answer null rather than [] when no robots were
      // known; guard anyway so an older chipper build cannot break the page.
      vbBots = Array.isArray(bots) ? bots : [];
      vbRenderBots(vbBots);
    })
    .catch(function () {
      // leave the last good render in place
    });
}

// ===== battery =====

function vbOnlineBots() {
  return vbBots.filter(function (b) {
    return b.status === "online";
  });
}

async function vbPollBattery() {
  const bots = vbOnlineBots();
  for (const bot of bots) {
    const card = vbCard(bot.esn);
    if (!card) {
      continue;
    }
    let data;
    try {
      data = await getBatteryStatus(bot.esn);
    } catch (e) {
      data = undefined;
    }
    if (!data) {
      card.querySelector(".vb-batt").textContent = "--";
      card.querySelector(".vb-volts").textContent = "unreachable";
      continue;
    }
    const volts = data.battery_volts;
    const pct = getBatteryPercentage(volts);
    card.querySelector(".vb-batt").textContent = pct + "%";
    card.querySelector(".vb-volts").textContent = volts
      ? volts.toFixed(2) + " V"
      : "voltage not reported";

    const fill = card.querySelector(".vb-bar-fill");
    fill.style.width = pct + "%";
    fill.className = "vb-bar-fill" + (pct < 20 ? " vb-bar-low" : pct < 50 ? " vb-bar-mid" : "");

    let state = "off charger";
    if (data.is_charging) {
      state = "charging";
    } else if (data.is_on_charger_platform) {
      state = "docked";
    }
    card.querySelector(".vb-charge").textContent = state;
    card.querySelector(".vb-chargesub").textContent =
      data.is_charging && data.suggested_charger_sec
        ? Math.round(data.suggested_charger_sec / 60) + " min to full"
        : "";
  }
}

// ===== lifetime stats =====

function vbPrettyKey(k) {
  return k.replace(/[._]/g, " ");
}

async function vbPollStats() {
  const bots = vbOnlineBots();
  for (const bot of bots) {
    const card = vbCard(bot.esn);
    if (!card) {
      continue;
    }
    let text;
    try {
      const resp = await fetch("/api-sdk/get_robot_stats?serial=" + bot.esn, {
        method: "POST",
        signal: AbortSignal.timeout(15000)
      });
      text = await resp.text();
    } catch (e) {
      text = undefined;
    }
    const box = card.querySelector(".vb-stats");
    if (vbSdkFailed(text)) {
      box.textContent = "unavailable";
      continue;
    }
    let doc;
    try {
      doc = JSON.parse(text);
    } catch (e) {
      box.textContent = "unavailable";
      continue;
    }
    // The jdoc's field names are whatever the robot's firmware ships, so render
    // whatever scalars come back instead of hardcoding a schema.
    box.innerHTML = "";
    Object.keys(doc)
      .sort()
      .forEach(function (k) {
        const v = doc[k];
        if (v === null || typeof v === "object") {
          return;
        }
        const row = vbEl("div", "vb-stat");
        row.appendChild(vbEl("span", "vb-stat-k", vbPrettyKey(k)));
        row.appendChild(
          vbEl("span", "vb-stat-v", typeof v === "number" ? v.toLocaleString() : String(v))
        );
        box.appendChild(row);
      });
    if (!box.children.length) {
      box.textContent = "no stats reported";
    }
  }
}

// ===== camera =====

function vbStopCam() {
  const mount = document.getElementById("botStats");
  if (!mount) {
    return;
  }
  mount.querySelectorAll(".vb-cam-frame").forEach(function (f) {
    f.innerHTML = "";
  });
  mount.querySelectorAll(".vb-cam-btn").forEach(function (b) {
    b.textContent = "Start camera";
    b.classList.remove("vb-cam-on");
  });
  vbCamEsn = null;
}

function vbToggleCam(esn) {
  const wasStreaming = vbCamEsn === esn;
  // The Go handler tracks one CamStreaming flag per robot and the page only ever
  // needs one feed, so always tear the current one down first.
  vbStopCam();
  if (wasStreaming) {
    return;
  }
  const card = vbCard(esn);
  if (!card) {
    return;
  }
  const img = document.createElement("img");
  img.className = "vb-cam-img";
  img.alt = "camera feed for " + esn;
  img.src = "/cam-stream?serial=" + esn;
  card.querySelector(".vb-cam-frame").appendChild(img);
  const btn = card.querySelector(".vb-cam-btn");
  btn.textContent = "Stop camera";
  btn.classList.add("vb-cam-on");
  vbCamEsn = esn;
}

// ===== live log =====

function vbPollLog() {
  fetch("/api/get_logs_json?since=" + vbLogSince)
    .then(function (r) {
      return r.json();
    })
    .then(function (entries) {
      if (!Array.isArray(entries) || entries.length === 0) {
        return;
      }
      entries.forEach(function (e) {
        if (e.t > vbLogSince) {
          vbLogSince = e.t;
        }
        vbLog.push(e);
      });
      while (vbLog.length > VB_LOG_ROWS) {
        vbLog.shift();
      }
      vbRenderLog();
    })
    .catch(function () {
      // leave the last good render in place
    });
}

function vbRenderLog() {
  const body = document.getElementById("vbLogBody");
  if (!body) {
    return;
  }
  body.innerHTML = "";
  vbLog.forEach(function (e) {
    const row = vbEl("div", "vb-log-row");
    row.appendChild(vbEl("span", "vb-log-t", logTimeString(e.t)));
    row.appendChild(vbEl("span", "vb-log-l level-" + e.level, e.level));
    row.appendChild(vbEl("span", "vb-log-c comp-" + e.comp, e.comp || ""));
    row.appendChild(vbEl("span", "vb-log-b", e.bot || ""));
    row.appendChild(vbEl("span", "vb-log-m", e.msg));
    body.appendChild(row);
  });
}

// ===== bootstrap =====

function initVectorBrain() {
  const mount = document.getElementById("botStats");
  if (!mount || mount.classList.contains("vb-mount")) {
    return;
  }
  // #botStats is a fixed 50px flex row sized for battery.js's cards; vb-mount
  // releases that so the dashboard can size itself. The class is only ever added
  // here, so sdkapp/settings.html keeps the original strip layout untouched.
  mount.classList.add("vb-mount");
  mount.innerHTML = "";
  mount.appendChild(vbEl("div", "vb-empty", "connecting..."));
  mount.appendChild(vbEl("div", "vb-grid"));

  const log = vbEl("div", "vb-log");
  log.appendChild(vbEl("div", "vb-label", "LIVE LOG"));
  const body = vbEl("div", "vb-log-body");
  body.id = "vbLogBody";
  log.appendChild(body);
  mount.appendChild(log);

  vbPollStatus();
  vbPollLog();
  vbTimers.push(setInterval(vbPollStatus, VB_STATUS_MS));
  vbTimers.push(setInterval(vbPollLog, VB_LOG_MS));
  vbTimers.push(setInterval(vbPollBattery, VB_BATTERY_MS));
  vbTimers.push(setInterval(vbPollStats, VB_STATS_MS));
}
