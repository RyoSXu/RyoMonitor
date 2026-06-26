<p align="center">
  <img src="app/assets/logo.svg" alt="RyoMonitor logo" width="120">
</p>

# RyoMonitor

[English](README.md) | [简体中文](README.zh-CN.md)

RyoMonitor 是一个轻量的自托管 VPS 监控面板，带深色看板、密码登录，并且不需要前端构建步骤。

它适合小型服务器：当完整监控系统太重，但你又需要一个清晰、私有、容易同步的状态页时，RyoMonitor 正好够用。

<p align="center">
  <img src="docs/screenshot.png" alt="RyoMonitor dashboard" width="900">
</p>

## 为什么是 RyoMonitor

- Bash + Python 小体量运行时
- 不需要数据库
- 不需要前端构建
- 适合单台 VPS 部署
- 自带密码保护
- Web UI 支持中文和英文
- 支持 GitHub 同步更新

## 展示内容

- CPU 使用率
- 内存和 Swap 使用率
- 磁盘使用率
- 网络吞吐
- 平均负载
- 服务状态
- 按内存占用排序的主要进程

## 工作方式

```text
ryo-monitor.service
  -> scripts/ryo-monitor.sh
  -> app/status.json

ryo-mon-auth.service
  -> app/mon-auth.py
  -> 密码登录 + 静态看板

Caddy
  -> HTTPS
  -> reverse_proxy 127.0.0.1:8090
```

## 文件结构

```text
app/index.html              监控看板 UI
app/mon-auth.py             密码登录和静态文件网关
app/assets/logo.svg         项目 logo 和前端图标
scripts/ryo-monitor.sh      指标采集脚本
scripts/install.sh          首次安装脚本
scripts/update.sh           git pull + 重启脚本
systemd/*.service           systemd 服务模板
caddy/Caddyfile.example     Caddy 反代示例
docs/screenshot.png         看板截图
.env.example                环境变量示例
```

## 运行要求

- 使用 systemd 的 Linux VPS
- Python 3.10+
- Bash
- Caddy
- Git，用于 GitHub 同步更新

## 安装

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
