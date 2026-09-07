# STRM + NFO Docker 镜像

镜像由当前工作区的 Navidrome 源码构建，加入 STRM、目标元数据提取和 NFO 支持。
本次已验证的 `linux/amd64` 镜像标签为
`navidrome:0.63.2-liristy`（镜像仓库名已简化为 `navidrome`）。此前导出包中的旧标签仍为
`navidrome-strm-nfo:0.63.2-liristy`，导入后可按下方命令添加新名称。导出文件的 SHA256 见同目录
`navidrome-strm-nfo-linux-amd64.tar.sha256`。

关于页面显示的服务端版本为
`0.63.2-liristy (afb3a2f8)`。按要求简化为官方版本基线加 `-liristy`；括号保留上游提交，
用于定位源码。注意：当前工作区来自该版本之后的 master 提交 `afb3a2f8`，并非未经修改的
官方 v0.63.2 release。版本链接指向官方 v0.63.2 发布页，不会生成不存在的自定义 release 链接。

容器断网自检已覆盖：启动与内置 UI、中文/韩文路径、目标 FLAC 标签/歌词/技术参数提取、
安全 NFO 生成、数据库入库、Redia 所需原生 API 字段、真实路径响应、原始音频逐字节比对及
FFmpeg MP3 转码。

## 导入

将 `binaries/navidrome-strm-nfo-linux-amd64.tar` 及其 `.sha256` 上传到 NAS：

```sh
sha256sum -c navidrome-strm-nfo-linux-amd64.tar.sha256
docker load -i navidrome-strm-nfo-linux-amd64.tar
docker tag navidrome-strm-nfo:0.63.2-liristy navidrome:0.63.2-liristy
```

还必须将现有 Compose 中 navidrome 服务的 `image:` 改为
`navidrome:0.63.2-liristy`，再在 Compose 文件所在目录执行：

```sh
docker-compose up -d --force-recreate navidrome
docker-compose exec navidrome /app/navidrome --version
```

已安装 Compose V2 的主机可把 `docker-compose` 换成 `docker compose`。
不要替换已有端口、挂载、网络和 Redia 配置。仅 `docker load` 不会更新正在运行的容器。

镜像没有自动推送到公共仓库。替换旧容器前先备份 `/data`，不要让不同版本同时使用同一
数据库；首次建议使用独立测试数据目录。

### 旧 Redia 镜像数据库兼容

参考镜像可能把数据库推进到私有版本 `20251019000000`，但没有执行后来加入上游的
`20250823142158_make_playqueue_position_int.sql`。普通新版 Navidrome 会因此报告
`found 1 missing migrations` 并退出。本镜像只在错误精确匹配这一个已知缺口时，自动补执行
该迁移并继续升级；发现任何其他缺失迁移仍会拒绝启动，避免掩盖数据库损坏。

已使用旧镜像产生的真实数据库副本验证：完整性检查升级前后均为 `ok`，986 首曲目、1 个
用户、21 个歌单保持不变，`playqueue.position` 从 `REAL` 转换为 `INTEGER`；旧库已有的
`is_strm`、`strm_target` 及示例歌曲真实路径保持不变，并成功启动到当前数据库版本
`20260905132000`。首次升级耗时取决于曲库大小，期间不要强制终止容器。

## Compose 示例

```yaml
services:
  navidrome:
    image: navidrome:0.63.2-liristy
    restart: unless-stopped
    ports:
      - "4533:4533"
    environment:
      ND_STRM_LOCALROOTS: "/CloudNAS/CloudDrive/115/音乐库"
      ND_STRM_FORCEREPORTREALPATH: "true"
      ND_STRM_METADATA_PROBELOCALTARGETS: "true"
      ND_STRM_METADATA_PROBECONCURRENCY: "2"
      ND_SCANNER_SIDECAR_ENABLED: "true"
      ND_SCANNER_SIDECAR_FORMAT: "nfo"
      ND_SCANNER_SIDECAR_READONLY: "true"
      ND_SCANNER_SIDECAR_GENERATEONSTARTUP: "true"
      ND_SCANNER_SIDECAR_TRUST: "true"
      ND_SCANNER_SIDECAR_DELETEONPURGE: "true"
    volumes:
      - /你的Navidrome数据目录:/data
      - /你的STRM文件目录:/music
      - /你的CloudDrive音乐库挂载目录:/CloudNAS/CloudDrive/115/音乐库:ro
```

`/music` 不能挂成 `:ro`，否则只能读取现有 NFO，无法生成缺失文件；CloudDrive 音频目录
应保持只读。两项容器内路径必须与 STRM 内容一致。无需 `privileged: true`，也无需以
root 用户运行 Navidrome，但所配置的 UID/GID 必须能写 `/data` 和 `/music`。

