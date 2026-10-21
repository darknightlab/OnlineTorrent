# 部署环境记录

## 服务器 131.143.240.2（主机名 docker，LAN 192.168.233.8）
- Podman + podman-compose；Portainer EE（`/root/app/portainer`，endpoint `local` id=2）管理 stack，stack 文件在 `/root/app/portainer/portainer_data/compose/<id>/`。
- 约定：数据目录 `/root/app/<app>`；端口绑 `192.168.233.8:<port>` 或 host 网络；对外经 cloudflared（AMS 隧道，远程管理 ingress）。
- 已有：`onlinetorrent-tracker` stack（tracker-ws `192.168.233.8:23332` = wss://tracker.projectk.org；aquatic-udp）。
- 磁盘剩 ~4.6G，内存 8G。
- 新服务端口：`192.168.233.8:23380`（otd），Portainer stack `onlinetorrent`（id 26），镜像 `docker.io/darknightlab/onlinetorrent:main`。

## Cloudflare
- 账户 `Project Kawaii` 8f11cddcc0b974f55349b65f5d4c2fdf，zone projectk.org 80f8bb16b106e853584adba3b40afc0f。
- 本项目临时 API Token：`pi-agent-onlinetorrent (delete after project)`，id `439dc8d1244aef8cd7ff106dc234a6f5`，存于 `~/.config/onlinetorrent/cloudflare.env`（权限：Tunnel/Pages/Workers 读写，zone DNS/Zone/Zone Settings/SSL）。**项目结束删除。**
- AMS 隧道 `37d916b5-13b3-4048-abcd-29ddb43d04a4`（本服务器 cloudflared）。
- 现状（2026-10-18 起）：`www.projectk.org`、`projectk.org` → AMS 隧道 → otd（`192.168.233.8:23380`），裸域由 otd 301 到 www。旧 Pages 项目 `onlinetorrent` 已解绑域名并停止自动部署（保留未删）。旧后端 `kawaiiapi.projectk.org` 已下线：mci 隧道路由和 DNS 已删除，MCI 主机（23.175.184.30）上的容器 `onlinetorrent` 已停止并设为不自动重启（未删除）。备份在 `~/.config/onlinetorrent/backup/`。
- 上线步骤：AMS 隧道 ingress 加 `www.projectk.org → http://192.168.233.8:23380` → 从 Pages 项目移除 www 自定义域 → www CNAME 改为 `37d916b5-….cfargotunnel.com`（proxied）。apex 保持 Pages 或做 301 到 www。

## 测试 qBittorrent
- https://qbittorrent-ams.kawaii.cam（隧道 → TrueNAS `192.168.233.14:8888`，同 LAN 可直连），v5.3.0beta1 / libtorrent 2.1.1。

## 边缘节点（otd edge，未部署）
qB 所在主机（TrueNAS-AMS，Portainer endpoint 5，docker；LAN 192.168.233.14）上运行，读 qB 的下载目录直接向浏览器提供 WebSeed（包括下载中的文件），数据不经主站。WebSeed 只由边缘节点提供：没有边缘节点的 qB 服务器不提供 WebSeed（主站没有 WebSeed 数据接口）。它主动连主站的隧道，主站不需要能访问它。

1. 后台 → qB 服务器 → 该服务器「编辑」：填「边缘节点公开地址」`https://edge-ams.projectk.org`；如需让主站经边缘节点访问 qB WebUI（不再走 `qbittorrent-ams.kawaii.cam`），开「qB API 经边缘节点访问」。
2. 同一行的「边缘节点」按钮 → 生成令牌，记下 `OTD_EDGE_TOKEN` 与 `OTD_MAIN`（只显示一次）。
3. 确认设置 `site.base_url` = `https://www.projectk.org`（边缘节点的 CORS 只允许这个 origin）。
4. 在 TrueNAS 的 Portainer（endpoint 5）新建 stack：

```yaml
services:
  otd-edge:
    image: docker.io/darknightlab/onlinetorrent:main
    command: ["edge"]
    network_mode: host            # qB WebUI 在 127.0.0.1:8888
    user: "3000:3000"             # 与 qB 的 PUID/PGID 相同，读得到 UMASK 002 的文件
    volumes:
      - /mnt/HMSMRData1/Torrent:/Data/Torrent:ro   # 与 qB 容器内路径相同，不用 DATA_MAP
    environment:
      OTD_MAIN: wss://www.projectk.org/api/v1/edge/connect
      OTD_EDGE_TOKEN: otde_…      # 后台生成
      QB_URL: http://127.0.0.1:8888
      QB_USER: …
      QB_PASS: …
      DATA_ROOT: /Data/Torrent
      EDGE_LISTEN: 192.168.233.14:8890   # 只对 LAN 开放，由 cloudflared 访问
      EDGE_TRUST_PROXY: "1"              # 客户端 IP 取 CF-Connecting-IP
    restart: unless-stopped
```

   其他环境变量：`DATA_MAP`（qB 看到的路径与边缘节点不同时，`qB前缀=本地前缀[,…]`）、`EDGE_VERIFY=0`（不复核分块 SHA-1，默认复核）、`EDGE_ALLOW_ORIGIN`（覆盖 CORS origin）。边缘节点只监听 HTTP，TLS 由 Cloudflare 终止。
