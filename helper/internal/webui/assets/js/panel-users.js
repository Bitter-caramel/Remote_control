/* 用户管理（仅管理员）：左列账号列表，主区对选中账号做角色调整、
   停用启用、重置口令、删除，并支持新增账号。

   口令一律由服务端加盐哈希保存，前端只在「创建」和「重置」的当次看到明文，
   刷新之后再也取不回来。 */
(function () {
  "use strict";

  var UI = window.UI;

  function fmtTime(s) {
    if (!s) return "从未登录";
    var d = new Date(s);
    if (isNaN(d.getTime()) || d.getFullYear() < 2000) return "从未登录";
    return d.toLocaleString();
  }

  function mount(ctx) {
    var sideHead = ctx.sideHead, sideBody = ctx.sideBody, stage = ctx.stage;

    var users = [];
    var current = null;   // 当前选中的账号
    var creating = false; // 是否处于「新增账号」表单
    var pendingSecret = ""; // 待展示一次的新口令
    var disposed = false;

    var titleEl = UI.el("span", "title", "用户管理");
    var statusEl = UI.el("span", "status", "");
    var head = UI.el("div", "stage-head");
    head.append(titleEl, UI.el("span", "grow"), statusEl);
    var body = UI.el("div", "stage-body form-body");
    stage.append(head, body);

    function setStatus(text, bad) {
      statusEl.textContent = text || "";
      statusEl.className = "status" + (bad ? " bad" : "");
    }

    /* ---------- 左列 ---------- */

    var addBtn = UI.el("button", "icon-btn", "新增");
    addBtn.addEventListener("click", function () {
      creating = true;
      current = null;
      renderRows();
      renderForm();
    });
    var refreshBtn = UI.el("button", "icon-btn", "刷新");
    refreshBtn.addEventListener("click", function () { load(); });
    sideHead.append(UI.el("span", "title", "用户"), UI.el("span", "grow"), addBtn, refreshBtn);

    function renderRows() {
      sideBody.textContent = "";
      if (creating) {
        var row = UI.el("div", "row active");
        var m = UI.el("div", "row-main");
        m.append(UI.el("div", "row-title", "新账号"));
        m.append(UI.el("div", "row-sub", "填写账号信息并创建"));
        row.append(m);
        sideBody.append(row);
      }
      if (!creating && users.length === 0) {
        sideBody.append(UI.el("div", "empty", "暂无账号"));
      }
      users.forEach(function (u) {
        var active = !creating && current && u.id === current.id;
        var r = UI.el("div", "row" + (active ? " active" : ""));
        r.append(UI.el("span", "dot" + (u.disabled ? " warn" : "")));
        var main = UI.el("div", "row-main");
        main.append(UI.el("div", "row-title", u.display_name || u.username));
        main.append(UI.el("div", "row-sub",
          u.username + " · " + u.role_name + (u.disabled ? " · 已停用" : "")));
        r.append(main);
        r.addEventListener("click", function () {
          creating = false;
          current = u;
          renderRows();
          renderForm();
        });
        sideBody.append(r);
      });
    }

    /* ---------- 主区 ---------- */

    function renderForm() {
      body.textContent = "";
      if (pendingSecret) {
        var notice = UI.el("div", "result");
        notice.append(UI.el("div", "result-title", "新口令（只显示这一次，请立即抄录）："));
        notice.append(UI.el("div", "secret", pendingSecret));
        body.append(notice);
        pendingSecret = "";
      }
      if (creating) {
        titleEl.textContent = "新增账号";
        body.append(createForm());
        return;
      }
      if (!current) {
        titleEl.textContent = "用户管理";
        body.append(UI.el("div", "placeholder", "在左侧选择一个账号，或点「新增」创建账号"));
        return;
      }
      titleEl.textContent = "用户：" + current.username;
      body.append(editForm(current));
    }

    function createForm() {
      var box = UI.el("div", "form-col");
      var name = UI.el("input", "input");
      name.placeholder = "登录用的账号名";
      var display = UI.el("input", "input");
      display.placeholder = "显示名（留空则与账号名相同）";
      var role = UI.roleSelect(1); // 默认最小权限
      var pass = UI.el("input", "input");
      pass.type = "password";
      pass.placeholder = "留空则由服务端生成随机口令";

      var sec = UI.section("新增账号");
      sec.append(UI.field("账号名", name), UI.field("显示名", display),
        UI.field("角色", role), UI.field("口令", pass));
      sec.append(UI.el("div", "tip",
        "口令以 PBKDF2 加盐哈希保存，服务端不存明文；留空时生成的口令只在创建成功后显示一次。"));

      var btn = UI.el("button", "primary-btn", "创建账号");
      var err = UI.el("div", "tip bad");
      btn.addEventListener("click", async function () {
        if (!name.value.trim()) {
          err.textContent = "请填写账号名";
          return;
        }
        btn.disabled = true;
        err.textContent = "";
        try {
          var out = await UI.api.createUser({
            username: name.value.trim(),
            display_name: display.value.trim(),
            password: pass.value,
            role: Number(role.value),
          });
          pendingSecret = (out && out.password) || "";
          setStatus("已创建 " + out.user.username);
          await load(out.user.id);
        } catch (e) {
          err.textContent = "创建失败: " + e.message;
        } finally {
          btn.disabled = false;
        }
      });
      sec.append(btn, err);
      box.append(sec);
      return box;
    }

    function editForm(u) {
      var box = UI.el("div", "form-col");

      async function act(btn, fn, okMsg) {
        btn.disabled = true;
        try {
          await fn();
          setStatus(okMsg);
          await load(u.id);
        } catch (e) {
          setStatus(e.message, true);
        } finally {
          btn.disabled = false;
        }
      }

      var info = UI.section("账号信息");
      info.append(UI.el("div", "kv", "账号：" + u.username));
      info.append(UI.el("div", "kv", "最近登录：" + fmtTime(u.last_login_at)));
      info.append(UI.el("div", "kv", "创建时间：" + fmtTime(u.created_at)));
      box.append(info);

      var sec = UI.section("角色（决定能用哪些功能）");
      var role = UI.roleSelect(u.role);
      var roleBtn = UI.el("button", "primary-btn", "应用角色");
      roleBtn.addEventListener("click", function () {
        var next = Number(role.value);
        act(roleBtn, function () { return UI.api.setUserRole(u.id, next); },
          "已把 " + u.username + " 设为" + UI.session.roleName(next));
      });
      var roleRow = UI.el("div", "inline");
      roleRow.append(role, roleBtn);
      sec.append(roleRow);
      sec.append(UI.el("div", "tip",
        "观察者：只能看终端实时画面与程序日志；普通用户：可操作机器；" +
        "管理员：另有本页与机器管理。改角色会让该账号已登录的会话立即失效。"));
      box.append(sec);

      var ops = UI.section("其它操作");
      var disBtn = UI.el("button", "icon-btn", u.disabled ? "启用账号" : "停用账号");
      disBtn.addEventListener("click", function () {
        act(disBtn, function () { return UI.api.setUserDisabled(u.id, !u.disabled); },
          (u.disabled ? "已启用 " : "已停用 ") + u.username);
      });

      var pwBtn = UI.el("button", "icon-btn", "重置口令");
      pwBtn.addEventListener("click", async function () {
        if (!window.confirm("重置 " + u.username + " 的口令？该账号已登录的会话会立即失效。")) return;
        pwBtn.disabled = true;
        try {
          var out = await UI.api.setUserPassword(u.id, "");
          pendingSecret = (out && out.password) || "";
          setStatus("已重置 " + u.username + " 的口令");
          await load(u.id);
        } catch (e) {
          setStatus(e.message, true);
        } finally {
          pwBtn.disabled = false;
        }
      });

      var delBtn = UI.el("button", "icon-btn danger", "删除账号");
      delBtn.addEventListener("click", function () {
        if (!window.confirm("删除账号 " + u.username + "？该操作不可恢复。")) return;
        current = null;
        act(delBtn, function () { return UI.api.deleteUser(u.id); }, "已删除 " + u.username);
      });

      var opsRow = UI.el("div", "inline");
      opsRow.append(disBtn, pwBtn, delBtn);
      ops.append(opsRow);
      ops.append(UI.el("div", "tip", "为保证系统始终有人可管，最后一个可用管理员不能被降级、停用或删除。"));
      box.append(ops);

      return box;
    }

    /* ---------- 数据 ---------- */

    async function load(selectID) {
      try {
        var list = await UI.api.users();
        if (disposed) return;
        users = list || [];
        if (selectID) {
          creating = false;
          current = users.filter(function (x) { return x.id === selectID; })[0] || null;
        } else if (current) {
          current = users.filter(function (x) { return x.id === current.id; })[0] || null;
        } else if (!creating && users.length > 0) {
          current = users[0];
        }
        renderRows();
        renderForm();
        setStatus("共 " + users.length + " 个账号");
      } catch (e) {
        if (disposed) return;
        setStatus("读取失败: " + e.message, true);
      }
    }

    load();

    return function dispose() {
      disposed = true;
    };
  }

  UI.registerPanel({
    id: "users",
    name: "用户",
    icon: "☺",
    order: 80,
    minRole: 3,
    mount: mount,
  });
})();
