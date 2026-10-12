# alcywall

给 **niri + Noctalia v5** 的随机二次元壁纸工具，图源是 [栗次元 API](https://t.alcy.cc/)（`https://t.alcy.cc/ycy`）。

一个 Go 单文件二进制，零外部依赖（标准库），支持 Arch Linux（以及任何能跑 niri 的发行版）。

```bash
alcywall next                 # 换一张壁纸
alcywall watch -interval 15m  # 前台常驻，每 15 分钟换一张
alcywall install -interval 30m # 装成 systemd user timer，开机自动轮换
```

---

## 30 秒上手

```bash
# 1) 下载二进制（Release 里挑你的架构）
curl -L -o alcywall https://github.com/Become-ILLUSORY/alcywall/releases/latest/download/alcywall-linux-amd64
chmod +x alcywall && sudo install -m755 alcywall /usr/local/bin/

# 2) 体检：看看 Noctalia 在不在、图库通不通
alcywall doctor

# 3) 换一张
alcywall next

# 4) 开机自动轮换（可选）
alcywall install -interval 30m
```

---

## 它到底做了什么

```
        ┌──────────────────────┐
        │  https://t.alcy.cc   │   随机取图（302 或 /json）
        └──────────┬───────────┘
                   │  ?w=2560 按显示器宽度取合适尺寸
                   ▼
        ~/Pictures/Wallpapers/alcy-xxx.webp     下载 + 去重 + 自动清理旧图
                   │
        ┌──────────┴───────────┐
        ▼                      ▼
  noctalia msg            niri background-color
  wallpaper-set           （取图片平均色写进 config.kdl）
  真正的壁纸                       补底色，过渡时不留白
```

**niri 和 Noctalia 的分工**（这点很关键，别搞混）：

- **壁纸本身归 Noctalia 管**。niri 是合成器，它没有「设置壁纸」这个 IPC —— 图片由 shell（Noctalia）或 swaybg 这类 layer-shell 工具渲染。所以真正换图走的是 `noctalia msg wallpaper-set`。
- **niri 管的是每个输出的纯色底色**。换图时顺便把新图的平均色写进 `~/.config/niri/config.kdl` 的 `background-color`，这样壁纸切换的空档期不会闪白/闪黑。不想要就 `-no-niri` 或配置里 `niri_sync_background: false`。

---

## 命令

| 命令 | 作用 |
| --- | --- |
| `next` | 换一张壁纸（默认命令，直接 `alcywall` 也行） |
| `watch` | 前台常驻，按 `-interval` 轮换 |
| `probe` | 体检 API：延迟、各图库是否可用、图片尺寸 |
| `config` | 打印生效配置；`-init` 写出默认配置文件 |
| `history` | 最近换过的壁纸 |
| `doctor` | 端到端体检：Noctalia IPC / niri 输出 / 依赖 / 网络 |
| `install` / `uninstall` | 装/卸 systemd user timer |

---

## 图库

`-category <key>`，默认 `ycy`。

| key | 内容 | key | 内容 |
| --- | --- | --- | --- |
| `ycy` | 二次元自适应（按 UA 出横图/竖图） | `ysmp` | 原神竖图 |
| `moez` | 萌版自适应 | `moemp` | 萌版竖图 |
| `ysz` | 原神自适应 | `mikump` | 初音未来竖图 |
| `mikuz` | 初音未来自适应 | `tx` | 头像方图 |
| `pc` | PC 横图 | `bd` | 白底素材 |
| `fj` | 风景横图 | `xhl` | 小狐狸 |
| `moe` | 萌版横图 | `lai` | 七濑胡桃 |
| `mp` | 手机竖图 | `acg` | ACG 动图（mp4，**不能做壁纸**，会被自动跳过） |
| `miku` | 初音未来横图 | | |
| `ys` | 原神横图 | | |

`alcywall probe -all` 可以一次性测所有图库。

---

## 尺寸（`?w=` 参数）

栗次元的图基本是 2K~8K。默认行为是**按你显示器最宽的那个分辨率去取图**，这样既不会拿 8K 图浪费解码，也不会糊：

```bash
alcywall next -w 2560          # 固定 2560 宽
alcywall next -w 0             # 要原图
alcywall next -no-fit          # 同上，不开自动适配
```

实测服务端行为（2026-10 验证）：

- `?w=N` 把图**缩小**到 N 像素宽，高度按比例；N 大于原图宽度时返回原图（不会放大）
- **N > 4096 直接 404**，所以工具内部硬夹到 4096
- 小图请求大 `w` 不会 404，只是原样返回（例如 1920 宽的图 `?w=2560` 还是 1920）
- `?h=` 也可以带，但服务端按比例缩，实际以宽为准

---

## 配置文件

`~/.config/alcywall/config.json`（`alcywall config -init` 生成）：

```json
{
  "base_url": "https://t.alcy.cc",
  "category": "ycy",
  "width": 0,
  "keep": 40,
  "notify": true,
  "niri_sync_background": true,
  "apply_backend": "auto"
}
```

- `width`：`0` = 自动按显示器（推荐）；固定值就直接用。
- `keep`：只清理 **本工具下载的**（`alcy-` 前缀）文件，你自己放进去的图永远不会被删。
- `notify`：走 Noctalia 自己的通知（`notification-show`），没有 Noctalia 时退回 `notify-send`。
- `apply_backend`：`auto`（默认）/ `noctalia` / `niri` / `none`。`niri` 模式只同步底色不换图（没装 shell 时用）。

环境变量（`ALCYWALL_*`）可以覆盖任意一项，方便写进 systemd 或快捷键脚本。

---

## 快捷键 / 开机自启

niri 配置里加一条：

```kdl
binds {
    Mod+W { spawn "alcywall" "next" "-silent"; }
}
```

开机自启（不用 systemd 的话）：

```kdl
spawn-at-startup "alcywall" "watch" "-interval" "30m"
```

或者用 systemd（`alcywall install` 已经帮你写好）：

```bash
systemctl --user status alcywall.timer
journalctl --user -u alcywall.service -n 50
```

---

## 依赖

**必需**：无。一个二进制搞定。

**可选**（有则更好）：

| 工具 | 用途 | Arch 包 |
| --- | --- | --- |
| Noctalia | 真正渲染壁纸的 shell | `noctalia` |
| ImageMagick | 取图片平均色 → 同步 niri 底色 | `imagemagick` |
| swaybg | Noctalia 不在时的图片兜底 | `swaybg` |
| libnotify | Noctalia 不在时的通知兜底 | `libnotify` |

---

## 从源码构建

```bash
git clone https://github.com/Become-ILLUSORY/alcywall.git
cd alcywall
go build -o alcywall .
go test ./...
```

交叉编译：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o alcywall .
```

CI 会在每次 push / tag 时跑测试、vet、并交叉编译 linux/darwin × amd64/arm64 发布到 Release。

---

## License

MIT
