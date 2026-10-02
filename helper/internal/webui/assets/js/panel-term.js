/* 通信控制面板：左侧机器列表 + 右侧整块终端。
   主区就是一个真正的终端（xterm.js），没有聊天式发送框——
   接入后按键直接透传到远端 shell，与 DOS 下的终端窗口体验一致。
   控制权是独占的：空闲时接入即占用；被占用则排队，能看画面但不能输入。 */
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
    var mode = "observer"; // 本连接的控制权：operator | waiting | observer
    var idleTimer = null;  // 闲置询问的本地倒计时

    /* ---------- 主区：终端 ---------- */

    var statusEl = UI.el("span", "status", "未选择机器");
    var head = UI.el("div", "stage-head");
    head.append(UI.el("span", "title", "终端"), UI.el("span", "grow"), statusEl);

    /* 控制权提示条：排队/让位/被收回/闲置确认都走这里 */
    var banner = UI.el("div", "term-banner");
    banner.hidden = true;
    var bannerText = UI.el("span", "grow", "");
    var bannerBtns = UI.el("span", "inline");
    banner.append(bannerText, bannerBtns);

    var host = UI.el("div", "term-host");
    var body = UI.el("div", "stage-body");
    body.append(host);
    stage.append(head, banner, body);

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

    /* 模式 → 状态栏文案 */
    function modeText(queue) {
      if (mode === "operator") return "占用中";
      if (mode === "waiting") return "排队中" + (queue ? "（第 " + queue + " 位）" : "");
      return "观察者·只读";
    }

    function setMode(next, queue) {
      mode = next;
      setStatus(currentID ? "已接入 " + currentID + " · " + modeText(queue) : "未选择机器",
        mode === "operator" ? "ok" : null);
    }

    function showBanner(text, kind, buttons) {
      bannerText.textContent = text;
      banner.className = "term-banner" + (kind ? " " + kind : "");
      bannerBtns.textContent = "";
      (buttons || []).forEach(function (b) {
        var btn = UI.el("button", "mini-btn", b.label);
        btn.addEventListener("click", b.onClick);
        bannerBtns.append(btn);
      });
      banner.hidden = false;
    }

    function hideBanner() {
      banner.hidden = true;
      bannerBtns.textContent = "";
      if (idleTimer) {
        clearInterval(idleTimer);
        idleTimer = null;
      }
    }

    /* 闲置确认：1 分钟倒计时，本地刷新文案；「是」下线，「否」重新计时 5 分钟 */
    function showIdlePrompt(deadline) {
      var end = deadline * 1000;
      function tick() {
        var left = end - Date.now();
        if (left <= 0) {
          showBanner("闲置确认超时，正在下线…", "bad", []);
          if (idleTimer) {
            clearInterval(idleTimer);
            idleTimer = null;
          }
          return;
        }
        var s = Math.ceil(left / 1000);
        showBanner("5 分钟没有输入且机器无输出，是否下线？（" + s + " 秒后自动下线）", null, [
          { label: "是", onClick: function () { answerIdle(true); } },
          { label: "否", onClick: function () { answerIdle(false); } },
        ]);
      }
      if (idleTimer) clearInterval(idleTimer);
      tick();
      idleTimer = setInterval(tick, 1000);
    }

    function answerIdle(keep) {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "idle_answer", keep: keep }));
      }
      hideBanner();
      if (!keep) {
        showBanner("已选择继续，重新计 5 分钟无操作。", "ok", []);
      }
    }

    /* 把本终端的行列数同步给远端 PTY（尺寸只有占用者能改）。
       必须做：远端 ConPTY 按自己记录的行数清屏，两边不一致时 cls 只会清掉
       它已知的那几行，浏览器比远端高的话就会留下「清不掉的下半屏」。
       除了尺寸变化，接管控制权时也要补发一次——接入时会先 fit 再收到 ready，
       那一次 onResize 因为还不是占用者被丢掉了，否则远端会一直停在初始尺寸。 */
    function sendSize() {
      if (mode !== "operator") return;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    }

    term.onResize(sendSize);

    term.onData(function (data) {
      /* 只读与排队者的按键不发送；服务端还会再拦一道 */
      if (!UI.session.state.canOperate || mode !== "operator") return;
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
      hideBanner();
      term.reset();
      setStatus("连接中…", null);

      var url = "ws://" + location.host + "/api/term?bot=" + encodeURIComponent(id);
      var sock = new WebSocket(url);
      ws = sock;

      sock.onopen = function () {
        if (disposed || ws !== sock) return;
        mode = "observer";
        setStatus("已接入 " + id, "ok");
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
        switch (msg.type) {
          case "output":
            term.write(UI.b64decode(msg.data));
            break;
          case "ready":
            setMode(msg.mode, msg.queue);
            sendSize(); // 占用者接入时把尺寸同步过去，远端才会按本终端的行数清屏
            if (mode === "operator") term.focus();
            break;
          case "queued":
            setMode("waiting", msg.queue);
            showBanner(msg.text || ("已被 " + (msg.holder || "他人") + " 占用，已进入队列"), null, []);
            break;
          case "granted":
            setMode("operator");
            hideBanner();
            showBanner(msg.text || "轮到你了，已获得控制权", "ok", []);
            sendSize(); // 换人操作时按新占用者的终端尺寸重设远端 PTY
            term.focus();
            break;
          case "revoked":
            setMode("observer");
            hideBanner();
            showBanner(msg.text || "控制权已被收回", "bad", []);
            break;
          case "idle_prompt":
            showIdlePrompt(msg.deadline || 0);
            break;
          case "bye": {
            var why = msg.data || "连接已结束";
            term.write("\r\n\x1b[31m[" + why + "]\x1b[0m\r\n");
            setMode("observer");
            setStatus(why, "bad");
            hideBanner();
            break;
          }
          case "error":
            term.write("\r\n\x1b[31m[" + msg.data + "]\x1b[0m\r\n");
            setStatus(msg.data, "bad");
            break;
        }
      };

      sock.onclose = function () {
        if (disposed || ws !== sock) return;
        ws = null;
        hideBanner();
        mode = "observer";
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
        var occ = b.occupancy || { text: "无人占用", occupied: false, queue: 0, observers: 0 };
        var occLine = UI.el("div", "row-sub occupancy" + (occ.occupied ? " busy" : ""), occ.text);
        if (occ.observers > 0) {
          occLine.append(UI.el("span", "occ-observer", " · " + occ.observers + " 人观察中"));
        }
        if (occ.queue > 0) {
          occLine.append(UI.el("span", "occ-queue", " · " + occ.queue + " 人排队"));
        }
        main.append(
          UI.el("div", "row-title", b.name || b.id),
          UI.el("div", "row-sub", b.id + " · " + (b.os || "未知系统")),
          occLine
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
      hideBanner();
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
