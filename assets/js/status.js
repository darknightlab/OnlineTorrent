import * as tools from "./tool-kawaii.js";

const serverURL = tools.serverURL;

document.getElementById("serverSelect").onchange = function () {
    let serverSelectInit = document.getElementById("serverSelectInit");
    if (serverSelectInit != null) {
        this.removeChild(serverSelectInit);
    }
    let sURL = this.value;
    printStatus(sURL);
};

initPage();

function initPage() {
    tools.loadMarkdown();

    let serverSelect = document.getElementById("serverSelect");
    serverURL.forEach(function (url) {
        let option = document.createElement("option");
        option.value = url;
        option.innerHTML = url;
        serverSelect.appendChild(option);
    });
}

function printStatus(serverURI) {
    $("#torrent-list").empty();
    $.ajax({
        url: serverURI + "api/status",
        type: "GET",
        success: function (result) {
            if (result.Torrents == null) {
                tools.log("服务器上没有种子: " + serverURI);
                $("#torrent-area").addClass("mdui-hidden");
            } else {
                result.Torrents.forEach(function (torr) {
                    $("#torrent-area").removeClass("mdui-hidden");
                    addTorrentToList(torr);
                });
            }
        },
        error: function (e) {
            tools.log("获取不到服务器信息: " + serverURI);
        },
    });
}

function addTorrentToList(torr) {
    let li = $(`<li class="mdui-list-item mdui-ripple">${torr.Name}</li>`);
    li.click(function () {
        $("#torrent-list")
            .children()
            .each(function () {
                $(this).removeClass("mdui-list-item-active");
            });
        li.addClass("mdui-list-item-active");
        $("#showinfo")
            .unbind("click")
            .click(function () {
                $("#download-info").text(tools.filesize(torr.ConnStatus.BytesRead));
                $("#upload-info").text(tools.filesize(torr.ConnStatus.BytesWritten));
                $("#peer-info").text(`${torr.TotalPeers} | ${torr.PendingPeers} | ${torr.ActivePeers}`);
                $("#open-drawer")[0].click();
                $("#torrent-info")[0].click();
            });
        $("#opentorrent")
            .unbind("click")
            .click(function () {
                let urlsuffix = "/?";
                urlsuffix += `uri=${encodeURIComponent(torr.Magnet)}`;
                // let res = window.location.origin + urlsuffix;
                window.open(urlsuffix);
            });
        $("#deletetorrent")
            .unbind("click")
            .click(function () {
                let sURL = document.getElementById("serverSelect").value;
                tools.deleteTorrent(
                    torr.Hash,
                    sURL,
                    function (result) {
                        if (result.response == 200) {
                            tools.log("请求删除成功: " + sURL);
                        } else {
                            tools.log("请求删除失败[200]: " + sURL);
                        }
                    },
                    function (e) {
                        tools.log("请求删除失败: " + sURL);
                    }
                );
                printStatus(sURL);
            });
    });
    $("#torrent-list").append(li);
}
