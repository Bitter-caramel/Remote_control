/* 播报面板：占用 / 排队 / 释放的历史记录。
   只有点进这个面板才会拉取（GET /api/events），不做常驻推送，
   因此对带宽与性能几乎没有影响。可见范围与机器列表一致：服务端按机器等级过滤。 */
(function () {
  "use strict";

  var UI = window.UI;
  var REFRESH_MS = 5000;
  var LIMIT = 300;

  var KIND_TEXT = { occupy: "接入", release: "离开" };

  function pad(n) { return n < 10 ? "0" + n : String(n); }

  function timeText(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return "--:--:--";
    return pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
  }

  function mount(ctx) {
    var sideHead = ctx.sideHead, sideBody = ctx.sideBody, stage = ctx.stage;

    var events = [];
    var filter = ""; // "" = 全部
    var disposed = false;

    var statusEl = UI.el("span", "status", "");
    var titleEl = UI.el("span", "title", "播报");
    var head = UI.el("div", "stage-head");
    head.append(titleEl, UI.el("span", "grow"), statusEl);

    var listEl = UI.el("div", "event-list");
    var body = UI.el("div", "stage-body");
    body.append(listEl);
    stage.append(head, body);

    var refreshBtn = UI.el("button", "icon-btn", "刷新");
    refreshBtn.addEventListener("click", function () { load(); });
    sideHead.append(UI.el("span", "title", "播报"), UI.el("span", "grow"), refreshBtn);

    /* 左列：全部 + 从已拉取事件里派生的机器（不再请求 /api/bots，
       这样已下线机器的历史记录依然可以筛选） */
    function renderSide() {
      sideBody.textContent = "";
      var seen = {};
      var bots = [];
      events.forEach(function (ev) {
        if (seen[ev.bot_id]) return;
        seen[ev.bot_id] = true;
        bots.push({ id: ev.bot_id, name: ev.bot_name && ev.bot_name !== "-" ? ev.bot_name : ev.bot_id });
      });
      bots.sort(function (a, b) { return a.id < b.id ? -1 : 1; });

      var all = UI.el("div", "row" + (filter === "" ? " active" : ""));
      var allMain = UI.el("div", "row-main");
      allMain.append(
        UI.el("div", "row-title", "全部"),
        UI.el("div", "row-sub", events.length + " 条记录")
      );
      all.append(allMain);
      all.addEventListener("click", function () { filter = ""; renderSide(); renderList(); });
      sideBody.append(all);

      bots.forEach(function (b) {
        var row = UI.el("div", "row" + (filter === b.id ? " active" : ""));
        var main = UI.el("div", "row-main");
        var n = events.filter(function (e) { return e.bot_id === b.id; }).length;
        main.append(UI.el("div", "row-title", b.name), UI.el("div", "row-sub", b.id + " · " + n + " 条"));
        row.append(main);
        row.addEventListener("click", function () { filter = b.id; renderSide(); renderList(); });
        sideBody.append(row);
      });
    }

    function renderList() {
      listEl.textContent = "";
      var shown = filter ? events.filter(function (e) { return e.bot_id === filter; }) : events;
      if (shown.length === 0) {
        listEl.append(UI.el("div", "empty", "暂无记录"));
        return;
      }
      shown.forEach(function (ev) {
        var item = UI.el("div", "event-item");
        var kind = ev.kind || "occupy";
        var bot = ev.bot_name && ev.bot_name !== "-" ? ev.bot_name + "(" + ev.bot_id + ")" : ev.bot_id;
        item.append(
          UI.el("span", "ev-time", timeText(ev.time)),
          UI.el("span", "ev-kind " + kind, KIND_TEXT[kind] || kind),
          UI.el("span", "ev-main", (ev.actor || "未知") + " · " + (ev.detail || "") + " · " + bot)
        );
        listEl.append(item);
      });
    }

    async function load() {
      try {
        events = (await UI.api.events(LIMIT)) || [];
      } catch (e) {
        if (disposed) return;
        statusEl.textContent = "读取失败: " + e.message;
        statusEl.className = "status bad";
        return;
      }
      if (disposed) return;
      statusEl.textContent = "更新于 " + new Date().toLocaleTimeString();
      statusEl.className = "status";
      renderSide();
      renderList();
    }

    load();
    var timer = setInterval(function () { if (!disposed) load(); }, REFRESH_MS);

    return function dispose() {
      disposed = true;
      clearInterval(timer);
    };
  }

  UI.registerPanel({
    id: "events",
    name: "播报",
    icon: "◎",
    order: 30,
    mount: mount,
  });
})();
