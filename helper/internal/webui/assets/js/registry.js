/* 前端面板注册表。
   外壳（shell.js）只认识注册表，不认识任何具体功能；
   功能模块（panel-*.js）各自 registerPanel 注册自己，新增功能不必改动外壳。 */
(function () {
  "use strict";

  var panels = [];

  /* 注册一个功能面板。
     约定字段：
       id       唯一标识（也用于 URL hash）
       name     左侧导航栏显示的名称
       icon     导航栏图标字符
       order    排序权重，越小越靠前
       minRole  可见所需的最低角色（1 观察者 / 2 普通用户 / 3 管理员），
                不填表示登录即可见；管理员专属面板填 3
       mount({ sideHead, sideBody, stage })  在激活时调用，返回可选的 dispose 函数 */
  function registerPanel(panel) {
    if (!panel || !panel.id || typeof panel.mount !== "function") {
      throw new Error("registerPanel: 面板必须包含 id 与 mount");
    }
    if (panels.some(function (p) { return p.id === panel.id; })) {
      throw new Error("registerPanel: 面板 id 重复: " + panel.id);
    }
    panels.push(panel);
  }

  function allPanels() {
    return panels.slice().sort(function (a, b) {
      return (a.order || 100) - (b.order || 100);
    });
  }

  window.UI = window.UI || {};
  window.UI.registerPanel = registerPanel;
  window.UI.allPanels = allPanels;
})();
