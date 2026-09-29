/* 控制台外壳：只负责三栏骨架与面板切换，不认识任何具体功能。
   左侧导航栏由注册表生成——面板即功能是唯一事实来源。
   外壳在登录成功后才启动（session.js 调用 startShell），
   并按当前角色过滤掉无权访问的面板。 */
(function () {
  "use strict";

  var UI = window.UI;

  var rail = document.getElementById("rail");
  var sideHead = document.getElementById("side-head");
  var sideBody = document.getElementById("side-body");
  var stage = document.getElementById("stage");

  var started = false;
  var buttons = {};
  var dispose = null;
  var activeID = null;

  /* 当前身份能看到的面板 */
  function visiblePanels() {
    return UI.allPanels().filter(function (p) {
      return !p.minRole || UI.session.can(p.minRole);
    });
  }

  function activate(id) {
    var panels = visiblePanels();
    var panel = panels.filter(function (p) { return p.id === id; })[0] || panels[0];
    if (!panel) return;
    if (activeID === panel.id) return;

    if (dispose) {
      try {
        dispose();
      } catch (e) {
        /* 面板清理失败不应阻塞切换 */
      }
      dispose = null;
    }

    activeID = panel.id;
    Object.keys(buttons).forEach(function (k) {
      buttons[k].classList.toggle("active", k === panel.id);
    });
    if (location.hash.slice(1) !== panel.id) {
      history.replaceState(null, "", "#" + panel.id);
    }

    sideHead.textContent = "";
    sideBody.textContent = "";
    stage.textContent = "";

    dispose = panel.mount({ sideHead: sideHead, sideBody: sideBody, stage: stage }) || null;
  }

  window.addEventListener("hashchange", function () {
    if (started) activate(location.hash.slice(1));
  });

  UI.startShell = function () {
    if (started) return;
    started = true;

    /* 支持 ?bot=Bxxxx 直接跳到某台机器 */
    var params = new URLSearchParams(location.search);
    window.UIState = { bot: params.get("bot") || "" };

    rail.append(UI.el("div", "brand", "RA"));

    visiblePanels().forEach(function (panel) {
      var btn = UI.el("button", "rail-item");
      btn.append(UI.el("span", "ico", panel.icon), UI.el("span", null, panel.name));
      btn.title = panel.name;
      btn.addEventListener("click", function () { activate(panel.id); });
      buttons[panel.id] = btn;
      rail.append(btn);
    });

    /* 底部：当前身份 + 退出登录 */
    var me = UI.session.state;
    var foot = UI.el("div", "rail-foot");
    var userBtn = UI.el("button", "rail-item");
    userBtn.append(
      UI.el("span", "ico", "●"),
      UI.el("span", null, UI.session.roleName(me.role))
    );
    userBtn.title = me.displayName + "（" + me.username + "）· " +
      UI.session.roleName(me.role) + "\n点击退出登录";
    userBtn.addEventListener("click", function () { UI.session.logout(); });
    foot.append(userBtn);
    rail.append(foot);

    var first = visiblePanels()[0];
    activate(location.hash.slice(1) || (first && first.id));
  };

  UI.session.boot();
})();
