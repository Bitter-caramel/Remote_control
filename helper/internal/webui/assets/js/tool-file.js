/* 控制台「文件传输」工具：投放文件到被控端 / 从被控端下载文件。
   通过 UI.console.registerTool 挂进每用户每 bot 的控制台（panel-console.js）。
   权限由后端把关：观察者（只读）会被 403 拒绝。 */
(function () {
  "use strict";
  var UI = window.UI;

  function mount(sec, cur) {
    var botID = cur.botID;

    // ---------- 投放 ----------
    var putBlock = UI.el("div", null);
    var fileInput = UI.el("input", "input");
    fileInput.type = "file";
    var pathInput = UI.el("input", "input");
    pathInput.placeholder = "目标路径（留空则落到 received/ 目录）";
    var putBtn = UI.el("button", "primary-btn", "投放文件");
    var putStatus = UI.el("div", "tip");

    putBlock.append(fileInput, pathInput, putBtn, putStatus);

    // ---------- 下载 ----------
    var getBlock = UI.el("div", null);
    var getInput = UI.el("input", "input");
    getInput.placeholder = "被控端文件路径";
    var getBtn = UI.el("button", "primary-btn", "下载文件");
    var getStatus = UI.el("div", "tip");

    getBlock.append(getInput, getBtn, getStatus);

    sec.append(
      UI.el("div", "tip", "投放：把本机文件发送到被控端。"),
      putBlock,
      UI.el("hr", null),
      UI.el("div", "tip", "下载：输入被控端文件路径后拉回本机。"),
      getBlock
    );

    putBtn.addEventListener("click", function () {
      var f = fileInput.files && fileInput.files[0];
      if (!f) {
        putStatus.textContent = "请先选择文件";
        return;
      }
      var path = pathInput.value.trim();
      var url = "/api/bots/" + encodeURIComponent(botID) + "/file?name=" +
        encodeURIComponent(f.name) +
        (path ? "&path=" + encodeURIComponent(path) : "");

      putBtn.disabled = true;
      putStatus.textContent = "上传中 0%";
      var xhr = new XMLHttpRequest();
      xhr.open("POST", url);
      xhr.upload.onprogress = function (e) {
        if (e.lengthComputable) {
          putStatus.textContent = "上传中 " + Math.round((e.loaded / e.total) * 100) + "%";
        }
      };
      xhr.onload = function () {
        putBtn.disabled = false;
        if (xhr.status === 401) {
          UI.session.expired();
          return;
        }
        var data = null;
        try { data = JSON.parse(xhr.responseText); } catch (e) { /* ignore */ }
        if (xhr.status >= 200 && xhr.status < 300) {
          putStatus.textContent = "投放完成 → " + (data && data.path ? data.path : "");
        } else {
          putStatus.textContent = "投放失败：" + ((data && data.error) || ("HTTP " + xhr.status));
        }
      };
      xhr.onerror = function () {
        putBtn.disabled = false;
        putStatus.textContent = "投放失败：网络错误";
      };
      xhr.send(f);
    });

    getBtn.addEventListener("click", function () {
      var path = getInput.value.trim();
      if (!path) {
        getStatus.textContent = "请输入被控端文件路径";
        return;
      }
      var url = "/api/bots/" + encodeURIComponent(botID) + "/file?path=" + encodeURIComponent(path);
      var a = document.createElement("a");
      a.href = url;
      a.download = "";
      document.body.appendChild(a);
      a.click();
      a.remove();
      getStatus.textContent = "已发起下载（若未弹出请检查浏览器设置）";
    });

    return function dispose() {
      putBtn.disabled = false;
      fileInput.value = "";
      pathInput.value = "";
      getInput.value = "";
    };
  }

  UI.console.registerTool({
    id: "file",
    name: "文件传输",
    order: 10,
    applies: function () {
      return true; // 投放/下载与机器类型无关
    },
    mount: mount,
  });
})();