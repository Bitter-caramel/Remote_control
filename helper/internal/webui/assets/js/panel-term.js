/* 通信控制面板：左侧机器列表 + 右侧整块终端。
   每台机器上，每个用户各有一条独立的「操作上下文」——对应被控端一个独立 shell，
   默认私密、互不干扰。接入某条上下文后才开始收发；输入是独占的
   （同一时刻只有持有输入锁的人能打字）。他人的上下文要申请授权后才能观看或接续。 */
(function () {
  "use strict";

  var UI = window.UI;
  var POLL_MS = 2000;

  /* 访问模式/角色 → 界面文案 */
  function roleText(role, ownerName) {
    if (role === "owner") return "我的操作上下文";
    if (role === "operate") return "已接续操作 · 所有者 " + (ownerName || "对方");
    return "只读观看 · " + (ownerName || "对方") + " 的上下文";
  }

  /* store 的访问模式 → core 的订阅角色 */
  function roleOfMode(mode) {
    if (mode === "owner") return "owner";
    if (mode === "operate") return "operate";
    return "watch";
  }

  function mount(ctx) {
    var sideHead = ctx.sideHead, sideBody = ctx.sideBody, stage = ctx.stage;

    var bots = [];
    var currentBotID = null;
    var curCtx = null;   // { ctxID, ownerName, mode, role, inputName }
    var ws = null;
    var disposed = false;
    var mode = "observer"; // 本连接是否持有输入锁：operator | observer
    var role = "watch";    // owner | operate | watch

    /* ---------- 主区：终端 ---------- */

    var statusEl = UI.el("span", "status", "未选择机器");
    var head = UI.el("div", "stage-head");
    head.append(UI.el("span", "title", "终端"), UI.el("span", "grow"), statusEl);

    /* 上下文信息条：当前接入的是谁的上下文 + 可执行动作 */
    var ctxBar = UI.el("div", "ctx-bar");
    var ctxInfo = UI.el("span", "ctx-info", "未接入任何操作上下文");
    var ctxActions = UI.el("span", "inline");
    ctxBar.append(ctxInfo, UI.el("span", "grow"), ctxActions);

    /* 其它上下文入口：可切换的 / 需申请的 */
    var ctxChoices = UI.el("div", "ctx-choices");
    ctxChoices.hidden = true;

    /* 临时提示条（申请结果、被接续、上下文结束等） */
    var banner = UI.el("div", "term-banner");
    banner.hidden = true;
    var bannerText = UI.el("span", "grow", "");
    var bannerBtns = UI.el("span", "inline");
    banner.append(bannerText, bannerBtns);

    var host = UI.el("div", "term-host");
    var body = UI.el("div", "stage-body");
    body.append(host);
    stage.append(head, ctxBar, ctxChoices, banner, body);

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
    term.write("\x1b[90m在左侧选择一台机器即可接入它的操作上下文。\x1b[0m\r\n");

    function fitNow() {
      try {
        fit.fit();
      } catch (e) {
        /* 尺寸为 0 时忽略（面板尚未布局完成） */
      }
    }

    /* 终端容器尺寸一变就重新 fit：不依赖 window.resize（window 尺寸没变、但容器
       可能因侧栏/主区布局变化而变），避免终端停留在旧行数导致显示错位或半屏空白。 */
    var resizeObs = ("ResizeObserver" in window)
      ? new ResizeObserver(function () { fitNow(); })
      : null;
    if (resizeObs) resizeObs.observe(host);

    function realtime() { return role === "owner" || role === "operate"; }

    function setStatus(text, kind) {
      statusEl.textContent = text;
      statusEl.className = "status" + (kind ? " " + kind : "");
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
    }

    /* 当前选中机器的摘要（供「控制台」按机器类型定制功能使用） */
    function currentBot() {
      for (var i = 0; i < bots.length; i++) {
        if (bots[i].id === currentBotID) return bots[i];
      }
      return null;
    }

    /* 把当前上下文同步给底部面板等功能 */
    function publishCtx() {
      if (!curCtx) {
        UI.ctx.set(null);
        return;
      }
      var bot = currentBot();
      UI.ctx.set({
        botID: currentBotID,
        botName: bot ? bot.name : "",
        os: bot ? bot.os : "",
        ctxID: curCtx.ctxID,
        mode: curCtx.mode,
        role: role,
        ownerName: curCtx.ownerName,
      });
    }

    function renderCtxBar() {
      ctxActions.textContent = "";
      if (!curCtx) {
        ctxInfo.textContent = "未接入任何操作上下文";
        return;
      }
      var who = curCtx.ownerName || "对方";
      if (role === "owner") {
        ctxInfo.textContent = "我的操作上下文（" + curCtx.ctxID + "）" +
          (curCtx.inputName && curCtx.inputName !== who ? " · 输入权在 " + curCtx.inputName : "");
      } else {
        ctxInfo.textContent = roleText(role, who);
        /* 只读观看且有操作能力时，给出「申请接续」入口 */
        if (role === "watch" && UI.session.state.canOperate) {
          var btn = UI.el("button", "mini-btn", "申请接续");
          btn.addEventListener("click", function () {
            requestAccess({ id: curCtx.ctxID, owner_name: curCtx.ownerName }, "operate");
          });
          ctxActions.append(btn);
        }
      }
    }

    /* 其它上下文：可切换的直接接入；未授权的给出申请入口 */
    function renderChoices(res, activeID) {
      ctxChoices.textContent = "";
      var others = (res.contexts || []).filter(function (c) { return c.id !== activeID; });
      if (others.length === 0) {
        ctxChoices.hidden = true;
        return;
      }
      ctxChoices.hidden = false;
      ctxChoices.append(UI.el("span", "ctx-choices-label", "其它上下文："));
      others.forEach(function (c) {
        var who = c.owner_name || "对方";
        if (c.mode === "none") {
          var watchBtn = UI.el("button", "mini-btn", "申请观看 " + who);
          watchBtn.addEventListener("click", function () { requestAccess(c, "watch"); });
          ctxChoices.append(watchBtn);
          if (res.can_operate) {
            var operBtn = UI.el("button", "mini-btn", "申请接续 " + who);
            operBtn.addEventListener("click", function () { requestAccess(c, "operate"); });
            ctxChoices.append(operBtn);
          }
          return;
        }
        var label = who + "（" + (c.mode === "operate" ? "可接续" : "观看") + "）";
        var pickBtn = UI.el("button", "mini-btn", label);
        pickBtn.addEventListener("click", function () {
          curCtx = { ctxID: c.id, ownerName: c.owner_name, mode: c.mode, role: roleOfMode(c.mode) };
          connect(c.id);
          renderChoices(res, c.id);
        });
        ctxChoices.append(pickBtn);
      });
    }

    function requestAccess(target, kind) {
      UI.api.ctxRequest(target.id, kind).then(function () {
        if (disposed) return;
        showBanner("已向 " + (target.owner_name || "对方") + " 发出" +
          (kind === "operate" ? "接续" : "观看") + "申请，等待对方同意。", "ok", []);
      }).catch(function (e) {
        if (!disposed) showBanner("申请失败: " + e.message, "bad", []);
      });
    }

    /* 把本终端的行列数同步给远端 PTY（只有实时角色能改尺寸）。
       必须做：远端 ConPTY 按自己记录的行数清屏，两边不一致时 cls 只清掉
       它已知的那几行，浏览器比远端高就会留下「清不掉的下半屏」。 */
    function sendSize() {
      if (!realtime()) return;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    }

    term.onResize(sendSize);

    term.onData(function (data) {
      /* 不持有输入锁时不发送；服务端还会再拦一道 */
      if (!UI.session.state.canOperate || mode !== "operator") return;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "input", data: UI.b64encode(data) }));
      }
    });

    function connect(ctxID) {
      if (ws) {
        ws.onclose = null;
        ws.close();
        ws = null;
      }
      hideBanner();
      term.reset();
      mode = "observer";
      role = curCtx ? curCtx.role : "watch";
      setStatus("连接中…", null);
      renderCtxBar();
      publishCtx();

      var url = "ws://" + location.host + "/api/term?bot=" + encodeURIComponent(currentBotID) +
        "&ctx=" + encodeURIComponent(ctxID);
      var sock = new WebSocket(url);
      ws = sock;

      sock.onopen = function () {
        if (disposed || ws !== sock) return;
        setStatus("已接入 " + currentBotID, "ok");
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
            role = msg.role || role;
            mode = msg.mode || "observer";
            if (curCtx) {
              curCtx.role = role;
              curCtx.mode = role === "owner" ? "owner" : (role === "operate" ? "operate" : "watch");
              if (msg.owner_name) curCtx.ownerName = msg.owner_name;
              curCtx.inputName = msg.holder || curCtx.inputName;
            }
            publishCtx();
            renderCtxBar();
            setStatus(currentBotID + " · " + roleText(role, curCtx && curCtx.ownerName),
              mode === "operator" ? "ok" : null);
            sendSize(); // 实时角色接入时把尺寸同步过去，远端才会按本终端的行数清屏
            if (mode === "operator") term.focus();
            break;
          case "opened":
            showBanner(msg.text || "终端已就绪", "ok", []);
            break;
          case "failed":
            showBanner("上下文开启失败: " + (msg.text || "未知原因"), "bad", []);
            break;
          case "granted":
            mode = "operator";
            if (role === "watch" && UI.session.state.canOperate) role = "operate";
            if (curCtx) curCtx.role = role;
            publishCtx();
            renderCtxBar();
            setStatus(currentBotID + " · " + roleText(role, curCtx && curCtx.ownerName), "ok");
            hideBanner();
            showBanner(msg.text || "你已获得输入权", "ok", []);
            sendSize();
            term.focus();
            break;
          case "revoked":
            mode = "observer";
            if (curCtx) curCtx.inputName = "";
            publishCtx();
            renderCtxBar();
            setStatus(currentBotID + " · " + roleText(role, curCtx && curCtx.ownerName), null);
            hideBanner();
            showBanner(msg.text || "输入权已被收回", "bad", []);
            break;
          case "bye": {
            var why = msg.text || "连接已结束";
            term.write("\r\n\x1b[31m[" + why + "]\x1b[0m\r\n");
            mode = "observer";
            setStatus(why, "bad");
            hideBanner();
            break;
          }
          case "error":
            term.write("\r\n\x1b[31m[" + msg.text + "]\x1b[0m\r\n");
            setStatus(msg.text, "bad");
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
        var row = UI.el("div", "row" + (b.id === currentBotID ? " active" : ""));
        row.append(UI.el("span", "dot" + (b.buffered ? " warn" : "")));
        var main = UI.el("div", "row-main");
        main.append(
          UI.el("div", "row-title", b.name || b.id),
          UI.el("div", "row-sub", b.id + " · " + (b.os || "未知系统")),
          UI.el("div", "row-sub occupancy", (b.ctx_count || 0) + " 条操作上下文")
        );
        row.append(main);
        row.addEventListener("click", function () { select(b.id); });
        sideBody.append(row);
      });
    }

    function select(botID) {
      currentBotID = botID;
      renderRows();
      loadContexts(botID);
    }

    async function loadContexts(botID) {
      setStatus("加载上下文中…", null);
      var res;
      try {
        res = await UI.api.ctxList(botID);
      } catch (e) {
        if (!disposed) setStatus("无法获取上下文: " + e.message, "bad");
        return;
      }
      if (disposed || currentBotID !== botID) return;

      var pick = null;
      if (res.can_operate) {
        /* 有操作能力：优先用自己的上下文，没有就新建（幂等） */
        pick = (res.contexts || []).filter(function (c) { return c.id === res.mine; })[0] || null;
        if (!pick) {
          try {
            var created = await UI.api.ctxOpen(botID);
            pick = { id: created.id, owner_name: created.owner_name, mode: "owner" };
          } catch (e) {
            if (!disposed) setStatus("打开上下文失败: " + e.message, "bad");
          }
        }
      } else {
        /* 观察者：只能接入已授权/默认开放的上下文 */
        pick = (res.contexts || []).filter(function (c) { return c.mode !== "none"; })[0] || null;
      }
      if (disposed || currentBotID !== botID) return;

      if (!pick) {
        curCtx = null;
        if (ws) {
          ws.onclose = null;
          ws.close();
          ws = null;
        }
        UI.ctx.set(null);
        term.reset();
        term.write("\x1b[90m暂无可观看的操作上下文。若想观看他人操作，可在下方发起申请。\x1b[0m\r\n");
        setStatus("暂无可观看的上下文", null);
        renderCtxBar();
        renderChoices(res, null);
        return;
      }

      curCtx = {
        ctxID: pick.id,
        ownerName: pick.owner_name,
        mode: pick.mode || "owner",
        role: roleOfMode(pick.mode || "owner"),
      };
      connect(pick.id);
      renderChoices(res, pick.id);
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
      /* 首次进入自动接入第一台在线机器 */
      if (!currentBotID && bots.length > 0) {
        select(bots[0].id);
      }
    }

    function onWindowResize() { fitNow(); }
    window.addEventListener("resize", onWindowResize);

    refresh();
    var timer = setInterval(function () { if (!disposed) refresh(); }, POLL_MS);

    if (window.UIState && window.UIState.bot) {
      select(window.UIState.bot);
    }

    return function dispose() {
      disposed = true;
      clearInterval(timer);
      hideBanner();
      window.removeEventListener("resize", onWindowResize);
      if (resizeObs) resizeObs.disconnect();
      if (ws) {
        ws.onclose = null;
        ws.close();
      }
      UI.ctx.set(null);
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
