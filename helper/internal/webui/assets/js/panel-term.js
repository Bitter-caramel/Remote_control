/* 通信控制面板：左侧机器列表 + 右侧整块终端。
   主区就是一个真正的终端（xterm.js），没有聊天式发送框——
   接入后按键直接透传到远端 shell，与 DOS 下的终端窗口体验一致。
   多个浏览器可同时接入同一台机器：输出广播给所有人，输入都生效。 */
(function () {
  "use strict";

  var UI = window.UI;
  var POLL_MS = 2000;

  function mount(ctx) {
    var sideHead = ctx.sideHead, sideBody = ctx.sideBody, stage = ctx.stage;

    var bots = [];
    var currentID = null;
    var ws = null;
    var disposed = false;

    /* ---------- 主区：终端 ---------- */

    var statusEl = UI.el("span", "status", "未选择机器");
    var head = UI.el("div", "stage-head");
    head.append(UI.el("span", "title", "终端"), UI.el("span", "grow"), statusEl);

    var host = UI.el("div", "term-host");
    var body = UI.el("div", "stage-body");
    body.append(host);
    stage.append(head, body);

    var term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      scrollback: 5000,
      fontFamily: 'Consolas, "Cascadia Mono", "Microsoft YaHei Mono", monospace',
      theme: { background: "#1e1e1e" },
    });
    var fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(host);
    term.write("\x1b[90m在左侧选择一台机器即可接入它的终端。\x1b[0m\r\n");

    function fitNow() {
      try {
        fit.fit();
      } catch (e) {
        /* 尺寸为 0 时忽略（面板尚未布局完成） */
      }
    }

    function setStatus(text, kind) {
      statusEl.textContent = text;
      statusEl.className = "status" + (kind ? " " + kind : "");
    }

    term.onResize(function (size) {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: size.cols, rows: size.rows }));
      }
    });

    term.onData(function (data) {
      /* 观察者只能看：按键不发送，服务端也会再丢弃一次 */
      if (!UI.session.state.canOperate) return;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "input", data: UI.b64encode(data) }));
      }
    });

    function connect(id) {
      if (ws) {
        ws.onclose = null;
        ws.close();
        ws = null;
      }
      term.reset();
      setStatus("连接中…", null);

      var url = "ws://" + location.host + "/api/term?bot=" + encodeURIComponent(id);
      var sock = new WebSocket(url);
      ws = sock;

      sock.onopen = function () {
        if (disposed || ws !== sock) return;
        if (UI.session.state.canOperate) {
          setStatus("已接入 " + id, "ok");
          term.focus();
        } else {
          setStatus("已接入 " + id + "（观察者·只读）", "ok");
        }
        fitNow();
      };

      sock.onmessage = function (ev) {
        if (disposed || ws !== sock) return;
        var msg;
        try {
          msg = JSON.parse(ev.data);
        } catch (e) {
          return;
        }
        if (msg.type === "output") {
          term.write(UI.b64decode(msg.data));
        } else if (msg.type === "bye") {
          var why = msg.data || "该机器已下线";
          term.write("\r\n\x1b[31m[" + why + "]\x1b[0m\r\n");
          setStatus(why, "bad");
        } else if (msg.type === "error") {
          term.write("\r\n\x1b[31m[" + msg.data + "]\x1b[0m\r\n");
          setStatus(msg.data, "bad");
        }
      };

      sock.onclose = function () {
        if (disposed || ws !== sock) return;
        ws = null;
        setStatus("连接已断开（点击左侧机器可重连）", "bad");
      };
    }

    /* ---------- 第二列：机器列表 ---------- */

    var refreshBtn = UI.el("button", "icon-btn", "刷新");
    refreshBtn.addEventListener("click", function () { refresh(); });
    sideHead.append(UI.el("span", "title", "机器"), UI.el("span", "grow"), refreshBtn);

    function renderRows() {
      sideBody.textContent = "";
      if (bots.length === 0) {
        sideBody.append(UI.el("div", "empty", "当前没有机器在线"));
        return;
      }
      bots.forEach(function (b) {
        var row = UI.el("div", "row" + (b.id === currentID ? " active" : ""));
        row.append(UI.el("span", "dot" + (b.buffered ? " warn" : "")));
        var main = UI.el("div", "row-main");
        main.append(
          UI.el("div", "row-title", b.name || b.id),
          UI.el("div", "row-sub", b.id + " · " + (b.os || "未知系统"))
        );
        row.append(main);
        row.addEventListener("click", function () { select(b.id); });
        sideBody.append(row);
      });
    }

    function select(id) {
      currentID = id;
      renderRows();
      connect(id);
    }

    async function refresh() {
      var list;
      try {
        list = await UI.api.bots();
      } catch (e) {
        setStatus("无法获取机器列表: " + e.message, "bad");
        return;
      }
      if (disposed) return;
      bots = list || [];
      renderRows();
      // 首次进入自动接入第一台在线机器
      if (!currentID && bots.length > 0) {
        select(bots[0].id);
      }
    }

    function onWindowResize() { fitNow(); }
    window.addEventListener("resize", onWindowResize);

    refreshNow();
    var timer = setInterval(refreshNow, POLL_MS);

    function refreshNow() {
      if (!disposed) refresh();
    }

    if (window.UIState && window.UIState.bot) {
      select(window.UIState.bot);
    }

    return function dispose() {
      disposed = true;
      clearInterval(timer);
      window.removeEventListener("resize", onWindowResize);
      if (ws) {
        ws.onclose = null;
        ws.close();
      }
      term.dispose();
    };
  }

  UI.registerPanel({
    id: "term",
    name: "终端",
    icon: "▶",
    order: 10,
    mount: mount,
  });
})();
