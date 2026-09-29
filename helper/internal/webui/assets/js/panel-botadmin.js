/* 机器管理（仅管理员）：给每台机器设定最低可见角色与备注。

   等级的含义（数值越大越严）：
     1 观察者   观察者及以上可见（最宽松）
     2 普通用户 普通用户及以上可见（新机器默认）
     3 管理员   只有管理员可见
   能看 = 当前角色 >= 该等级；能操作还要额外满足「普通用户及以上」。
   调高等级会立即断开已经打开的低权限终端连接。 */
(function () {
  "use strict";

  var UI = window.UI;

  function fmtTime(s) {
    if (!s) return "—";
    var d = new Date(s);
    if (isNaN(d.getTime()) || d.getFullYear() < 2000) return "—";
    return d.toLocaleString();
  }

  function mount(ctx) {
    var sideHead = ctx.sideHead, sideBody = ctx.sideBody, stage = ctx.stage;

    var bots = [];
    var current = null;
    var disposed = false;

    var titleEl = UI.el("span", "title", "机器管理");
    var statusEl = UI.el("span", "status", "");
    var head = UI.el("div", "stage-head");
    head.append(titleEl, UI.el("span", "grow"), statusEl);
    var body = UI.el("div", "stage-body form-body");
    stage.append(head, body);

    function setStatus(text, bad) {
      statusEl.textContent = text || "";
      statusEl.className = "status" + (bad ? " bad" : "");
    }

    var refreshBtn = UI.el("button", "icon-btn", "刷新");
    refreshBtn.addEventListener("click", function () { load(); });
    sideHead.append(UI.el("span", "title", "机器"), UI.el("span", "grow"), refreshBtn);

    function renderRows() {
      sideBody.textContent = "";
      if (bots.length === 0) {
        sideBody.append(UI.el("div", "empty", "还没有任何机器连接过"));
        return;
      }
      bots.forEach(function (b) {
        var row = UI.el("div", "row" + (current && b.id === current.id ? " active" : ""));
        row.append(UI.el("span", "dot" + (b.online ? (b.buffered ? " warn" : "") : " off")));
        var main = UI.el("div", "row-main");
        main.append(UI.el("div", "row-title", b.name || b.id));
        main.append(UI.el("div", "row-sub",
          b.id + " · " + UI.session.roleName(b.min_role) + "及以上"));
        row.append(main);
        row.addEventListener("click", function () {
          current = b;
          renderRows();
          renderForm();
        });
        sideBody.append(row);
      });
    }

    function renderForm() {
      body.textContent = "";
      if (!current) {
        titleEl.textContent = "机器管理";
        body.append(UI.el("div", "placeholder", "在左侧选择一台机器"));
        return;
      }
      titleEl.textContent = "机器：" + (current.name || current.id);
      body.append(detail(current));
    }

    function detail(b) {
      var box = UI.el("div", "form-col");

      async function act(btn, fn, okMsg) {
        btn.disabled = true;
        try {
          await fn();
          setStatus(okMsg);
          await load(b.id);
        } catch (e) {
          setStatus(e.message, true);
        } finally {
          btn.disabled = false;
        }
      }

      var info = UI.section("机器信息");
      info.append(UI.el("div", "kv", "botID：" + b.id));
      info.append(UI.el("div", "kv", "系统：" + (b.os || "未知")));
      info.append(UI.el("div", "kv",
        "状态：" + (b.online ? (b.buffered ? "疑似掉线（缓存区）" : "在线 " + b.addr) : "离线")));
      info.append(UI.el("div", "kv", "最近在线：" + fmtTime(b.last_seen)));
      info.append(UI.el("div", "kv", "首次连接：" + fmtTime(b.first_seen)));
      box.append(info);

      var lvl = UI.section("可见等级");
      var sel = UI.roleSelect(b.min_role);
      var lvlRow = UI.el("div", "inline");
      var lvlBtn = UI.el("button", "primary-btn", "应用等级");
      lvlBtn.addEventListener("click", function () {
        var next = Number(sel.value);
        act(lvlBtn, function () { return UI.api.setBotMinRole(b.id, next); },
          "已把 " + b.id + " 设为" + UI.session.roleName(next) + "及以上可见");
      });
      lvlRow.append(sel, lvlBtn);
      lvl.append(lvlRow);
      lvl.append(UI.el("div", "tip",
        "新机器默认「普通用户」，即观察者看不到；想让观察者监看某台机器，" +
        "把这里改成「观察者」。改成「管理员」则其他人完全看不到。"));
      box.append(lvl);

      var noteSec = UI.section("备注");
      var note = UI.el("input", "input");
      note.value = b.note || "";
      note.placeholder = "例如：财务部办公机";
      var noteBtn = UI.el("button", "primary-btn", "保存备注");
      noteBtn.addEventListener("click", function () {
        act(noteBtn, function () { return UI.api.setBotNote(b.id, note.value); }, "备注已保存");
      });
      var noteRow = UI.el("div", "inline");
      noteRow.append(note, noteBtn);
      noteSec.append(noteRow);
      box.append(noteSec);

      return box;
    }

    async function load(selectID) {
      try {
        var list = await UI.api.botsAll();
        if (disposed) return;
        bots = list || [];
        if (selectID) {
          current = bots.filter(function (x) { return x.id === selectID; })[0] || null;
        } else if (current) {
          current = bots.filter(function (x) { return x.id === current.id; })[0] || null;
        } else if (bots.length > 0) {
          current = bots[0];
        }
        renderRows();
        renderForm();
        setStatus("共 " + bots.length + " 台机器，在线 " +
          bots.filter(function (x) { return x.online; }).length + " 台");
      } catch (e) {
        if (disposed) return;
        setStatus("读取失败: " + e.message, true);
      }
    }

    /* 不做轮询：主区有输入框，自动重绘会打断正在填写的备注，刷新交给按钮 */
    load();

    return function dispose() {
      disposed = true;
    };
  }

  UI.registerPanel({
    id: "botadmin",
    name: "机器",
    icon: "▣",
    order: 90,
    minRole: 3,
    mount: mount,
  });
})();
