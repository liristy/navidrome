# 2026-09-07 STRM 兼容修复版

本地镜像：`navidrome:0.63.2-liristy-fix1`，平台 `linux/amd64`。
应用版本仍为 `0.63.2-liristy`。2026-09-07 按用户要求发布到 Docker Hub：
`liristy/navidrome:0.63.2-liristy-fix1` 和 `liristy/navidrome:latest`。
两个标签的发布摘要均为 `sha256:ae3ba31fc5ae8d3df8c2c2f542a9dd8b9ca64f6b5ad52bb14a9f6989540a6667`。
此文优先于此前检查记录中的新版转码兼容说明。

## 播放回归

恢复用户确认正常的 redia5 关键接口行为：STRM 不下发 transcodeParams，客户端回退到经典 stream；
缓存的 getTranscodeStream GET/HEAD 请求直接同域 307 到 stream，不再受新增的
ForceReportRealPath、缓存目标字段、转码决策及旧令牌有效期条件约束。
保留 format、maxBitRate 和 offset 映射，不额外强制 format=raw。
正常 Subsonic 登录认证、歌曲访问范围与本地文件白名单检查仍有效。
POST 请求保持原生处理，避免把请求正文中的认证信息错误放进跳转链。
STRM 的新版格式协商回到 redia5 行为，需要转码时使用经典 stream 的格式/码率参数。

## 专辑元数据与扫描收尾

- 标签合并先统一大小写，避免大写音频标签与小写 NFO 冲突导致后续随机选择。
- 新 NFO 保留完整 date、releasedate、originaldate、albumversion，不再丢掉日期精度或混用日期类型。
- 全量扫描成功走到收尾时，即使本轮未发现文件变化，也运行官方 GC，清理之前中断扫描遗留的空专辑。
- 不把不同发行日期的同名专辑无条件合并，不修改歌曲 ID、歌单或收藏。

重扫发生专辑 ID 变化时，新专辑会先入库，旧空专辑在扫描最后清理。
扫描途中暂时看到同名专辑不代表最终重复入库；应等扫描完成后判断。
本次部署的用户已确认 Redia 302 正常，扫描完成后专辑显示恢复正常。

已有 NFO 不等于不需要扫描。增量扫描通过目录中文件的名称、大小和修改时间检查变化，
通常跳过未变化的目录；全量扫描重新处理元数据，中断的全量扫描会在后续恢复。
本版仍保留目标元数据探测顺序：启用 `STRM.Metadata.ProbeLocalTargets` 后，
处理 STRM 时先读取白名单目标，再合并 NFO；已有 NFO 不会自动跳过目标探测。
NFO 优先且跳过目标探测的优化未纳入此版本。

## 验证结果

Linux 构建中的 20 个 Go 测试包通过；镜像冒烟测试覆盖旧版 STRM 回退、缓存播放链接跳转、
Range 206 断点续传、MP3 转码和 NFO 元数据。另新增的 NFO 重扫专辑 ID 稳定性测试在 Windows 通过。
使用旧数据库的测试副本进行隔离启动验证，修复镜像成功启动。
镜像包 SHA256：`17e041506e184c6a1396c9c1908dd6b097bd199865ee530948fd904c043cf46d`。

## 部署

镜像包位于 `binaries/liristy-fix1/navidrome-strm-nfo-linux-amd64.tar`。
导入后将现有 Compose 服务的 image 改为 `navidrome:0.63.2-liristy-fix1`，保留原网络、端口和挂载。
在原 Compose 目录重建服务；请先用独立测试数据验证你的 Redia 实际链路。
也可以将 Compose 的 image 设置为 `liristy/navidrome:latest`，拉取后重建服务。

升级前备份整个 data 目录；不要覆盖运行中的数据库或混用不同快照的 WAL/SHM。
私有数据库、诊断副本和凭据不随源码或镜像发布。

本地模拟流测试不等于所有 Redia/115 部署的端到端验证；不同环境仍需自行确认实际链路。
