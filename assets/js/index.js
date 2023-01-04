const serverURL = ["https://sg1-server.darknight.tech:16101/", "https://cn2-server.darknight.tech:16101/", "https://us1-server.darknight.tech:16101/", "/"];

const loopTime = 2000;

const progressBarSplits = 200;

const defaultEnableWebseed = true;

var printInfo;

$("#uri").keydown(function (e) {
    if (e.keyCode == "13") {
        e.preventDefault();
        let uri = $(this).val();
        sendURIToAll(uri);
    }
});

$("#upload-select").click(function (e) {
    $("#upload-torrent").click();
});

$("#upload-torrent").change(function (e) {
    $("#upload-filename").text($("#upload-torrent")[0].files[0].name);
});

$("#upload-submit").click(function (e) {
    if ($("#upload-torrent")[0].files[0] == undefined) {
        log("还没选择要上传的文件!");
        return;
    }
    uploadTorrentToAll($("#upload-torrent")[0].files[0]);
});

$("#share-button").click(function (e) {
    let urlsuffix = "/?";
    client.torrents.forEach(function (t) {
        urlsuffix += `uri=${uriEncode(t.magnetURI)}&`;
    });
    urlsuffix = urlsuffix.substr(0, urlsuffix.length - 1);
    let res = window.location.origin + urlsuffix;
    $("#share-url").val(res);
});

$("#share-url").click(function (e) {
    $("#share-url").select();
    document.execCommand("copy");
    mdui.snackbar({
        message: "已复制到剪贴板",
        position: "right-bottom",
    });
});

initPage();

function initPage() {
    loadMarkdown();
    loadShareURL();
    setProgressBar();
    loadServiceWorker();
}

function loadServiceWorker() {
    navigator.serviceWorker.register("/sw.min.js", { scope: "/" }).then((reg) => {
        const worker = reg.active || reg.waiting || reg.installing;
        function checkState(worker) {
            return worker.state === "activated" && client.loadWorker(worker);
        }
        if (!checkState(worker)) {
            worker.addEventListener("statechange", ({ target }) => checkState(target));
        }
    });
}

// 检测url并加载链接
function loadShareURL() {
    let mList = getQueryVariable("uri");
    if (mList.length == 0) {
        return;
    }
    mList.forEach(function (strin) {
        sendURIToAll(uriDecode(strin));
    });
}

function onTorrent(torrent) {
    log("已获取种子信息");
    log("种子名: " + torrent.name);
    log(
        "哈希值: " +
            torrent.infoHash +
            " " +
            //  '<a href="' + torrent.magnetURI + '" target="_blank">[磁力链接]</a> ' +
            '<a href="' +
            torrent.torrentFileBlobURL +
            '" target="_blank" download="' +
            torrent.name +
            '.torrent">[下载 .torrent]</a>'
    );

    // 优先下载首尾，获取播放时长
    let priority = 10;
    torrent.files.forEach(function (file) {
        torrent.select(file._endPiece, file._endPiece, priority);
        torrent.select(file._startPiece, file._startPiece, priority);
    });

    function enableWebseed() {
        webseedPrefix.forEach((prefix) => {
            let webseedURL = prefix + encodeURIComponent(torrent.name) + webseedSuffix;
            torrent.addWebSeed(webseedURL);
        });
        log("已启用webseed");
    }

    $("#enable-webseed").unbind("click").click(enableWebseed);
    $("#disable-webseed")
        .unbind("click")
        .click(function (e) {
            for (let i = torrent.wires.length - 1; i >= 0; i--) {
                if (torrent.wires[i].type == "webSeed") {
                    torrent.wires[i].destroy();
                }
            }
            log("已禁用webseed");
        });

    // 自动启用webseed
    if (defaultEnableWebseed) {
        enableWebseed();
    }

    // Render all files into to the page
    torrent.files.forEach(function (file) {
        if (isExt(file.name, videoExt)) {
            let li = addList(file, function () {
                $("#video")[0].oncanplay = function () {
                    $("#video").css("aspect-ratio", "");
                };
                // file.renderTo("#video");
                file.streamTo($("#video")[0]);
                $("#video-area").removeClass("mdui-hidden");
                $("#video-title").text(file.name);
                $(document).attr("title", file.name);
            });
            // 自动播放
            if ($("#video-area").hasClass("mdui-hidden")) {
                // 两秒后点击
                setTimeout(function () {
                    li.click();
                }, 2000);
            }
        } else if (isExt(file.name, imageExt)) {
            addList(file, function () {
                $("#other-file").empty();
                $("#other-area").removeClass("mdui-hidden");
                file.appendTo("#other-file");
            });
        } else if (isExt(file.name, audioExt)) {
            addList(file, function () {
                $("#other-file").empty();
                $("#other-area").removeClass("mdui-hidden");
                file.appendTo("#other-file");
            });
        } else {
            log(`不支持播放${file.name}, 左侧面板点击 √ 可下载`);
        }
    });
}

function setProgressBar() {
    for (let i = 0; i < progressBarSplits; i++) {
        let d = document.createElement("div");
        d.setAttribute("class", "progress-item");
        document.getElementById("progress-bar").appendChild(d);
    }
}

