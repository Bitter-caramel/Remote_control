/* 底部面板 · 我的预定：列出本人正在占用或排队中的机器。
   不做网络轮询（只有「刷新」按钮），但闲置下线的倒计时在本地每秒刷新。 */
(function () {
  "use strict";

  var UI = window.UI;

  function fmtCountdown(ms) {
    var s = Math.max(0, Math.ceil(ms / 1000));
    var m = Math.floor(s / 60);
    s = s % 60;
    return m > 0 ? m + " 分 " + s + " 秒" : s + " 秒";
  }

  function mount(ctx) {
    var body = ctx.body;
    var head = UI.el("div", "dock-head");
    var refreshBtn = UI.el("button", "icon-btn", "刷新");
    head.append(UI.el("span", "title", "我的预定"), UI.el("span", "grow"), refreshBtn);

    var listBox = UI.el("div", "resv-list");
    var tip = UI.el("div", "dock-empty", "加载中…");
    body.append(head, listBox, tip);

    var items = [];
    var disposed = false;
    var timer = null;

    function render() {
      listBox.textContent = "";
      if (items.length === 0) {
        tip.textContent = "当前没有占用或排队中的机器";
        return;
      }
      tip.textContent = "";
      var now = Date.now();
      items.forEach(function (r) {
        var item = UI.el("div", "resv-item");
        var main = UI.el("div", "resv-main");
        var title = r.bot_name && r.bot_name !== "-" ? r.bot_name : r.bot_id;
        main.append(UI.el("div", "resv-title", title + "（" + r.bot_id + "）"));

        var sub = UI.el("div", "resv-sub");
        if (r.mode === "operator") {
          sub.textContent = "占用中 · 可以输入";
        } else {
          sub.textContent = "排队中（第 " + r.queue + " 位）" +
            (r.holder ? " · 当前由 " + r.holder + " 占用" : "");
        }
        main.append(sub);

        if (r.idle_deadline > 0) {
          var left = r.idle_deadline * 1000 - now;
          var warn = UI.el("div", "resv-sub bad");
          warn.textContent = left > 0
            ? "长时间无操作，将在 " + fmtCountdown(left) + " 后自动下线"
            : "闲置确认已超时";
          main.append(warn);
        }

        var btn = UI.el("button", "icon-btn danger", "放弃");
        btn.addEventListener("click", function () { giveUp(r.bot_id, btn); });
        item.append(main, btn);
        listBox.append(item);
      });
    }

    async function load() {
      try {
        items = (await UI.api.reservations()) || [];
      } catch (e) {
        if (disposed) return;
        listBox.textContent = "";
        tip.textContent = "无法获取预定列表: " + e.message;
        return;
      }
      if (disposed) return;
      render();
    }

    async function giveUp(botID, btn) {
      btn.disabled = true;
      try {
        await UI.api.release(botID);
      } catch (e) {
        if (!disposed) tip.textContent = "放弃失败: " + e.message;
        btn.disabled = false;
        return;
      }
      if (!disposed) load();
    }

    refreshBtn.addEventListener("click", function () { load(); });

    load();
    /* 本地倒计时：只改文字，不请求服务器 */
    timer = setInterval(function () {
      if (!disposed && items.some(function (r) { return r.idle_deadline > 0; })) render();
    }, 1000);

    return function dispose() {
      disposed = true;
      clearInterval(timer);
    };
  }

  UI.dock.register({
    id: "myresv",
    name: "我的预定",
    order: 10,
    mount: mount,
  });
})();
