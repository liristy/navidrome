# STRM 与 NFO 支持

本分支在当前 Navidrome 源码上增加 `.strm` 音轨支持。STRM 可以指向 HTTP/HTTPS
音频，或 `STRM.LocalRoots` 白名单中的绝对本地路径，适用于 Redia + CloudDrive2
挂载方案。数据库新增与旧 Redia 镜像同名的 `is_strm` 和 `strm_target` 字段；迁移会先检查
字段是否已经存在，因此旧镜像数据库可保留原有目标并平滑升级。

每个 STRM 只允许一个目标，文件不超过 64 KiB；支持 UTF-8 BOM、空行、`#` 注释和
大小写不敏感的扩展名。例如：

```text
/CloudNAS/CloudDrive/115/音乐库/2PM/Tik Tok/Tik Tok - 2PM、윤은혜.flac
```

也可用 EXTINF 显式指定标题和时长：

```text
#EXTINF:249.4,Tik Tok
/CloudNAS/CloudDrive/115/音乐库/2PM/Tik Tok/Tik Tok - 2PM、윤은혜.flac
```

## 从目标音频生成 NFO

已有本地 STRM/NFO/封面的网盘用户，请优先使用[本地监听、缺失才刮削](strm-watch.md)。
完全不需自动生成 NFO 时，可选择[严格本地模式](strm-local-only.md)。

安全默认值是不打开 STRM 目标，也不生成文件。显式开启后，扫描器会使用 Navidrome
现有音频解析器读取白名单内的 FLAC/MP3 等真实文件，将以下信息写入同名 NFO 并立即
导入数据库：

- 标题、艺术家、专辑艺术家、专辑、日期/年份、轨号、碟号、流派和注释；
- 时长、码率、位深、采样率、声道和编码；
- 歌词、MusicBrainz/ISRC 标识和已知角色的参与者；
- 文件大小、修改时间、真实音频后缀及是否含内嵌封面。

生成格式为 `<musicfile version="1.1">`，并兼容读取用户提供的 1.0 格式，以及
`track`、`song`、`musictrack` 根节点。默认文件名是 `歌曲名.nfo`；若旁边已有同名
真实音频，为避免抢占它的 NFO，会使用 `歌曲名.strm.nfo`。扫描器也能读取这两种命名。

```toml
[STRM]
LocalRoots = ["/CloudNAS/CloudDrive/115/音乐库"]
ForceReportRealPath = true

[STRM.Metadata]
ProbeLocalTargets = true
ProbeConcurrency = 2

[Scanner.Sidecar]
Enabled = true
Format = "nfo"
ReadOnly = true
GenerateOnStartup = true
Trust = true
DeleteOnPurge = true
```

`ReadOnly=true` 仍允许创建缺失 NFO，但绝不覆盖已有 NFO。无效 NFO 会被保留并跳过；
`DeleteOnPurge` 只删除带有本实现生成标记的 NFO，不删除用户或旧镜像生成的文件。
生成内容不会保存 `<targetpath>`、HTTP URL 或 CloudDrive 绝对路径，以免泄露挂载结构与
签名地址。NFO 最大 2 MiB，DTD/实体和过深 XML 会被拒绝。

读取 CloudDrive/FUSE 目标可能触发远端 I/O，因此目标探测默认关闭，并由
`ProbeConcurrency` 限流。探测失败不会让 STRM 消失：会退回 EXTINF 和保守的目录/文件名
推断。HTTP(S) 目标始终不会被扫描器探测，避免 SSRF、签名 URL 外泄和无界下载。

旧方案生成的 `歌曲名-cover.jpg` 等同名前缀图片会被优先识别。没有旁挂图片时，若目标是
白名单内的本地音频且显式启用 `ProbeEmbeddedCover`，Navidrome 才允许提取其内嵌封面并使用自身图片缓存，不必再复制
一份 JPG 到 STRM 目录。

## 播放、下载与转码

- 本地目标会经过白名单、符号链接边界和普通文件检查；支持原样播放、Range、下载、归档
  和现有 FFmpeg 转码。
- HTTP 原样播放由 Navidrome 代理，转发 Range、If-Range、条件请求和 HEAD；不会把签名
  URL 作为重定向暴露给客户端。远端网络 I/O 超时为 15 秒。
- STRM 每次新建流时重新读取，更新过期 URL 后无需重启；重新扫描才会刷新库元数据。
- 不支持相对路径、`file://`、UNC/SMB、FTP、HLS/M3U、多目标、自定义 Cookie/Referer、
  自动续签和 Jukebox STRM。

只有可信管理员才能写音乐库。HTTP 请求从服务器发出，可能访问内网，应按需要配置容器
出站网络策略。不要把 `/` 加入 `LocalRoots`。

## Redia + CloudDrive2

音乐库挂载的是包含 STRM/NFO 的目录；CloudDrive2 真实音频还必须以 STRM 中完全相同的
容器路径挂入，例如 `/CloudNAS/CloudDrive/115/音乐库`。生成 NFO 时 `/music` 必须可写，
CloudDrive2 挂载可以保持只读。

在 Navidrome 的播放器设置中启用“报告真实路径”后，Subsonic `getSong` 等响应的 `path`
会返回白名单内的本地 STRM 目标。Redia 反代建议使用专用显式开关，不依赖数据库中已有
播放器的设置：

```toml
[STRM]
ForceReportRealPath = true
```

该开关只影响 `.strm`，且仅报告 `LocalRoots` 内的本地目标；HTTP URL、越界路径和普通歌曲
不会被强制报告。下面的旧选项只影响新播放器：

```toml
[Subsonic]
DefaultReportRealPath = true
```

HTTP URL 永远不会通过该字段报告。若 Redia 云盘助手名为 `115`，典型路径映射为：

```text
/CloudNAS/CloudDrive/115 => 115
```

经直接拆解和隔离运行参考镜像确认，Redia 还会读取 Navidrome 原生 API。对本地白名单内的
STRM，本分支的 `/api/song/{id}` 会兼容返回：

```json
{
  "isStrm": true,
  "strmTarget": "/CloudNAS/CloudDrive/115/音乐库/…/歌曲.flac",
  "originalPath": "/CloudNAS/CloudDrive/115/音乐库/…/歌曲.flac"
}
```

数据库读取时会再次按当前 `LocalRoots` 校验目标；旧库中越界或 HTTP 目标不会通过这些字段
暴露。参考镜像的 `/rest/stream` 本身返回 200 音频，最终 302 是 Redia 代理根据原生 API
信息生成的。

客户端需要连接 Redia 配置的 Navidrome 代理入口，Redia 才能拦截并返回云盘 302。本分支
提供 STRM 入库、真实路径报告，以及挂载回源播放/转码，但不调用 Redia 私有接口，也不
负责生成 115 签名链接。真实环境仍需核对 Redia 版本、代理接口和最终 302 响应。
Redia 媒体服务器配置中的用户名/密码必须对应实际 Navidrome 用户；能反代显示登录页不能
证明 Redia 自己用于 `getSong` 查询的账号已经通过认证。

参考：[Redia 路径映射](https://www.symedia.top/archive/redia/Redia部署教程.html)；
[参考镜像教程](https://www.symedia.top/archive/redia/Navidrome302教程.html)。

旧教程中的扁平环境变量（例如 `ND_SCANNER_ENABLESIDECAR`）仍作为弃用别名兼容；新部署
建议使用文档中的嵌套变量名。完整 Docker 示例见 [docker-strm.md](docker-strm.md)。
