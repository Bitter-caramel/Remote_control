/* 命令历史回放（仅管理员）：设置各操作上下文终端输出的保留策略。

   回放 = 每条上下文按「段」（一次提示符到下一次提示符）持久化的原始输出，
   用于重连后自动恢复上次画面。存储不能无限增长，因此这里二选一：
     - 按条数：每条上下文最多留 N 段；
     - 按天数：只留最近 N 天。
   保存后服务端立即按新策略清理多余数据，且不可恢复。 */
(function () {
  "use strict";

  var UI = window.UI;

  /* 字节数转可读文本 */
  function fmtBytes(n) {
    if (!n) return "0 B";
    var units = ["B", "KB", "MB", "GB"];
    var i = 0;
    while (n >= 1024 && i < units.length - 1) {
      n = n / 1024;
      i++;
    }
    return (i === 0 ? n : n.toFixed(1)) + " " + units[i];
  }

  function mount(ctx) {
    var sideHead = ctx.sideHead, sideBody = ctx.sideBody, stage = ctx.stage;

    var view = null; // 最近一次拿到的策略 + 现状
    var disposed = false;

    var titleEl = UI.el("span", "title", "命令历史回放");
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
    sideHead.append(UI.el("span", "title", "保留策略"), UI.el("span", "grow"), refreshBtn);

    sideBody.textContent = "";

    /* ---------- 主区：策略表单 ---------- */

    function renderForm() {
      body.textContent = "";
      if (!view) {
        body.append(UI.el("div", "placeholder", "读取中…"));
        return;
      }

      var box = UI.el("div", "form-col");

      var sec = UI.section("保留策略（二选一）");
      var countInput = UI.el("input", "input");
      countInput.type = "number";
      countInput.min = "1";
      countInput.max = String(view.max_count_limit);
      countInput.value = String(view.max_count);
      var daysInput = UI.el("input", "input");
      daysInput.type = "number";
      daysInput.min = "1";
      daysInput.max = String(view.max_days_limit);
      daysInput.value = String(view.max_days);

      var countRow = modeRow("count", "按条数保留，每条上下文最多", "段", countInput);
      var daysRow = modeRow("days", "按天数保留，只保留最近", "天", daysInput);
      sec.append(countRow.box, daysRow.box);
      sec.append(UI.el("div", "tip",
        "条数上限 " + view.max_count_limit + "，天数上限 " + view.max_days_limit + "。"));

      function syncRows() {
        var byCount = countRow.radio.checked;
        countInput.disabled = !byCount;
        daysInput.disabled = byCount;
        countRow.box.className = "mode-row" + (byCount ? "" : " off");
        daysRow.box.className = "mode-row" + (byCount ? " off" : "");
      }
      countRow.radio.checked = view.mode !== "days";
      daysRow.radio.checked = view.mode === "days";
      countRow.radio.addEventListener("change", syncRows);
      daysRow.radio.addEventListener("change", syncRows);
      syncRows();

      var saveBtn = UI.el("button", "primary-btn", "保存并立即清理");
      var saveRow = UI.el("div", "inline");
      saveRow.append(saveBtn);
      sec.append(saveRow);
      sec.append(UI.el("div", "tip",
        "保存后服务端会立即按新策略删除多余回放，删除的数据无法恢复。"));
      box.append(sec);

      saveBtn.addEventListener("click", function () {
        var mode = daysRow.radio.checked ? "days" : "count";
        var cfg = {
          mode: mode,
          max_count: Math.floor(Number(countInput.value) || 0),
          max_days: Math.floor(Number(daysInput.value) || 0),
        };
        saveBtn.disabled = true;
        UI.api.setReplaySettings(cfg).then(function (res) {
          if (disposed) return;
          view = res;
          renderForm();
          renderSide();
          setStatus("已保存，多余回放已清理");
        }).catch(function (e) {
          if (disposed) return;
          setStatus("保存失败: " + e.message, true);
          saveBtn.disabled = false;
        });
      });

      box.append(statsSection(view));
      body.append(box);
    }

    /* 一行保留模式：单选 + 说明 + 数值输入 + 单位 */
    function modeRow(mode, text, unit, input) {
      var box = UI.el("div", "mode-row");
      var radio = document.createElement("input");
      radio.type = "radio";
      radio.name = "replay-mode";
      radio.value = mode;
      box.append(radio, UI.el("span", "mode-name", text), input, UI.el("span", "mode-unit", unit));
      return { box: box, radio: radio, input: input };
    }

    function statsSection(v) {
      var sec = UI.section("回放数据现状");
      sec.append(UI.el("div", "kv", "已存段落：" + v.stats.segments + " 段"));
      sec.append(UI.el("div", "kv", "占用空间：" + fmtBytes(v.stats.bytes)));
      sec.append(UI.el("div", "kv", "涉及上下文：" + v.stats.contexts + " 条"));
      return sec;
    }

    /* ---------- 左列：当前策略摘要 ---------- */

    function renderSide() {
      sideBody.textContent = "";
      if (!view) return;
      if (view.mode === "days") {
        sideBody.append(UI.el("div", "row-title", "按天数保留"));
        sideBody.append(UI.el("div", "row-sub", "只保留最近 " + view.max_days + " 天"));
      } else {
        sideBody.append(UI.el("div", "row-title", "按条数保留"));
        sideBody.append(UI.el("div", "row-sub", "每条上下文最多 " + view.max_count + " 段"));
      }
      sideBody.append(UI.el("div", "row-sub",
        "现存 " + view.stats.segments + " 段 · " + fmtBytes(view.stats.bytes)));
    }

    async function load() {
      try {
        var res = await UI.api.replaySettings();
        if (disposed) return;
        view = res;
        renderSide();
        renderForm();
        setStatus("");
      } catch (e) {
        if (disposed) return;
        setStatus("读取失败: " + e.message, true);
      }
    }

    load();

    return function dispose() { disposed = true; };
  }

  UI.registerPanel({
    id: "replay",
    name: "命令历史回放",
    icon: "⟲",
    order: 50,
    minRole: 3,
    mount: mount,
  });
})();
