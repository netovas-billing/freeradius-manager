// =============================================================================
// RM-API Console — vanilla JS frontend.
//   1. Helpers / 2. Token + fetch / 3. Dialogs + toast / 4. Health pill
//   5. Server info / 6. Instances table / 7. Create+Detail+Delete / 8. Boot
//
// TODO(audit-log): the server's audit log is JSON-lines on disk and not yet
// exposed via the API. When an /v1/audit/tail endpoint lands, render here.
// =============================================================================

(() => {
  "use strict";

  // ---- 1. Helpers -----------------------------------------------------------
  const TOKEN_KEY = "rm_api_token";
  const THEME_KEY = "rm_api_theme";
  const HEALTH_MS = 10_000;
  const INSTANCES_MS = 5_000;
  const INFO_MS = 15_000;

  const $ = (s) => document.querySelector(s);
  const $$ = (s) => Array.from(document.querySelectorAll(s));
  const el = (tag, attrs = {}, ...children) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (k === "class") n.className = v;
      else if (k === "html") n.innerHTML = v;
      else if (k.startsWith("on") && typeof v === "function") n.addEventListener(k.slice(2), v);
      else if (v === true) n.setAttribute(k, "");
      else if (v == null || v === false) continue;
      else n.setAttribute(k, v);
    }
    for (const c of children) {
      if (c == null) continue;
      n.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    }
    return n;
  };
  const fmtTime = (iso) => {
    if (!iso) return "—";
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { hour12: false });
  };
  const fmtUptime = (sec) => {
    if (sec == null || sec < 0) return "—";
    const d = Math.floor(sec / 86400),
      h = Math.floor((sec % 86400) / 3600),
      m = Math.floor((sec % 3600) / 60);
    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  };
  const lucideRefresh = () => window.lucide && window.lucide.createIcons();

  // ---- 2. Token + fetch -----------------------------------------------------
  const tokenStore = {
    get: () => localStorage.getItem(TOKEN_KEY) || "",
    set: (t) => localStorage.setItem(TOKEN_KEY, t),
    clear: () => localStorage.removeItem(TOKEN_KEY),
  };

  class ApiError extends Error {
    constructor(message, status, payload) {
      super(message);
      this.status = status;
      this.payload = payload;
    }
  }

  async function apiFetch(path, opts = {}) {
    const headers = new Headers(opts.headers || {});
    const tok = tokenStore.get();
    if (tok) headers.set("Authorization", `Bearer ${tok}`);
    if (opts.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
    let res;
    try {
      res = await fetch(path, { ...opts, headers });
    } catch (e) {
      throw new ApiError(`network error: ${e.message}`, 0, null);
    }
    let payload = null;
    const ct = res.headers.get("Content-Type") || "";
    if (ct.includes("application/json")) {
      try {
        payload = await res.json();
      } catch (_) {}
    } else {
      try {
        payload = await res.text();
      } catch (_) {}
    }
    if (!res.ok) {
      if (res.status === 401) {
        tokenStore.clear();
        showTokenModal();
      }
      const msg = (payload && payload.message) || (payload && payload.error) || `${res.status} ${res.statusText}`;
      throw new ApiError(msg, res.status, payload);
    }
    return payload;
  }

  // ---- 3. Dialogs + toasts --------------------------------------------------
  const toasts = $("#toasts");
  function toast(level, title, body, ttl = 4500) {
    const node = el(
      "div",
      { class: `toast ${level}` },
      el(
        "div",
        { style: "flex:1" },
        el("div", { class: "ttitle" }, title),
        body ? el("div", { class: "tbody" }, body) : null,
      ),
      el(
        "button",
        {
          class: "icon-btn",
          style: "width:18px;height:18px;border:none;background:transparent",
          "aria-label": "dismiss",
          onclick: () => node.remove(),
        },
        el("span", { html: "×" }),
      ),
    );
    toasts.appendChild(node);
    if (ttl > 0) setTimeout(() => node.remove(), ttl);
  }

  const dialogStack = [];
  function openDialog(node, onClose) {
    node.classList.remove("hidden");
    dialogStack.push({ node, onClose });
    queueMicrotask(() => {
      const f = node.querySelector("input,button,textarea,select");
      if (f) f.focus();
    });
  }
  function closeDialog(node) {
    node.classList.add("hidden");
    const i = dialogStack.findIndex((d) => d.node === node);
    if (i >= 0) {
      const [d] = dialogStack.splice(i, 1);
      d.onClose && d.onClose();
    }
  }
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && dialogStack.length) {
      const top = dialogStack[dialogStack.length - 1];
      if (top.node.id === "token-modal" && !tokenStore.get()) return;
      closeDialog(top.node);
    }
  });
  $$(".modal-backdrop, .drawer-backdrop").forEach((bd) => {
    bd.addEventListener("mousedown", (e) => {
      if (e.target === bd) {
        if (bd.id === "token-modal" && !tokenStore.get()) return;
        closeDialog(bd);
      }
    });
  });

  // Token modal
  const tokenModal = $("#token-modal");
  const tokenInput = $("#token-input");
  $("#token-show").addEventListener("change", (e) => {
    tokenInput.type = e.target.checked ? "text" : "password";
  });
  $("#token-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const v = tokenInput.value.trim();
    if (!v) return;
    tokenStore.set(v);
    tokenInput.value = "";
    closeDialog(tokenModal);
    toast("ok", "token saved", "running first sync…");
    refreshAll(true);
  });
  function showTokenModal() {
    if (!tokenModal.classList.contains("hidden")) return;
    openDialog(tokenModal, null);
  }
  $("#forget-token").addEventListener("click", () => {
    tokenStore.clear();
    toast("warn", "token forgotten", "paste a fresh one to reconnect");
    showTokenModal();
  });

  // ---- 4. Health pill -------------------------------------------------------
  const healthPill = $("#health-pill");
  const healthDetail = $("#health-detail");
  function setHealth(state, label, detail) {
    healthPill.className = `status-pill status-pill-${state}`;
    healthPill.querySelector(".label").textContent = label;
    healthDetail.classList.toggle("hidden", !detail);
    healthDetail.textContent = detail || "";
  }
  async function pollHealth() {
    try {
      const res = await fetch("../v1/server/health");
      if (res.ok) {
        const j = await res.json();
        setHealth("ok", j.status || "healthy", (j.issues || []).join("; "));
      } else if (res.status === 503) {
        let j = {};
        try { j = await res.json(); } catch (_) {}
        setHealth("bad", j.status || "unhealthy", (j.issues || []).join("; ") || "service degraded");
      } else {
        setHealth("warn", "http " + res.status, "");
      }
    } catch (_) {
      setHealth("bad", "unreachable", "");
    }
  }

  // ---- 5. Server info -------------------------------------------------------
  async function pollServerInfo() {
    if (!tokenStore.get()) return;
    try {
      const info = await apiFetch("../v1/server/info");
      $("#hostname").textContent = info.hostname || "—";
      $("#stat-instances").textContent = info.instances_count ?? "—";
      $("#stat-instances-sub").textContent = `of ${info.capacity_max ?? "—"}`;
      $("#stat-capacity").textContent = info.capacity_max ?? "—";
      $("#stat-frr").textContent = info.freeradius_version || "—";
      $("#stat-uptime").textContent = fmtUptime(info.uptime_seconds);
      $("#info-vpnip").textContent = info.vpn_ip || "—";
      $("#info-mariadb").textContent = info.mariadb_version || "—";
      $("#info-rmapi").textContent = info.rm_api_version || "—";
      const tag = $("#version-tag");
      tag.textContent = `rm-api ${info.rm_api_version || ""}`;
      tag.classList.remove("hidden");
    } catch (_) {
      // Silent: errors will surface on the relevant action.
    }
  }

  // ---- 6. Instances table ---------------------------------------------------
  const tbody = $("#instances-tbody");
  const filterInput = $("#filter-input");
  let instances = [];
  let filterText = "";
  filterInput.addEventListener("input", (e) => {
    filterText = e.target.value.trim().toLowerCase();
    renderInstances();
  });

  function statusClass(s) {
    if (s === "running") return { row: "is-running", pill: "ok" };
    if (s === "stopped") return { row: "is-stopped", pill: "warn" };
    if (s === "error") return { row: "is-error", pill: "bad" };
    return { row: "is-unknown", pill: "unknown" };
  }

  function statusPill(s) {
    const sc = statusClass(s);
    return el(
      "span",
      { class: `status-pill status-pill-${sc.pill}` },
      el("span", { class: "dot" }),
      el("span", { class: "label" }, s || "unknown"),
    );
  }

  function renderInstances() {
    tbody.replaceChildren();
    const filtered = instances.filter((i) =>
      filterText ? (i.name || "").toLowerCase().includes(filterText) : true,
    );
    $("#instances-count").textContent = `${filtered.length}/${instances.length}`;
    if (filtered.length === 0) {
      const msg = instances.length === 0 ? "no instances yet" : "no matches";
      tbody.appendChild(
        el("tr", { class: "empty-row" },
          el("td", { colspan: "8", class: "py-10 text-center text-ink-400" }, msg)),
      );
      return;
    }
    for (const inst of filtered) {
      const sc = statusClass(inst.status);
      const trashIcon = el("i", { "data-lucide": "trash-2", class: "h-3.5 w-3.5" });
      const tr = el(
        "tr",
        { class: sc.row, onclick: () => openDetail(inst.name) },
        el("td", { class: "row-marker" }),
        el("td", { class: "name-cell" }, inst.name || "—"),
        el("td", {}, statusPill(inst.status)),
        el("td", { class: "num text-right" }, String(inst.ports?.auth ?? "—")),
        el("td", { class: "num text-right" }, String(inst.ports?.acct ?? "—")),
        el("td", { class: "num text-right" }, String(inst.ports?.api ?? "—")),
        el("td", { class: "ts" }, fmtTime(inst.created_at)),
        el("td", { class: "text-right pr-5" },
          el("button", {
            class: "icon-btn",
            title: "Delete",
            "aria-label": "delete",
            onclick: (e) => { e.stopPropagation(); askDelete(inst.name); },
          }, trashIcon),
        ),
      );
      tbody.appendChild(tr);
    }
    lucideRefresh();
  }

  let inflight = false;
  async function pollInstances() {
    if (!tokenStore.get() || inflight) return;
    inflight = true;
    $("#instances-refreshing").classList.remove("hidden");
    try {
      const list = await apiFetch("../v1/instances/");
      instances = Array.isArray(list) ? list : [];
      renderInstances();
      $("#last-sync").textContent = `synced ${new Date().toLocaleTimeString(undefined, { hour12: false })}`;
    } catch (e) {
      if (e.status !== 401) toast("bad", "list instances failed", e.message, 6000);
    } finally {
      inflight = false;
      $("#instances-refreshing").classList.add("hidden");
    }
  }
  $("#refresh-btn").addEventListener("click", () => pollInstances());

  // ---- 7a. Create -----------------------------------------------------------
  $("#create-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const name = $("#create-name").value.trim();
    if (!name) return;
    const btn = $("#create-submit");
    btn.disabled = true;
    const original = btn.innerHTML;
    btn.innerHTML = '<i data-lucide="loader" class="h-3.5 w-3.5 animate-spin"></i><span>provisioning…</span>';
    lucideRefresh();
    toast("info", `creating ${name}`, "may take ~15s for first bootstrap", 8000);
    try {
      const r = await apiFetch("../v1/instances/", {
        method: "POST",
        body: JSON.stringify({ name }),
      });
      toast("ok", `created ${r.name}`, `auth :${r.ports?.auth} · api :${r.ports?.api}`);
      $("#create-name").value = "";
      pollInstances();
      pollServerInfo();
    } catch (err) {
      toast("bad", "create failed", err.message, 7000);
    } finally {
      btn.disabled = false;
      btn.innerHTML = original;
      lucideRefresh();
    }
  });

  // ---- 7b. Detail drawer ----------------------------------------------------
  const drawer = $("#detail-drawer");
  const drawerBody = $("#detail-body");
  const drawerTitle = $("#detail-title");
  const drawerStatusPill = $("#detail-status-pill");
  $("#detail-close").addEventListener("click", () => closeDialog(drawer));

  const copyBtn = (text) => el("button", {
    class: "copy-btn",
    title: "copy",
    onclick: async (e) => {
      e.stopPropagation();
      try { await navigator.clipboard.writeText(text); toast("info", "copied", "", 1500); }
      catch (_) { toast("warn", "copy unavailable", ""); }
    },
  }, "copy");

  const kvRow = (dt, dd) => {
    const dtN = el("dt", {}, dt);
    const ddN = el("dd", {});
    if (typeof dd === "string") ddN.appendChild(document.createTextNode(dd));
    else if (dd) ddN.appendChild(dd);
    return [dtN, ddN];
  };

  function buildDetail(inst) {
    const sc = statusClass(inst.status);
    drawerTitle.textContent = inst.name || "instance";
    drawerStatusPill.className = `status-pill status-pill-${sc.pill}`;
    drawerStatusPill.querySelector(".label").textContent = inst.status || "unknown";

    const root = el("div", {});
    const apiLink = inst.api_url
      ? el("a", { href: inst.api_url, target: "_blank", rel: "noreferrer", style: "color:var(--c-accent)" }, inst.api_url)
      : "—";
    root.appendChild(el("dl", { class: "kv" },
      ...kvRow("api url", apiLink),
      ...kvRow("created", fmtTime(inst.created_at)),
      ...kvRow("enabled", inst.enabled ? "yes" : "no"),
    ));

    const p = inst.ports || {};
    root.appendChild(el("div", { class: "section-label" }, "ports"));
    root.appendChild(el("dl", { class: "kv" },
      ...kvRow("auth", String(p.auth ?? "—")),
      ...kvRow("acct", String(p.acct ?? "—")),
      ...kvRow("coa", String(p.coa ?? "—")),
      ...kvRow("inner", String(p.inner ?? "—")),
      ...kvRow("api", String(p.api ?? "—")),
    ));

    const appendCreds = (label, obj) => {
      if (!obj) return;
      root.appendChild(el("div", { class: "section-label" }, label));
      const dl = el("dl", { class: "kv" });
      if (label === "database") {
        dl.append(...kvRow("host", `${obj.host || "—"}:${obj.port || "—"}`));
        dl.append(...kvRow("name", obj.name || "—"));
        dl.append(...kvRow("user", obj.user || "—"));
      } else {
        dl.append(...kvRow("user", obj.username || "—"));
      }
      if (obj.password) {
        dl.appendChild(el("dt", {}, "password"));
        dl.appendChild(el("dd", {}, el("code", {}, obj.password), copyBtn(obj.password)));
      } else if (obj.password_known === false) {
        dl.appendChild(el("dt", {}, "password"));
        dl.appendChild(el("dd", { class: "text-ink-400" }, "not stored"));
      }
      root.appendChild(dl);
    };
    appendCreds("database", inst.database);
    appendCreds("swagger", inst.swagger);

    root.appendChild(el("div", { style: "display:flex;gap:8px;margin-top:18px;flex-wrap:wrap" },
      el("button", { class: "btn btn-ghost", onclick: () => openDetail(inst.name) }, "refresh"),
      el("button", { class: "btn btn-danger", onclick: () => askDelete(inst.name) }, "delete instance"),
    ));

    drawerBody.replaceChildren(root);
  }

  async function openDetail(name) {
    drawerBody.replaceChildren(el("div", { class: "py-8 text-center text-ink-400" }, `loading ${name}…`));
    drawerTitle.textContent = name;
    drawerStatusPill.className = "status-pill status-pill-unknown";
    drawerStatusPill.querySelector(".label").textContent = "—";
    if (drawer.classList.contains("hidden")) openDialog(drawer, null);
    try {
      const inst = await apiFetch(`../v1/instances/${encodeURIComponent(name)}?include_secrets=true`);
      buildDetail(inst);
    } catch (e) {
      drawerBody.replaceChildren(el("div", { class: "text-bad py-4" }, `error: ${e.message}`));
    }
  }

  // ---- 7c. Delete -----------------------------------------------------------
  const confirmModal = $("#confirm-modal");
  const confirmText = $("#confirm-text");
  const confirmWithDB = $("#confirm-with-db");
  let pendingDelete = null;
  $("#confirm-cancel").addEventListener("click", () => closeDialog(confirmModal));
  $("#confirm-ok").addEventListener("click", async () => {
    if (!pendingDelete) return;
    const name = pendingDelete;
    const withDB = confirmWithDB.checked;
    closeDialog(confirmModal);
    pendingDelete = null;
    toast("info", `deleting ${name}`, withDB ? "with database drop" : "preserving database", 5000);
    try {
      const r = await apiFetch(`../v1/instances/${encodeURIComponent(name)}?with_db=${withDB}`, { method: "DELETE" });
      if (r.already_deleted) toast("warn", `${name} not found`, "marked as already deleted");
      else toast("ok", `deleted ${name}`, r.database_dropped ? "db dropped" : "db retained");
      if (!drawer.classList.contains("hidden")) closeDialog(drawer);
      pollInstances();
      pollServerInfo();
    } catch (e) {
      toast("bad", "delete failed", e.message, 7000);
    }
  });
  function askDelete(name) {
    pendingDelete = name;
    confirmText.innerHTML = `permanently delete instance <code>${name}</code>?`;
    confirmWithDB.checked = true;
    openDialog(confirmModal, () => { pendingDelete = null; });
  }

  // ---- 8. Theme + boot ------------------------------------------------------
  function applyTheme(t) {
    document.documentElement.setAttribute("data-theme", t);
    localStorage.setItem(THEME_KEY, t);
  }
  applyTheme(localStorage.getItem(THEME_KEY) || "dark");
  $("#theme-toggle").addEventListener("click", () => {
    const cur = document.documentElement.getAttribute("data-theme") || "dark";
    applyTheme(cur === "dark" ? "light" : "dark");
  });

  function refreshAll(force = false) {
    pollHealth();
    if (force || tokenStore.get()) {
      pollServerInfo();
      pollInstances();
    }
  }

  lucideRefresh();
  if (!tokenStore.get()) showTokenModal();
  refreshAll();
  setInterval(pollHealth, HEALTH_MS);
  setInterval(pollInstances, INSTANCES_MS);
  setInterval(pollServerInfo, INFO_MS);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") {
      refreshAll();
      lucideRefresh();
    }
  });
})();
