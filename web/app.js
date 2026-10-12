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

async function pollStatus() {
  try {
    const s = await api("/api/status");
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
        st.backend === "objects" ? `bucket ${st.bucket} · ${st.latencyMs} ms` : "in-memory (local only)");
    } else {
      setTile("tile-storage", "err", "Unreachable", st.error);
    }
  } catch (e) {
    const banner = $("banner");
    banner.className = "banner lost";
    $("conn-label").textContent = "Connection lost";
    $("conn-name").textContent = "Waiting for the app…";
  }
}

// ---- Settings ----

async function loadSettings() {
  try {
    const s = await api("/api/settings/nai");
    $("set-url").value = s.baseUrl || "";
    $("set-key").placeholder = s.hasKey ? `saved key ${s.keyHint} — leave blank to keep` : "API key";
    $("set-source").textContent = s.source === "none" ? "not configured" : "from " + s.source;
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
    const r = await api("/api/settings/nai/test", {
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
    return api("/api/settings/nai", { method: "PUT", headers, body: settingsBody() });
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
    setMsg(`Saved to ${r.source} · shared by all clusters`, "ok");
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
    $("preview-img").src = URL.createObjectURL(blob);
    $("preview").hidden = false;
  } else {
    $("preview").hidden = true;
    $("preview-img").removeAttribute("src");
  }
}

function stopCamera() {
  if (camStream) camStream.getTracks().forEach((t) => t.stop());
  camStream = null;
  $("camera").hidden = true;
}

$("btn-photo").addEventListener("click", async () => {
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    $("file-input").click();
    return;
  }
  try {
    camStream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: "environment" } });
    $("cam-video").srcObject = camStream;
    $("camera").hidden = false;
  } catch {
    $("file-input").click();
  }
});

$("btn-capture").addEventListener("click", () => {
  const v = $("cam-video");
  const canvas = document.createElement("canvas");
  canvas.width = v.videoWidth;
  canvas.height = v.videoHeight;
  canvas.getContext("2d").drawImage(v, 0, 0);
  canvas.toBlob((b) => { setPhoto(b); stopCamera(); }, "image/jpeg", 0.9);
});
$("btn-cam-cancel").addEventListener("click", stopCamera);
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
    const r = await api("/api/intake", { method: "POST", body: fd });
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
    recs = await api("/api/records?limit=50");
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
      img.src = `/api/records/${encodeURIComponent(r.id)}/thumb`;
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
setInterval(pollStatus, 3000);
setInterval(loadRecords, 10000);
