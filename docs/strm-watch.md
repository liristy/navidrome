# 本地监听，缺少 NFO 才刮削（fix3）

镜像：`liristy/navidrome:0.63.2-liristy-fix3`，同时发布为 `liristy/navidrome:latest`，linux/amd64。
应用版本仍为 `0.63.2-liristy`。

将[监听配置](../contrib/docker-compose.strm-watch.yml)的 environment 合并到原服务，
保留原路径白名单、网络、端口和挂载，再拉取镜像并重建容器。仅 restart 不会加载新的环境变量。
`/music` 应是本地 STRM/NFO/封面目录，不是 CloudDrive 实体音乐库；保留独立的目标音乐只读挂载。
本地 STRM 目录需可写，以便创建缺失 NFO。

## 行为

- 普通重启不启动扫描；关闭定时扫描，保留目录变化监听，5 秒合并一次事件。
- 已有有效 NFO：先读本地 NFO，不探测目标音频，即使目标暂时不可用也可导入元数据。
- 新增 STRM 且缺少 NFO：开启 ProbeLocalTargets 和 GenerateOnStartup 后读取白名单目标，生成 NFO 并入库。
- 已有 NFO 不覆盖；无效或不可读的 NFO 保留并告警，不自动回源覆盖用户文件。
- `STRM.Metadata.ProbeEmbeddedCover=false`（默认）：不为缺失封面重新打开网盘音频，
  也不检查白名单目标的旧封面缓存源修改时间。此开关与缺失 NFO 刮削独立。
- 播放、下载、转码和 Redia 302 兼容行为不变；这些是用户主动读取媒体的操作。

`GenerateOnStartup` 是兼容旧配置保留的名称：它控制扫描过程中的缺失 NFO 生成，
不是强制启动全量扫描的开关。`ProbeConcurrency=1` 限制目标刮削并发，但不保证网盘不会风控，
批量添加大量缺少 NFO 的 STRM 仍会产生对应数量的目标读取。

首次空库、停机期间新增文件需要手动扫描一次。数据库迁移、ID 规则改变或上次扫描中断，
仍可能触发必要的恢复扫描；已有 NFO 仍复用本地，只有缺失 NFO 的条目可能读取目标。
网络文件系统可能不提供可靠的目录事件，因此建议 STRM 目录真正落在本地。

若完全不需要自动刮削，使用[严格本地模式](strm-local-only.md)。