启动后执行全量扫描。`ND_STRM_FORCEREPORTREALPATH=true` 专用于 Redia 一类反代：它不依赖
已有播放器的“报告真实路径”设置，只对 `.strm` 返回白名单内的本地目标。HTTP URL 和白名单
外路径始终不会暴露。普通部署可不启用；`ND_SUBSONIC_DEFAULTREPORTREALPATH` 仅影响新播放器，
不能修复数据库里已有播放器均为关闭状态的问题。

目标探测会真实打开 CloudDrive 文件并可能触发云端流量；建议从并发 1–2 开始。若只想
读取已有 NFO，可关闭 `ND_STRM_METADATA_PROBELOCALTARGETS` 和
`ND_SCANNER_SIDECAR_GENERATEONSTARTUP`，保留 Sidecar Enabled。

`redia3` 起修复了目标探测曾错误依赖“启用 NFO 且缺少 NFO”的问题。现在只要
`ND_STRM_METADATA_PROBELOCALTARGETS=true`，无论是否启用 Sidecar、是否已有 NFO，扫描都会
像旧参考镜像一样读取白名单内真实音频的标签、大小、时长、码率、采样率、位深和声道数。

`liristy` 修正了先前对所有 STRM 一律回退的实现：现在正常签发并校验新版转码令牌。
仅在 `ND_STRM_FORCEREPORTREALPATH=true`、本地目标在白名单内、GET/HEAD 且决策为原码播放时，
`getTranscodeStream` 才以同域 `307` 转到 `stream`，让请求重新经过代理入口。
跳转保留部署子路径并使用兼容性更好的绝对路径；Range 请求继续支持 206。需要 MP3 等转码时保留协商的格式、
码率、采样率等参数；HTTP STRM、关闭兼容模式和 POST 请求不强制跳转。
无效或过期令牌返回 410，升级后若客户端缓存旧令牌，请刷新页面或重新播放。

同时修复：未知音频大小不再用 STRM 文本长度写入新 NFO；HTTP STRM 的 POST 播放会以 GET
读取上游，避免上游拒绝 POST；新版流接口不会在正常 HEAD/304/416 空响应后追加错误内容。
已有 NFO 不会被覆盖，旧文件中的错误大小需按实际元数据修正。

## 与旧参考镜像的差异

- 已直接拆解并隔离运行 `dajingzhongshan/navidrome:latest`。该镜像基于旧的
  `v0.52.0-strm...c039402`，本实现对齐其数据库列 `is_strm`、`strm_target`，以及原生
  `/api/song/{id}` 返回的 `isStrm`、`strmTarget`、`originalPath`；本镜像保持这套接口兼容，
  而不是猜测 Navidrome 的 `/rest/stream` 应直接返回 302；
- 基于当前 Navidrome 工作区源码，不把 STRM 功能绑死在长期停更的旧版本上；
- 兼容旧 `musicfile 1.0` NFO 和 `-cover.jpg`，新文件使用可版本化的 1.1 格式；
- 目标探测显式白名单、默认关闭且限并发，HTTP 目标绝不探测；
- 生成 NFO 不写真实目标路径或签名 URL，不覆盖用户文件；清理时只删自身生成文件；
- 封面复用 Navidrome 当前提取和缓存链路，不强制为每首歌再复制一份图片；
- 不要求 root/privileged，仍保留官方容器的权限模型和新功能更新路径。

## Redia 302 边界

客户端必须连接 Redia 的 Navidrome 代理入口。若云盘助手名为 `115`，映射通常是
`/CloudNAS/CloudDrive/115/ => 115/`。Redia 中配置的用户名和密码必须是实际存在的
Navidrome 账号；它会用该账号查询歌曲，反代页面能打开并不代表内部查询认证成功。镜像已
覆盖参考镜像的数据库与原生 API 契约、真实路径报告及挂载回源能力。参考镜像自身的
`/rest/stream` 也是 HTTP 200 音频，不是 302；302 由 Redia 在代理入口根据上述字段生成。
直连 Navidrome 不会返回 115 的 302。

本地自检证明的是 Navidrome 的接口、跳转及音频输出；没有连接用户生产 Redia/115 完成
端到端验证。单独看到 206 不能证明云盘直链成功，还应查看完整重定向链和最终请求域名。

## 重新构建

在 Docker Engine + Buildx 可用的 Linux/WSL 环境，于仓库根目录执行：

```sh
sed 's/\r$//' release/build-strm-image.sh | bash
```

具备 ARM64 原生节点或已安装 QEMU/binfmt 的 Buildx builder 时，可设置
`PLATFORM=linux/arm64` 构建 ARM64；本次主机没有 ARM 执行模拟器，因此交付包仅包含已经
完整运行自检的 AMD64 版本。脚本会打包当前工作区（含未提交修改），在临时目录处理
Windows 换行，运行断网自检，再导出 tar 和 SHA256；不会提交或推送代码。
