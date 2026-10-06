/* 底部面板（仿 VSCode panel）的注册表与外壳。
   与左侧面板同一套思路：外壳只认识注册表，不认识任何具体功能；
   功能模块（dock-*.js）各自 register 自己，往面板里加功能不必改动这里。

   约定字段：
     id       唯一标识
     name     标签页显示名
     order    排序权重，越小越靠前
     minRole  可见所需的最低角色，不填表示登录即可见
     global   true 表示与「当前操作上下文」无关（跨机器功能），不随上下文切换重挂
     mount({ body, ctx, setTitle }) 在被激活时调用，返回可选的 dispose 函数；
              ctx 是当前操作上下文（{ botID, ctxID, mode, role, ownerName }），
              非 global 标签在没有上下文时不会被挂载，而是显示占位提示。
   激活时才挂载、收起时立即卸载：没打开的功能不会占用任何网络与定时器。 */
(function () {
  "use strict";

  var UI = window.UI;
  var tabs = [];
  var started = false;
  var collapsed = true;
  var activeID = null;
  var dispose = null;

  var dockEl = document.getElementById("dock");
  var tabsEl = document.getElementById("dock-tabs");
  var bodyEl = document.getElementById("dock-body");
  var toggleBtn = null;

  function register(tab) {
    if (!tab || !tab.id || typeof tab.mount !== "function") {
      throw new Error("UI.dock.register: 必须包含 id 与 mount");
    }
    if (tabs.some(function (t) { return t.id === tab.id; })) {
      throw new Error("UI.dock.register: 标签 id 重复: " + tab.id);
    }
    tabs.push(tab);
  }

  function visibleTabs() {
    return tabs.slice().sort(function (a, b) {
      return (a.order || 100) - (b.order || 100);
    }).filter(function (t) {
      return !t.minRole || UI.session.can(t.minRole);
    });
  }

  function unmount() {
    if (dispose) {
      try {
        dispose();
      } catch (e) {
        /* 清理失败不应阻塞切换 */
      }
      dispose = null;
    }
    bodyEl.textContent = "";
  }

  function activeTab() {
    return visibleTabs().filter(function (t) { return t.id === activeID; })[0] || null;
  }

  function mountActive() {
    unmount();
    var tab = activeTab();
    if (!tab) return;
    var cur = UI.ctx ? UI.ctx.get() : null;
    if (!tab.global && !cur) {
      bodyEl.append(UI.el("div", "dock-empty",
        "请先在终端面板选择一台机器并接入一个操作上下文。"));
      return;
    }
    dispose = tab.mount({
      body: bodyEl,
      ctx: tab.global ? null : cur,
      setTitle: function (text) {
        var el = bodyEl.querySelector(".dock-head .title");
        if (el) el.textContent = text;
      },
    }) || null;
  }

  /* 展开/收起。收起时卸载当前标签，避免后台继续跑定时器 */
  function setCollapsed(next) {
    collapsed = next;
    dockEl.classList.toggle("collapsed", collapsed);
    if (toggleBtn) toggleBtn.textContent = collapsed ? "▴ 展开" : "▾ 收起";
    if (collapsed) {
      unmount();
    } else {
      if (!activeID) {
        var first = visibleTabs()[0];
        activeID = first ? first.id : null;
      }
      renderTabs();
      mountActive();
    }
  }

  function activate(id) {
    if (activeID === id && !collapsed) {
      setCollapsed(true); // 再点当前标签 = 收起（与 VSCode 一致）
      return;
    }
    var need = visibleTabs().some(function (t) { return t.id === id; });
    if (!need) return;
    activeID = id;
    collapsed = false;
    dockEl.classList.remove("collapsed");
    if (toggleBtn) toggleBtn.textContent = "▾ 收起";
    renderTabs();
    mountActive();
  }

  function renderTabs() {
    tabsEl.textContent = "";
    visibleTabs().forEach(function (tab) {
      var btn = UI.el("button", "dock-tab" + (tab.id === activeID && !collapsed ? " active" : ""), tab.name);
      btn.title = tab.name;
      btn.addEventListener("click", function () { activate(tab.id); });
      tabsEl.append(btn);
    });
    tabsEl.append(UI.el("span", "dock-grow"));
    toggleBtn = UI.el("button", "dock-toggle", collapsed ? "▴ 展开" : "▾ 收起");
    toggleBtn.title = "展开/收起底部面板";
    toggleBtn.addEventListener("click", function () { setCollapsed(!collapsed); });
    tabsEl.append(toggleBtn);
  }

  /* 外壳启动后调用一次（shell.js 在挂载完左侧面板后调用） */
  function start() {
    if (started) return;
    started = true;
    renderTabs();
    setCollapsed(true);

    /* 当前操作上下文变化：非 global 的标签重挂（先卸载再按新上下文挂载） */
    if (UI.ctx) {
      UI.ctx.onChange(function () {
        if (collapsed) return;
        var tab = activeTab();
        if (tab && tab.global) return; // 跨机器功能不受上下文切换影响
        mountActive();
      });
    }
  }

  window.UI.dock = {
    register: register,
    start: start,
  };
})();
