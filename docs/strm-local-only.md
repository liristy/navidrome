# STRM 本地监听模式

这是完全禁止自动刮削的可选配置。要求“缺少 NFO 就刮削”，请使用[缺失才刮削模式](strm-watch.md)。

适用：Redia 等工具已在本地生成 STRM、NFO、封面，不希望 Navidrome 后台读取 CloudDrive 音频。
音乐库 `/music` 必须是本地旁挂文件目录，不能是 CloudDrive 实体音乐库，也不要用符号链接引向网盘。

使用 `liristy/navidrome:0.63.2-liristy-fix3`（linux/amd64），同时应用下述环境变量；
只更新镜像不能覆盖已有的探测开关。

## 配置

将 `contrib/docker-compose.strm-local.yml` 的 environment 合并到现有 navidrome 服务中。
保留原 image、端口、网络、data 挂载和 Redia 路径白名单配置。更改环境变量需重建容器，
仅 `docker restart` 不会应用新配置。迁移前先备份 data。

- `Scanner.ScanOnStartup=false`：正常重启不触发初始扫描。
- `Scanner.Schedule=0`：关闭定时扫描。
- `Scanner.WatcherWait=5s`：本地目录变化后合并事件，只扫描变化的目录。
- `Scanner.FollowSymlinks=false`：不跟随音乐库内指向其他目录的链接。
- `STRM.Metadata.ProbeLocalTargets=false`：不探测 STRM 目标音频。
- `STRM.Metadata.ProbeEmbeddedCover=false`：独立禁止 STRM 内嵌封面回源（fix3 起默认关闭）。
- `Scanner.Sidecar.Enabled=true`、`Trust=true`：读取本地 NFO；`GenerateOnStartup=false`：不自动生成缺失 NFO。

监听不只是 `.strm` 扩展名：NFO 和封面变化也需要更新索引。没有 NFO 的新 STRM 只能使用
指针自带信息或现有解析规则，不会为了补全标题、时长而自动刮削网盘。请由旁挂文件生成工具提供完整元数据。
停机期间的变化不会产生重启后的监听事件；这时可手动执行一次本地增量扫描。
首次空库也需要手动扫描导入已有 STRM/NFO。若 `/music` 是不提供文件系统事件的网络挂载，
监听可能无法发现外部变化，需要改用本地目录或显式执行本地扫描。

上游为保证数据库一致性，数据库迁移、ID 规则改变或上次扫描中断仍可能触发必要的恢复扫描，
即使 ScanOnStartup=false。此时上述禁用探测配置仍然生效，只整理本地元数据，不用删除数据库或清空扫描标志。

## 新修复的边界

已有有效 NFO 时先读 NFO，不再先打开目标音频；适用于增量、全量和恢复扫描。
Trust 控制 NFO 与 STRM 显式字段的优先级，不控制是否先探测目标。
损坏或无权读取的 NFO 会保留并告警，不自动回源；没有 NFO 时仅在 ProbeLocalTargets=true 的显式配置下才允许探测。
关闭探测时，缺少本地封面不再解析/打开目标音频，也不会把 STRM 文本交给音频提取器。
旧版缓存的内嵌封面若记录了白名单内的网盘来源，新版会直接使用缓存，不再访问该来源检查修改时间。
因此后台不会自动发现目标音频内嵌封面的变化，应更新本地旁挂封面。

播放、下载、转码是用户主动访问媒体的操作，不受后台探测开关限制；Redia 302 行为保持不变。
这不是全局断网模式，外部艺术家图片服务仍由原有外部服务配置控制。

本修复需使用包含上述代码的新版镜像；仅修改旧 fix1 的环境变量能关闭音频标签探测，
但旧镜像仍有缺少封面时回源的路径，不能把它宣称为完整的后台网盘零读取模式。

## 验证

Linux 构建的回归测试通过，包含元数据、扫描器、封面和 Subsonic 播放。
无网络、无用户数据的镜像测试验证：首次扫描完成后关闭启动扫描并移走目标音频，
重启未改变扫描开始时间；新增本地 STRM/NFO 被监听器成功导入，并保持单一专辑。
已有 NFO 的目标探测调用数为零，关闭探测时缺少封面不解析目标，旧缓存封面无需 stat 离线目标。
原有 Redia 接口兼容、Range 206 和 MP3 转码测试也通过。未改动用户服务器或数据库。
