/* 登录态：整个控制台由它决定何时启动。
   未登录时只有登录卡片，不构建任何面板；登录后按角色过滤左侧导航。

   角色：1 观察者（只读）/ 2 普通用户（可操作）/ 3 管理员（另有账号与机器管理）。
   前端隐藏只是为了界面清爽，真正的门禁在服务端：
   每个 /api/* 都会重新校验会话与角色。 */
(function () {
  "use strict";

  var UI = window.UI;

  var state = { username: "", displayName: "", role: 0, canOperate: false };

  var mask = document.getElementById("login");
  var form = document.getElementById("login-card");
  var userInput = document.getElementById("login-user");
  var passInput = document.getElementById("login-pass");
  var msgEl = document.getElementById("login-msg");
  var submitBtn = document.getElementById("login-submit");

  function roleName(role) {
    var hit = UI.ROLES.filter(function (r) { return r.value === role; })[0];
    return hit ? hit.label : "未登录";
  }

  /* 当前身份是否达到某个角色档位（面板按此决定是否出现在导航栏） */
  function can(minRole) { return state.role >= (minRole || 0); }

  function applyUser(u) {
    state.username = u.username;
    state.displayName = u.display_name || u.username;
    state.role = u.role;
    state.canOperate = !!u.can_operate;
  }

  function setMsg(text) {
    if (!msgEl) return;
    msgEl.textContent = text || "";
    msgEl.className = "login-msg" + (text ? " bad" : "");
  }

  function showLogin(message) {
    if (mask) mask.hidden = false;
    setMsg(message);
    if (passInput) {
      passInput.value = "";
      passInput.focus();
    }
  }

  function hideLogin() {
    if (mask) mask.hidden = true;
  }

  function setBusy(busy) {
    if (!submitBtn) return;
    submitBtn.disabled = busy;
    submitBtn.textContent = busy ? "登录中…" : "登录";
  }

  if (form) {
    form.addEventListener("submit", async function (ev) {
      ev.preventDefault();
      if (submitBtn && submitBtn.disabled) return;
      setBusy(true);
      setMsg("");
      try {
        applyUser(await UI.api.login(userInput.value.trim(), passInput.value));
        hideLogin();
        UI.startShell();
      } catch (e) {
        setMsg(e.message || "登录失败");
      } finally {
        setBusy(false);
      }
    });
  }

  /* 会话失效（登出、被踢下线、超时）：由 api.js 在收到 401 时调用 */
  function expired() {
    var wasLoggedIn = state.role !== 0;
    state.role = 0;
    state.canOperate = false;
    if (wasLoggedIn) {
      showLogin("登录状态已失效，请重新登录");
    }
  }

  async function logout() {
    if (!window.confirm("确定退出登录？")) return;
    try {
      await UI.api.logout();
    } catch (e) {
      /* 会话可能已失效，忽略即可 */
    }
    location.reload();
  }

  async function boot() {
    try {
      applyUser(await UI.api.me());
      hideLogin();
      UI.startShell();
    } catch (e) {
      /* 首次打开尚未登录属于正常情况，api.js 已把 401 转过来了 */
      showLogin("");
    }
  }

  window.UI.session = {
    state: state,
    roleName: roleName,
    can: can,
    applyUser: applyUser,
    showLogin: showLogin,
    expired: expired,
    logout: logout,
    boot: boot,
  };
})();
