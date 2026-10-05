/* 后端接口封装与通用小工具。
   所有 /api/* 都要求已登录：收到 401 时交给 session.js 弹回登录页。 */
(function () {
  "use strict";

  async function request(method, url, body) {
    const opts = { method: method, cache: "no-store", headers: {} };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }

    const resp = await fetch(url, opts);
    if (resp.status === 401) {
      window.UI.session.expired();
      throw new Error("登录状态已失效");
    }

    const text = await resp.text();
    let data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch (e) {
        data = null;
      }
    }
    if (!resp.ok) {
      throw new Error((data && data.error) || "HTTP " + resp.status);
    }
    return data;
  }

  const api = {
    /* 登录态 */
    me: () => request("GET", "/api/me"),
    login: (username, password) => request("POST", "/api/login", { username, password }),
    logout: () => request("POST", "/api/logout"),

    /* 在线机器列表（服务端已按角色过滤） */
    bots: () => request("GET", "/api/bots"),
    /* 日志：kind = program | bots | bot（bot 需带 id） */
    logs: (kind, id) =>
      request("GET", "/api/logs?kind=" + encodeURIComponent(kind) +
        (id ? "&id=" + encodeURIComponent(id) : "")),

    /* 接入/接续/释放的播报历史（服务端已按机器等级过滤） */
    events: (limit) =>
      request("GET", "/api/events" + (limit ? "?limit=" + encodeURIComponent(limit) : "")),

    /* 操作上下文 */
    ctxList: (botID) =>
      request("GET", "/api/bots/" + encodeURIComponent(botID) + "/ctx"),
    ctxOpen: (botID) =>
      request("POST", "/api/bots/" + encodeURIComponent(botID) + "/ctx", {}),
    ctxRequest: (ctxID, mode) =>
      request("POST", "/api/ctx/" + encodeURIComponent(ctxID) + "/request", { mode: mode }),
    ctxRequests: () => request("GET", "/api/ctx/requests"),
    ctxAnswer: (id, accept) =>
      request("POST", "/api/ctx/requests/" + encodeURIComponent(id) + "/answer", { accept: !!accept }),
    ctxGrants: () => request("GET", "/api/ctx/grants"),
    ctxRevoke: (id) =>
      request("POST", "/api/ctx/grants/" + encodeURIComponent(id) + "/revoke", {}),
    setWatchDefault: (on) => request("POST", "/api/me/watch-default", { on: !!on }),

    /* 机器管理（管理员） */
    botsAll: () => request("GET", "/api/bots/all"),
    setBotMinRole: (id, minRole) =>
      request("POST", "/api/bots/" + encodeURIComponent(id) + "/min-role", { min_role: minRole }),
    setBotNote: (id, note) =>
      request("POST", "/api/bots/" + encodeURIComponent(id) + "/note", { note: note }),

    /* 账号管理（管理员） */
    users: () => request("GET", "/api/users"),
    createUser: (user) => request("POST", "/api/users", user),
    setUserRole: (id, role) => request("POST", "/api/users/" + id + "/role", { role: role }),
    setUserPassword: (id, password) =>
      request("POST", "/api/users/" + id + "/password", { password: password || "" }),
    setUserDisabled: (id, disabled) =>
      request("POST", "/api/users/" + id + "/disabled", { disabled: disabled }),
    deleteUser: (id) => request("DELETE", "/api/users/" + id),
  };

  /* 角色档位，与服务端 store 包保持一致 */
  const ROLES = [
    { value: 1, label: "观察者" },
    { value: 2, label: "普通用户" },
    { value: 3, label: "管理员" },
  ];

  /* 文本 → base64（终端按键以 UTF-8 字节传输） */
  function b64encode(str) {
    const bytes = new TextEncoder().encode(str);
    let bin = "";
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin);
  }

  /* base64 → 字节数组（终端输出可能是任意字节，不能按字符串处理） */
  function b64decode(b64) {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }

  function el(tag, cls, text) {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  /* 角色下拉框（账号管理与机器等级共用） */
  function roleSelect(value) {
    const sel = el("select", "input");
    ROLES.forEach(function (r) {
      const opt = el("option", null, r.label);
      opt.value = String(r.value);
      if (r.value === value) opt.selected = true;
      sel.append(opt);
    });
    return sel;
  }

  /* 一行表单：标签 + 控件 */
  function field(label, control) {
    const wrap = el("label", "field");
    wrap.append(el("span", null, label), control);
    return wrap;
  }

  /* 主区里的一段表单区块 */
  function section(title) {
    const box = el("div", "section");
    box.append(el("div", "section-title", title));
    return box;
  }

  window.UI = window.UI || {};
  window.UI.api = api;
  window.UI.ROLES = ROLES;
  window.UI.b64encode = b64encode;
  window.UI.b64decode = b64decode;
  window.UI.el = el;
  window.UI.roleSelect = roleSelect;
  window.UI.field = field;
  window.UI.section = section;
})();