5. Cloudflare：在 AMS 隧道（`37d916b5-…`，docker 主机上的 cloudflared，与 TrueNAS 同 LAN）的 ingress 加 `edge-ams.projectk.org → http://192.168.233.14:8890`，DNS `edge-ams` CNAME 到 `37d916b5-….cfargotunnel.com`（proxied）。也可以在 TrueNAS 上另跑 cloudflared 连同一隧道或新隧道，ingress 指向 `http://127.0.0.1:8890`（此时 `EDGE_LISTEN` 可用 `127.0.0.1:8890`）。不要让 Cloudflare 缓存（边缘节点返回 `Cache-Control: private, no-store`）。边缘节点直接收浏览器流量，Cloudflare 的条款 / 套餐由站长负责。
6. 检查：边缘节点日志出现 `tunnel connected` 与 `config from main server`；`curl https://edge-ams.projectk.org/healthz` → `ok`；后台 qB 服务器页「边缘节点」列显示已连接、延迟、已提供流量。

`OTD_MAIN` 也可以用 LAN 地址 `ws://192.168.233.8:23380/api/v1/edge/connect`（不经 Cloudflare，少一跳；令牌明文经过 LAN）。隧道每 25 秒心跳，Cloudflare 的 ~100 秒空闲断开不会触发；断开后 1 s 起指数退避（最长 60 s）重连，令牌被拒时 5 分钟后重试。

## 弹幕边车 danmu_api（2026-10-21 已部署）
- huangxd-/danmu_api **未修改**（AGPL-3.0），官方镜像 `docker.io/logvar/danmu-api:1.21.6@sha256:8e1e8f0e3ad578d6c2babaf1434c47e56ffbfe47c16a63108ced9af093b63800`（= commit `5ef63a0`；与该提交的 `danmu_api/` 逐文件 md5 一致），容器 `onlinetorrent-danmu-api`，服务名 `danmu-api`，在 stack 26 的 compose 文件里（`/root/app/portainer/portainer_data/compose/26/v2/docker-compose.yml`，修改前备份 `docker-compose.yml.bak-20261021-danmaku`）。
- 隔离：**不发布端口**，只在 compose 项目网络 `onlinetorrent_default`（podman `isolate=true`，只有 otd 与它）上；其他网络的容器按名字或 IP 都连不上（已测）；主机上没有 9321 / 5321 监听。访问令牌 `TOKEN` 在 `/root/app/onlinetorrent-danmu-api/danmu-api.env`（root 600，经 `env_file` 读入），无令牌 401。
- 配置：`GROUP_MINUTE=0`（不合并去重）、`LIKE_SWITCH=false`（不在文字后加点赞数）、`RATE_LIMIT_MAX_REQUESTS=0`、`OTHER_SERVER=http://127.0.0.1:9`（不回退第三方弹幕服务器）、`SOURCE_ORDER=bilibili,bahamut`、`PLATFORM_ORDER=bilibili1,bahamut`、`LOG_LEVEL=warn`。otd 只用它的 `GET /api/v2/comment?url=`（Bilibili）与 `POST /api/v2/segmentcomment`（动画疯），不用它的搜索。
- otd 设置：后台 → 设置 → 弹幕 →「danmu_api 地址」= `http://danmu-api:9321/<TOKEN>`（TOKEN 见上面的文件）。留空 = 不采集。
- 部署方式：stack 26 的容器由 podman-compose 创建（标签 `io.podman.compose.*`），Portainer 的「更新 stack」会用 docker compose 重建同名容器而失败（`container name "onlinetorrent" is already in use`，2026-10-21 实测；未改动 otd）。所以改的是 v2 的 compose 文件（Portainer 读同一文件，显示一致），再 `cd …/compose/26/v2 && podman-compose -p onlinetorrent -f docker-compose.yml up -d danmu-api`。
- 升级：改镜像 tag + digest（看 Docker Hub `logvar/danmu-api` 与上游提交对应关系），同上命令只重建 `danmu-api`。

## 仓库
- `darknightlab/OnlineTorrent`：`main` = 新版单仓库；`legacy` = 旧前端 main；`beta` 保留原样。
