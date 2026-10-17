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

## 仓库
- `darknightlab/OnlineTorrent`：`main` = 新版单仓库；`legacy` = 旧前端 main；`beta` 保留原样。
