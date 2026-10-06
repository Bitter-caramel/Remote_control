/* 底部面板 · 控制台（每用户每 bot 一个单一控制台，global=false，随当前上下文切换重挂）。
   - 这里只搭「每 bot 一个控制台」的骨架：工具通过 UI.console.registerTool 注册进来，
     控制台按当前机器类型（os）决定显示哪些，再把每个工具装进一个带标题的小节。
   - 面向机器的便捷功能（投放文件、拍摄桌面等）后续逐个注册进来；「按机器类型定制」
     目前只预留 applies(os) 钩子，具体定制逻辑后续再单独写。 */
(function () {
  "use strict";

  var UI = window.UI;
  var tools = [];

  /* 注册一个控制台工具。约定字段：
       id         唯一标识
       name       展示标题
       order      排序权重，越小越靠前
       applies(os) 可选，按当前机器系统（os）决定是否显示；
                   不提供则认为所有机器都适用（机器类型定制的挂载点）
       mount(section, ctx) 挂载到自己的小节里，返回可选 dispose 函数；
                   ctx = { botID, botName, os, ctxID, mode, role, ownerName } */
  function registerTool(tool) {
    if (!tool || !tool.id || typeof tool.mount !== "function") {
      throw new Error("UI.console.registerTool: 必须包含 id 与 mount");
    }
    if (tools.some(function (t) { return t.id === tool.id; })) {
      throw new Error("UI.console.registerTool: 工具 id 重复: " + tool.id);
    }
    tools.push(tool);
  }

  function applicableTools(os) {
    return tools.slice().sort(function (a, b) {
      return (a.order || 100) - (b.order || 100);
    }).filter(function (t) {
      return typeof t.applies !== "function" || t.applies(os);
    });
  }

  function mount(dockCtx) {
    var body = dockCtx.body;
    var cur = dockCtx.ctx; // { botID, botName, os, ctxID, mode, role, ownerName }

    var head = UI.el("div", "dock-head");
    head.append(UI.el("span", "title", "控制台"));
    body.append(head);

    var os = cur ? cur.os : "";
    var list = applicableTools(os);
    if (list.length === 0) {
      body.append(UI.el("div", "dock-empty", "暂无针对该机器类型的功能"));
      return function dispose() { /* 无工具，无清理 */ };
    }

    var disposers = [];
    list.forEach(function (tool) {
      var sec = UI.section(tool.name);
      body.append(sec);
      try {
        var d = tool.mount(sec, cur) || null;
        if (d) disposers.push(d);
      } catch (e) {
        /* 单个工具挂载失败不影响其它工具 */
      }
    });

    return function dispose() {
      disposers.forEach(function (d) {
        try { d(); } catch (e) { /* 忽略清理异常 */ }
      });
    };
  }

  UI.dock.register({
    id: "console",
    name: "控制台",
    order: 20,
    global: false,
    mount: mount,
  });

  UI.console = { registerTool: registerTool };
})();