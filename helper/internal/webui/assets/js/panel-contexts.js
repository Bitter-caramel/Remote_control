/* 底部面板 · 上下文与授权（跨机器功能，global=true，不随当前上下文切换重挂）。
   - 我的操作上下文（各自所属 bot、接入人数、当前输入持有者）
   - 待处理申请（同意 / 拒绝）
   - 我已发出的授权（收回）
   - 用户级「默认可看」开关 */
(function () {
  "use strict";

  var UI = window.UI;

  function modeLabel(mode) {
    return mode === "operate" ? "接续操作" : "观看";
  }

  function mount(ctx) {
    var body = ctx.body;

    var head = UI.el("div", "dock-head");
    var refreshBtn = UI.el("button", "icon-btn", "刷新");
    head.append(UI.el("span", "title", "上下文与授权"), UI.el("span", "grow"), refreshBtn);

    var prefsBox = UI.el("label", "ctx-prefs");
    var prefsChk = document.createElement("input");
    prefsChk.type = "checkbox";
    prefsBox.append(prefsChk,
      UI.el("span", null, "默认可看：开启后我名下所有上下文对所有人开放只读"));

    var reqSection = UI.section("待处理申请");
    var reqBox = UI.el("div", "resv-list");
    reqSection.append(reqBox);

    var grantSection = UI.section("我已发出的授权");
    var grantBox = UI.el("div", "resv-list");
    grantSection.append(grantBox);

    var mineSection = UI.section("我的操作上下文");
    var mineBox = UI.el("div", "resv-list");
    mineSection.append(mineBox);

    var tip = UI.el("div", "dock-empty", "加载中…");
    body.append(head, prefsBox, reqSection, grantSection, mineSection, tip);

    var disposed = false;

    function item(title, sub, btnLabel, btnCls, onClick) {
      var row = UI.el("div", "resv-item");
      var main = UI.el("div", "resv-main");
      main.append(UI.el("div", "resv-title", title));
      if (sub) main.append(UI.el("div", "resv-sub", sub));
      row.append(main);
      if (btnLabel) {
        var btn = UI.el("button", btnCls || "icon-btn", btnLabel);
        btn.addEventListener("click", function () { onClick(btn); });
        row.append(btn);
      }
      return row;
    }

    function render(reqs, grants, mine) {
      reqBox.textContent = "";
      if (reqs.length === 0) {
        reqBox.append(UI.el("div", "dock-empty", "没有待处理的申请"));
      } else {
        reqs.forEach(function (r) {
          var row = UI.el("div", "resv-item");
          var main = UI.el("div", "resv-main");
          main.append(
            UI.el("div", "resv-title", r.requester + " 申请" + modeLabel(r.mode)),
            UI.el("div", "resv-sub", r.bot_id + " · " + r.context_id)
          );
          row.append(main);
          var ok = UI.el("button", "icon-btn", "同意");
          ok.addEventListener("click", function () { answer(r.id, true, ok); });
          var no = UI.el("button", "icon-btn danger", "拒绝");
          no.addEventListener("click", function () { answer(r.id, false, no); });
          row.append(ok, no);
          reqBox.append(row);
        });
      }

      grantBox.textContent = "";
      if (grants.length === 0) {
        grantBox.append(UI.el("div", "dock-empty", "还没有发出任何授权"));
      } else {
        grants.forEach(function (g) {
          grantBox.append(item(
            g.grantee + " · " + modeLabel(g.mode),
            g.bot_id + " · " + g.context_id,
            "收回", "icon-btn danger",
            function (btn) { revoke(g.id, btn); }
          ));
        });
      }

      mineBox.textContent = "";
      if (mine.length === 0) {
        mineBox.append(UI.el("div", "dock-empty", "当前没有你的操作上下文（在线机器上）"));
      } else {
        mine.forEach(function (m) {
          var c = m.c;
          var who = c.input_name ? " · 输入权在 " + c.input_name : "";
          mineBox.append(item(
            (m.bot.name || m.bot.id) + "（" + m.bot.id + "）",
            "接入 " + (c.subs || 0) + " 人" + who + " · " + c.id,
            null, null, null
          ));
        });
      }

      var empty = reqs.length === 0 && grants.length === 0 && mine.length === 0;
      tip.textContent = empty ? "" : "";
      tip.hidden = true;
    }

    async function load() {
      tip.hidden = false;
      tip.textContent = "加载中…";
      var me, reqs, grants, bots, mine;
      try {
        me = await UI.api.me();
        reqs = (await UI.api.ctxRequests()) || [];
        grants = (await UI.api.ctxGrants()) || [];
        bots = (await UI.api.bots()) || [];
      } catch (e) {
        if (disposed) return;
        tip.textContent = "加载失败: " + e.message;
        return;
      }
      if (disposed) return;
      prefsChk.checked = !!me.watch_default;

      mine = [];
      for (var i = 0; i < bots.length; i++) {
        var b = bots[i];
        try {
          var res = await UI.api.ctxList(b.id);
          (res.contexts || []).forEach(function (c) {
            if (c.id === res.mine) mine.push({ bot: b, c: c });
          });
        } catch (e) {
          /* 单台机器失败不影响整体 */
        }
      }
      if (disposed) return;
      render(reqs, grants, mine);
    }

    async function answer(id, accept, btn) {
      btn.disabled = true;
      try {
        await UI.api.ctxAnswer(id, accept);
      } catch (e) {
        if (!disposed) tip.textContent = "处理失败: " + e.message;
        btn.disabled = false;
        return;
      }
      if (!disposed) load();
    }

    async function revoke(id, btn) {
      btn.disabled = true;
      try {
        await UI.api.ctxRevoke(id);
      } catch (e) {
        if (!disposed) tip.textContent = "收回失败: " + e.message;
        btn.disabled = false;
        return;
      }
      if (!disposed) load();
    }

    prefsChk.addEventListener("change", function () {
      var next = prefsChk.checked;
      prefsChk.disabled = true;
      UI.api.setWatchDefault(next).then(function () {
        if (!disposed) prefsChk.disabled = false;
      }).catch(function (e) {
        if (disposed) return;
        prefsChk.checked = !next;
        prefsChk.disabled = false;
        tip.hidden = false;
        tip.textContent = "保存设置失败: " + e.message;
      });
    });

    refreshBtn.addEventListener("click", function () { load(); });
    load();

    return function dispose() { disposed = true; };
  }

  UI.dock.register({
    id: "contexts",
    name: "上下文与授权",
    order: 10,
    global: true,
    mount: mount,
  });
})();