function updateProgressBar(file) {
    let bar = document.getElementById("progress-bar");
    let startPiece = file._startPiece;
    let endPiece = file._endPiece;
    let piecePerSegment = (endPiece + 1 - startPiece) / progressBarSplits;
    let pieceNumber = startPiece;
    for (let i = 0; i < progressBarSplits; i++) {
        let segment = bar.children.item(i);
        if (segment.classList.contains("progress-color-complete")) {
            pieceNumber = Math.ceil(startPiece + piecePerSegment * (i + 1));
            continue;
        }
        let hasNotReady = false;
        let hasReady = false;
        while (pieceNumber < startPiece + piecePerSegment * (i + 1)) {
            if (file._torrent.pieces[pieceNumber] == null) {
                // 说明已经下载完
                hasReady = true;
            } else {
                hasNotReady = true;
            }
            if (hasReady && hasNotReady) {
                break;
            }
            pieceNumber++;
        }
        if (hasReady && hasNotReady) {
            // 下到一半
            segment.setAttribute("class", "progress-item progress-color-half");
            pieceNumber = Math.ceil(startPiece + piecePerSegment * (i + 1));
        } else if (hasReady) {
            // 下完了
            segment.setAttribute("class", "progress-item progress-color-complete");
        }
    }
}

function resetProgressBar() {
    let bar = document.getElementById("progress-bar");
    for (let i = 0; i < bar.children.length; i++) {
        bar.children.item(i).setAttribute("class", "progress-item");
    }
}

function addList(file, clickfunc) {
    $("#file-list-area").removeClass("mdui-hidden");
    let li = $(`<li class="mdui-list-item mdui-ripple">` + file.name + `</li>`);
    li.click(function () {
        clearInterval(printInfo);
        resetProgressBar();
        printInfo = setInterval(function () {
            $("#download-info").text(getDownloadInfo(file));
            $("#upload-info").text(getUploadInfo(file));
            $("#peer-info").text(getPeerInfo(file));
            $("#progress-info").text(getProgressInfo(file));
            updateProgressBar(file);
        }, loopTime);
        $("#delete-torrent")
            .unbind("click")
            .click(function () {
                deleteTorrentInAll(getFileTorrentHash(file));
            });
        $("#peer-info")
            .parent()
            .unbind("click")
            .click(function () {
                let peerip = $("#peer-ip");
                peerip.empty();
                file._torrent.wires.forEach(function (wire) {
                    if (wire.type == "webrtc") {
                        let li = $(`<li class="mdui-menu-item">
                            <a class="mdui-ripple" style="overflow-x: auto">${wire.remoteAddress}</a>
                        </li>`);
                        peerip.append(li);
                    }
                });
                file._torrent.urlList.forEach(function (peerurl) {
                    let li = $(`<li class="mdui-menu-item">
                        <a class="mdui-ripple" style="overflow-x: auto">${peerurl}</a>
                    </li>`);
                    peerip.append(li);
                });
            });
        file.getStreamURL(function (err, url) {
            if (err) return log(err.message);
            $("#download-file").attr({ href: url });
        });
        clickfunc();
    });
    $("#file-list").append(li);
    return li;
}

function uploadTorrentToAll(file) {
    log("开始上传文件");
    let okflag = 0;
    for (let i = 0; i < serverURL.length; i++) {
        uploadTorrent(
            file,
            serverURL[i],
            function (result) {
                if (result.response == 200) {
                    if (okflag == 0) {
                        okflag = 1;
                        log("torrent 上传成功");
                        startMagnet(result.magnet, onTorrent);
                    }
                } else {
                    log("torrent 上传失败[200]: " + serverURL[i]);
                }
            },
            function (e) {
                if (okflag == 0) {
                    log("torrent 上传失败: " + serverURL[i]);
                }
            }
        );
    }
}

function sendURIToAll(uri) {
    log("开始发送请求");
    let okflag = 0;
    for (let i = 0; i < serverURL.length; i++) {
        sendURI(
            uri,
            serverURL[i],
            function (result) {
                if (result.response == 200) {
                    if (okflag == 0) {
                        okflag = 1;
                        log("请求发送成功");
                        log("已添加种子，磁力链接为: " + '<a href="' + result.magnet + '" target="_blank">[磁力链接]</a> ');
                        startMagnet(result.magnet, onTorrent);
                    }
                    webseedPrefix.push(serverURL[i] + "webseed/");
                } else {
                    log("请求发送失败[200]: " + serverURL[i]);
                }
            },
            function (e) {
                if (okflag == 0) {
                    log("请求发送失败: " + serverURL[i]);
                }
            }
        );
    }
    setTimeout(() => {
        if (okflag == 0) {
            startMagnet(uri, onTorrent);
        }
    }, 5000);
}

function deleteTorrentInAll(hash) {
    log("请求删除种子");
    for (let i = 0; i < serverURL.length; i++) {
        deleteTorrent(
            hash,
            serverURL[i],
            function (result) {
                if (result.response == 200) {
                    log("请求删除成功: " + serverURL[i]);
                } else {
                    log("请求删除失败[200]: " + serverURL[i]);
                }
            },
            function (e) {
                log("请求删除失败: " + serverURL[i]);
            }
        );
    }
}
