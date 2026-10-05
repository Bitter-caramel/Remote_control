/* 共享的「当前操作上下文」状态。
   终端面板接入某条上下文时调用 UI.ctx.set(...)，底部面板等功能据此知道自己
   正在为哪台机器、哪条上下文服务；没有接入时 set(null)。

   之所以单独抽出来：终端面板过去把选中的机器存在自己的局部变量里，
   dock 完全不知道当前在操控谁，这是架构缺口。 */
(function () {
  "use strict";

  var UI = window.UI;

  var state = { current: null }; // current = { botID, ctxID, mode, role, ownerName }
  var listeners = [];

  function emit() {
    listeners.slice().forEach(function (cb) {
      try {
        cb(state.current);
      } catch (e) {
        /* 单个订阅者出错不影响其它 */
      }
    });
  }

  function set(c) {
    state.current = c || null;
    // 同步到 window.UIState，兼容 ?bot= 跳转与其它面板对当前 bot 的读取
    window.UIState = window.UIState || {};
    window.UIState.bot = state.current ? state.current.botID : "";
    window.UIState.ctx = state.current;
    emit();
  }

  function get() { return state.current; }

  /* 订阅上下文变化；返回取消订阅函数 */
  function onChange(cb) {
    if (typeof cb !== "function") return function () {};
    listeners.push(cb);
    return function () {
      var i = listeners.indexOf(cb);
      if (i >= 0) listeners.splice(i, 1);
    };
  }

  UI.ctx = { get: get, set: set, onChange: onChange };
})();
