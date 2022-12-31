## 下面给出了一些支持的流媒体格式和对应浏览器

### 20220929: 喜报, chrome添加了对hevc的支持, 可以直接观看hevc视频. 等待Webtorrent项目更新文档

来源: [Webtorrent](https://github.com/webtorrent/webtorrent/blob/master/docs/api.md)

| Containers | Chromium | Mobile Chromium | Edge Chromium | Firefox |
| ---------- | :------: | :-------------: | :-----------: | :-----: |
| 3g2        |    ✓     |        ✓        |       ✓       |    ✓    |
| 3gp        |    ✓     |        ✓        |       ✓       |    ✘    |
| avi        |    ✘     |        ✘        |       ✘       |    ✘    |
| m2ts       |    ✘     |        ✘        |     ✓\*\*     |    ✘    |
| m4v etc.   |   ✓\*    |       ✓\*       |      ✓\*      |   ✓\*   |
| mp4        |    ✓     |        ✓        |       ✓       |    ✓    |
| mpeg       |    ✘     |        ✘        |       ✘       |    ✘    |
| mov        |    ✓     |        ✓        |       ✓       |    ✓    |
| ogm ogv    |    ✓     |        ✓        |       ✓       |    ✓    |
| webm       |    ✓     |        ✓        |       ✓       |    ✓    |
| mkv        |    ✓     |        ✓        |       ✓       |    ✘    |

\* Container might be supported, but the container's codecs might not be.  
\*\* Documented as working, but can't reproduce.

| Video Codecs | Chromium | Mobile Chromium | Edge Chromium | Firefox |
| ------------ | :------: | :-------------: | :-----------: | :-----: |
| AV1          |    ✓     |        ✓        |       ✓       |    ✓    |
| H.263        |    ✘     |        ✘        |       ✘       |    ✘    |
| H.264        |    ✓     |        ✓        |       ✓       |    ✓    |
| H.265        |    ✓     |        ✘        |      ✓\*      |    ✘    |
| MPEG-2/4     |    ✘     |        ✘        |       ✘       |    ✘    |
| Theora       |    ✓     |        ✘        |       ✓       |    ✓    |
| VP8/9        |    ✓     |        ✓        |       ✓       |    ✓    |

\* Requires MSStore extension which you can get by opening this link `ms-windows-store://pdp/?ProductId=9n4wgh0z6vhq` while using Edge.

| Audio Codecs | Chromium | Mobile Chromium | Edge Chromium | Firefox |
| ------------ | :------: | :-------------: | :-----------: | :-----: |
| AAC          |    ✓     |        ✓        |       ✓       |    ✓    |
| AC3          |    ✘     |        ✘        |       ✓       |    ✘    |
| DTS          |    ✘     |        ✘        |       ✘       |    ✘    |
| EAC3         |    ✘     |        ✘        |       ✓       |    ✘    |
| FLAC         |    ✓     |       ✓\*       |       ✓       |    ✓    |
| MP3          |    ✓     |        ✓        |       ✓       |    ✓    |
| Opus         |    ✓     |        ✓        |       ✓       |    ✓    |
| TrueHD       |    ✘     |        ✘        |       ✘       |    ✘    |
| Vorbis       |    ✓     |        ✓        |       ✓       |   ✓\*   |
