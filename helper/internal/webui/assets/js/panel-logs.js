/* 日志面板：第二列选择日志来源，主区直接显示内容。
   数据来自 /api/logs，对应 DOS 面板里的 log 命令族。 */
(function () {
  "use strict";

  var UI = window.UI;
  var REFRESH_MS = 5000;

  function mount(ctx) {
    var sideHead = ctx.sideHead, sideBody = ctx.sideBody, stage = ctx.stage;

    var entries = [];
    var current = null;
    var disposed = false;

    var statusEl = UI.el("span", "status", "");
    var titleEl = UI.el("span", "title", "日志");
    var head = UI.el("div", "stage-head");
    head.append(titleEl, UI.el("span", "grow"), statusEl);

    var view = UI.el("pre", "log-view", "");
    var body = UI.el("div", "stage-body");
    body.append(view);
    stage.append(head, body);

    var refreshBtn = UI.el("button", "icon-btn", "刷新");
    refreshBtn.addEventListener("click", function () { load(); });
    sideHead.append(UI.el("span", "title", "日志"), UI.el("span", "grow"), refreshBtn);

    function renderRows() {
      sideBody.textContent = "";
      entries.forEach(function (it) {
        var row = UI.el("div", "row" + (current && it.key === current.key ? " active" : ""));
        var main = UI.el("div", "row-main");
        main.append(UI.el("div", "row-title", it.label), UI.el("div", "row-sub", it.sub));
        row.append(main);
        row.addEventListener("click", function () { pick(it); });
        sideBody.append(row);
      });
    }

    function pick(it) {
      current = it;
      titleEl.textContent = it.label;
      renderRows();
      load();
    }

    async function buildEntries() {
      var list = [{ key: "program", label: "程序日志", sub: "program.log", kind: "program" },
                  { key: "bots", label: "机器连接记录", sub: "bots.log", kind: "bots" }];
      try {
        var bots = await UI.api.bots();
        (bots || []).forEach(function (b) {
          list.push({
            key: "bot:" + b.id,
            label: b.name || b.id,
            sub: "终端录像 · " + b.id,
            kind: "bot",
            id: b.id,
          });
        });
      } catch (e) {
        /* 机器列表取不到时仍可看前两类日志 */
      }
      if (disposed) return;
      entries = list;
      if (!current || !entries.some(function (it) { return it.key === current.key; })) {
        current = entries[0];
        titleEl.textContent = current.label;
      }
      renderRows();
    }

    async function load() {
      if (!current) return;
      try {
        var data = await UI.api.logs(current.kind, current.id);
        if (disposed) return;
        if (data.error) {
          view.textContent = "读取失败: " + data.error;
          statusEl.textContent = "读取失败";
          statusEl.className = "status bad";
          return;
        }
        view.textContent = data.text || "（暂无内容）";
        statusEl.textContent = "更新于 " + new Date().toLocaleTimeString();
        statusEl.className = "status";
      } catch (e) {
        if (disposed) return;
        statusEl.textContent = "读取失败: " + e.message;
        statusEl.className = "status bad";
      }
    }

    async function tick() {
      await buildEntries();
      await load();
    }

    tick();
    var timer = setInterval(function () { if (!disposed) tick(); }, REFRESH_MS);

    return function dispose() {
      disposed = true;
      clearInterval(timer);
    };
  }

  UI.registerPanel({
    id: "logs",
    name: "日志",
    icon: "≡",
    order: 20,
    mount: mount,
  });
})();
