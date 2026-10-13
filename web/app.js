"use strict";

const $ = (id) => document.getElementById(id);
let adminToken = null; // kept in memory only
let photoBlob = null;
let camStream = null;
let lastRecordIds = new Set();
let firstRecordsLoad = true;

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

async function api(path, opts = {}) {
  const res = await fetch(path, { cache: "no-store", ...opts });
  let body = null;
  try { body = await res.json(); } catch { /* non-JSON */ }
  if (!res.ok) {
    const err = new Error((body && body.error) || `HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return body;
}

function setTile(id, state, main, sub) {
  const t = $(id);
  t.classList.remove("ok", "warn", "err");
  if (state) t.classList.add(state);
  t.querySelector(".tile-main").textContent = main;
  t.querySelector(".tile-sub").textContent = sub || "";
}

// ---- Status polling ----

// Status is polled every second; a poll that takes longer than this counts as
// a lost connection, so a dead or hung pod shows up within 1–2 seconds.
const STATUS_INTERVAL_MS = 1000;
const STATUS_TIMEOUT_MS = 1500;
let statusInFlight = false;

async function pollStatus() {
  if (statusInFlight) return; // don't stack polls while one is still waiting
  statusInFlight = true;
  try {
    const s = await api("api/status", { signal: AbortSignal.timeout(STATUS_TIMEOUT_MS) });
    const c = s.connection;
    const banner = $("banner");
    banner.className = "banner " + (c.bannerColor === "orange" ? "orange" : "blue");
    $("conn-label").textContent = "You're connected to";
    $("conn-name").textContent = c.displayName;
    $("conn-host").textContent = c.host;
    $("conn-pod").textContent = c.pod + (c.node ? " @ " + c.node : "");
    $("version").textContent = `${c.cluster} (${c.role}) · ${s.version}`;

    const k = s.cluster;
    if (k.mode === "local") {
      setTile("tile-cluster", "ok", "Running locally", "Not inside Kubernetes");
    } else if (k.error) {
      setTile("tile-cluster", "warn", "Status unavailable", k.error);
    } else {
      setTile("tile-cluster", k.healthy ? "ok" : "err", k.healthy ? "Healthy" : "Degraded",
        `${k.nodesReady}/${k.nodesTotal} nodes ready · app pods ${k.hpaCurrent} (max ${k.hpaMax})`);
    }

    const n = s.nai;
    if (!n.configured) {
      setTile("tile-nai", "warn", "Not configured", "Open NAI endpoint settings below");
    } else if (n.connected) {
      setTile("tile-nai", "ok", "Connected", `${n.model || "no model selected"} · ${n.latencyMs} ms`);
    } else {
      setTile("tile-nai", "err", "Error", n.error);
    }

    const st = s.storage;
    if (st.reachable) {
      setTile("tile-storage", "ok", `${st.recordCount} records`,
        st.backend === "objects" ? `Reachable · ${st.latencyMs} ms` : "In-memory (local only)");
    } else {
      setTile("tile-storage", "err", "Unreachable", st.error);
    }
  } catch (e) {
    const banner = $("banner");
    banner.className = "banner lost";
    $("conn-label").textContent = "Connection lost";
    $("conn-name").textContent = "Waiting for the app…";
  } finally {
    statusInFlight = false;
  }
}

// ---- Settings ----

async function loadSettings() {
  try {
    const s = await api("api/settings/nai");
    $("set-url").value = s.baseUrl || "";
    $("set-key").placeholder = s.hasKey ? `saved key ${s.keyHint} — leave blank to keep` : "API key";
    const sel = $("set-model");
    if (s.model && ![...sel.options].some((o) => o.value === s.model)) {
      sel.replaceChildren(new Option(s.model, s.model, true, true));
    }
    if (s.source === "none") $("settings").open = true;
  } catch (e) {
    $("set-msg").textContent = e.message;
  }
}

function settingsBody() {
  return JSON.stringify({ baseUrl: $("set-url").value.trim(), apiKey: $("set-key").value, model: $("set-model").value });
}

function setMsg(text, cls) {
  const m = $("set-msg");
  m.className = "hint " + (cls || "");
  m.textContent = text;
}

$("btn-test").addEventListener("click", async () => {
  const btn = $("btn-test");
  btn.disabled = true;
  setMsg("Testing…");
  try {
    const r = await api("api/settings/nai/test", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: settingsBody(),
    });
    if (!r.ok) { setMsg(r.error, "err"); return; }
    const sel = $("set-model");
    const current = sel.value;
    sel.replaceChildren(...r.models.map((m) => new Option(m, m, false, m === current)));
    setMsg(`${r.models.length} models found · ${r.latencyMs} ms`, "ok");
  } catch (e) {
    setMsg(e.message, "err");
  } finally {
    btn.disabled = false;
  }
});

$("set-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const save = async () => {
    const headers = { "Content-Type": "application/json" };
    if (adminToken) headers["X-Admin-Token"] = adminToken;
    return api("api/settings/nai", { method: "PUT", headers, body: settingsBody() });
  };
  try {
    let r;
    try {
      r = await save();
    } catch (e) {
      if (e.status !== 401) throw e;
      adminToken = prompt("Admin token required to change NAI settings:");
      if (!adminToken) return;
      r = await save();
    }
    $("set-key").value = "";
    setMsg("Saved", "ok");
    loadSettings();
    pollStatus();
  } catch (e) {
    setMsg(e.message, "err");
  }
});

// ---- Photo capture ----

function setPhoto(blob) {
  photoBlob = blob;
  if (blob) {
    $("cam-error").hidden = true;
    $("preview-img").src = URL.createObjectURL(blob);
    $("preview").hidden = false;
  } else {
    $("preview").hidden = true;
    $("preview-img").removeAttribute("src");
  }
}

let camFacing = "environment";
let capturedBlob = null;

function stopCamera() {
  if (camStream) camStream.getTracks().forEach((t) => t.stop());
  camStream = null;
  $("camera").hidden = true;
  document.body.style.overflow = "";
}

function showCamError(msg) {
  $("cam-error-msg").textContent = msg;
  $("cam-error").hidden = false;
}

function camErrorMessage(err) {
  if (!window.isSecureContext) {
    return `Chrome only allows the camera on HTTPS or localhost. This page is open on ${location.origin}. ` +
      "Open it over HTTPS (or via localhost) to take photos.";
  }
  switch (err && err.name) {
    case "NotAllowedError":
    case "SecurityError":
      return "Camera permission is blocked. Click the camera icon in the address bar, allow access, then try again.";
    case "NotFoundError":
    case "OverconstrainedError":
      return "No camera was found on this device.";
    case "NotReadableError":
      return "The camera is in use by another app (e.g. Zoom or Teams). Close it and try again.";
  }
  return "Could not start the camera" + (err && err.message ? ": " + err.message : ".");
}

// Live view state: video + shutter. Review state: still image + Retake / Use photo.
function setCamMode(review) {
  $("cam-video").hidden = review;
  $("cam-still").hidden = !review;
  $("btn-capture").style.visibility = review ? "hidden" : "visible";
  $("btn-retake").hidden = !review;
  $("btn-use").hidden = !review;
  $("btn-cam-switch").hidden = review || !$("btn-cam-switch").dataset.multi;
}

async function startStream() {
  if (camStream) camStream.getTracks().forEach((t) => t.stop());
  camStream = await navigator.mediaDevices.getUserMedia({
    video: { facingMode: { ideal: camFacing }, width: { ideal: 1920 }, height: { ideal: 1080 } },
    audio: false,
  });
  $("cam-video").srcObject = camStream;
  const cams = (await navigator.mediaDevices.enumerateDevices()).filter((d) => d.kind === "videoinput");
  $("btn-cam-switch").dataset.multi = cams.length > 1 ? "1" : "";
}

async function openCamera() {
  $("cam-error").hidden = true;
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    showCamError(camErrorMessage(null));
    return;
  }
  try {
    await startStream();
  } catch (err) {
    showCamError(camErrorMessage(err));
    return;
  }
  capturedBlob = null;
  setCamMode(false);
  $("camera").hidden = false;
  document.body.style.overflow = "hidden";
}

// Phones/tablets: the capture input opens the device's camera app directly.
// Laptops/desktops: open the camera full-screen in the page.
const isTouchDevice = window.matchMedia("(pointer: coarse)").matches;

$("btn-photo").addEventListener("click", () => {
  if (isTouchDevice) $("file-input").click();
  else openCamera();
});
$("btn-file").addEventListener("click", () => $("file-input").click());

$("btn-capture").addEventListener("click", () => {
  const v = $("cam-video");
  if (!v.videoWidth) return;
  const canvas = document.createElement("canvas");
  canvas.width = v.videoWidth;
  canvas.height = v.videoHeight;
  canvas.getContext("2d").drawImage(v, 0, 0);
  const cam = $("camera");
  cam.classList.remove("flash");
  void cam.offsetWidth; // restart the flash animation
  cam.classList.add("flash");
  canvas.toBlob((b) => {
    capturedBlob = b;
    $("cam-still").src = URL.createObjectURL(b);
    setCamMode(true);
  }, "image/jpeg", 0.92);
});
$("btn-retake").addEventListener("click", () => { capturedBlob = null; setCamMode(false); });
$("btn-use").addEventListener("click", () => { setPhoto(capturedBlob); stopCamera(); });
$("btn-cam-switch").addEventListener("click", async () => {
  camFacing = camFacing === "environment" ? "user" : "environment";
  try { await startStream(); } catch (err) { stopCamera(); showCamError(camErrorMessage(err)); }
});
$("btn-cam-cancel").addEventListener("click", stopCamera);
document.addEventListener("keydown", (ev) => {
  if ($("camera").hidden) return;
  if (ev.key === "Escape") stopCamera();
  if ((ev.key === " " || ev.key === "Enter") && !$("cam-video").hidden) { ev.preventDefault(); $("btn-capture").click(); }
});
$("btn-remove").addEventListener("click", () => setPhoto(null));
$("file-input").addEventListener("change", (ev) => {
  const f = ev.target.files[0];
  if (f) setPhoto(f);
  ev.target.value = "";
});

document.querySelectorAll("[data-sample]").forEach((b) =>
  b.addEventListener("click", () => { $("in-text").value = b.dataset.sample; }));

// ---- Intake ----

function chip(text, cls) { return el("span", "chip " + (cls || ""), text); }

function renderResult(r) {
  const box = $("result");
  box.hidden = false;
  box.className = "result";
  box.replaceChildren();

  if (r.status === "blocked") {
    box.classList.add("blocked");
    box.append(el("div", "", "⛔ Blocked by endpoint policy"), el("div", "hint", r.reason || ""));
    return;
  }

  box.append(el("h3", "", `Personal data found (${r.entities.length})`));
  const chips = el("div", "chips");
  if (r.entities.length === 0) chips.append(chip("None", ""));
  r.entities.forEach((e) => chips.append(chip(`${e.type.replace("_", " ")}: ${e.masked}`, "pii")));
  box.append(chips);

  box.append(el("h3", "", "Masked text (this is all that is stored)"), el("div", "masked", r.maskedText || "—"));

  box.append(el("h3", "", "Classification"));
  const meta = el("div", "chips");
  meta.append(chip(r.classification, r.classification));
  box.append(meta);

  box.append(el("h3", "", "Summary"), el("div", "", r.summary));
  box.append(el("div", "hint", `Model ${r.model} · ${r.naiLatencyMs} ms · filed by ${r.processedBy.cluster} as ${r.id}`));
}

$("btn-scan").addEventListener("click", async () => {
  const text = $("in-text").value.trim();
  if (!text && !photoBlob) {
    renderError("Enter some text or attach a photo.");
    return;
  }
  const fd = new FormData();
  fd.append("text", text);
  if (photoBlob) fd.append("photo", photoBlob, "photo.jpg");

  const btn = $("btn-scan");
  btn.disabled = true;
  $("spinner").hidden = false;
  $("result").hidden = true;
  try {
    const r = await api("api/intake", { method: "POST", body: fd });
    renderResult(r);
    if (r.status !== "blocked") {
      $("in-text").value = "";
      setPhoto(null);
    }
    loadRecords();
    pollStatus();
  } catch (e) {
    renderError(e.message);
  } finally {
    btn.disabled = false;
    $("spinner").hidden = true;
  }
});

function renderError(msg) {
  const box = $("result");
  box.hidden = false;
  box.className = "result error";
  box.replaceChildren(el("div", "", "⚠ " + msg));
}

// ---- Records ----

async function loadRecords() {
  let recs;
  try {
    recs = await api("api/records?limit=50");
  } catch (e) {
    return; // storage tile shows the error
  }
  $("rec-count").textContent = recs.length;
  const list = $("rec-list");
  list.replaceChildren();
  if (recs.length === 0) list.append(el("li", "empty", "No records yet."));
  const seen = new Set();
  for (const r of recs) {
    seen.add(r.id);
    const li = el("li", "rec");
    if (!firstRecordsLoad && !lastRecordIds.has(r.id)) li.classList.add("new");
    if (r.hasPhoto) {
      const img = el("img");
      img.src = `api/records/${encodeURIComponent(r.id)}/thumb`;
      img.alt = "Blurred photo thumbnail";
      img.loading = "lazy";
      li.append(img);
    }
    const body = el("div", "rec-body");
    const line = r.entities && r.entities.length ? r.entities.map((e) => e.masked).join(" · ") : r.summary;
    body.append(el("div", "rec-line", line));
    body.append(el("div", "rec-sum", r.summary));
    const meta = el("div", "rec-meta");
    meta.append(chip(r.classification, r.classification));
    meta.append(chip(r.processedBy.cluster, "role-" + r.processedBy.role));
    meta.append(el("span", "", new Date(r.createdAt).toLocaleString() + " · " + r.inputType));
    body.append(meta);
    li.append(body);
    list.append(li);
  }
  lastRecordIds = seen;
  firstRecordsLoad = false;
}

pollStatus();
loadSettings();
loadRecords();
setInterval(pollStatus, STATUS_INTERVAL_MS);
setInterval(loadRecords, 10000);
