# RyoMonitor

[English](README.md) | [简体中文](README.zh-CN.md)

RyoMonitor 是一个轻量的自托管服务器监控面板，包含深色 Web 看板、密码登录页，以及中文/英文显示切换。

它面向单台 VPS 使用：

- Bash 采集脚本每秒写入一次 `status.json`。
- 静态监控看板读取 `status.json`。
- Python 认证网关负责登录和静态文件服务。
- Caddy 负责 HTTPS，并反代到 `127.0.0.1:8090`。

## 功能

- 展示 CPU、内存、Swap、磁盘、网络、平均负载、服务状态和主要进程。
- Web UI 支持中文和英文显示。
- 语言选择保存在 `localStorage`。
- 密码登录使用安全的 `HttpOnly` Cookie。
- 不需要数据库，也不需要前端构建步骤。
- 支持 GitHub 推送后在 VPS 上 `git pull` 更新。

## 文件结构

```text
app/index.html              监控看板 UI
app/mon-auth.py             密码登录和静态文件网关
scripts/ryo-monitor.sh      指标采集脚本
scripts/install.sh          首次安装脚本
scripts/update.sh           git pull + 重启脚本
systemd/*.service           systemd 服务模板
caddy/Caddyfile.example     Caddy 反代示例
.env.example                环境变量示例
```

## 运行要求

- 使用 systemd 的 Linux VPS
- Python 3.10+
- Bash
- Caddy
- Git，用于 GitHub 同步更新

## 在 VPS 上安装

把仓库克隆到 `/opt/ryo-monitor`：

```bash
git clone https://github.com/RyoSXu/RyoMonitor.git /opt/ryo-monitor
cd /opt/ryo-monitor
```

用 root 执行安装脚本：

```bash
DOMAIN=mon.example.com bash scripts/install.sh
```

安装脚本会要求输入登录密码，并把密码哈希和随机签名密钥写入：

```text
/etc/ryo-mon-auth.env
```

不要把这个文件提交到 Git。

## Caddy

添加类似配置：

```caddyfile
mon.example.com {
    reverse_proxy 127.0.0.1:8090
}
```

然后校验并 reload：

```bash
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

## 更新

本地修改并推送到 GitHub 后，在 VPS 上执行：

```bash
cd /opt/ryo-monitor
bash scripts/update.sh
```

更新脚本会执行 `git pull --ff-only`，检查 Python 和 Bash 语法，重启两个服务，并检查认证网关健康状态。

## 服务

```bash
systemctl status ryo-monitor.service
systemctl status ryo-mon-auth.service
```

`ryo-monitor.service` 写入：

```text
/opt/ryo-monitor/app/status.json
```

`ryo-mon-auth.service` 监听：

```text
127.0.0.1:8090
```

## 配置

认证环境变量：

```text
/etc/ryo-mon-auth.env
```

可选采集配置：

```text
/etc/ryo-monitor.env
```

示例：

```bash
RYO_MONITOR_STATUS_FILE=/opt/ryo-monitor/app/status.json
RYO_MONITOR_IFACE=eth0
```

## 安全建议

- 不要泄露 `MON_AUTH_SECRET`。
- 不要把 `/etc/ryo-mon-auth.env` 提交到 Git。
- 认证网关只绑定 `127.0.0.1`。
- 只通过 Caddy HTTPS 暴露监控面板。
- 如需轮换密码，重新生成 `/etc/ryo-mon-auth.env` 并重启 `ryo-mon-auth.service`。
