# 使用与部署指南

[返回 README](../README.md) · [下载](https://github.com/AhsokaTano26/SpeedProbe/releases) · [报告问题](https://github.com/AhsokaTano26/SpeedProbe/issues)

本指南说明当前源码的配置、部署、测速方法和开发流程。示例中的域名、Token 和证书路径需替换为实际值；版本专属功能会单独标注。

- [安装](#安装)
- [客户端 .env](#客户端自动读取-env)与[完整参数](#命令与配置)
- [定时测速与日志](#定时测速与日志)
- [测速方法](#测速方法与准确性)与[资源限制](#流量与并发限制)
- [Docker](#docker-compose)、[Ubuntu / systemd](#ubuntu-与-systemd)、[Nginx / HTTPS](#nginx-与-https)、[IPv4 / IPv6](#ipv4--ipv6)
- [REST API](#rest-api)与[故障排查](#故障排查)
- [开发与测试](#开发与测试)、[自动构建与 Release](#自动构建与-release)

## 安装

从 [GitHub Releases](https://github.com/AhsokaTano26/SpeedProbe/releases) 下载对应平台的压缩包，运行二进制无需安装 Go。

| 系统 | 架构 | 格式 |
| --- | --- | --- |
| Linux | `amd64`、`arm64`、`armv7` | `.tar.gz` |
| macOS | `amd64`（Intel）、`arm64`（Apple Silicon） | `.tar.gz` |
| Windows | `amd64`、`arm64` | `.zip` |

以下以 Linux amd64 的 `v0.1.0` 为例，需要 curl、tar 和 sha256sum，在新目录中执行：

```sh
mkdir speedprobe-v0.1.0
cd speedprobe-v0.1.0
curl -fLO https://github.com/AhsokaTano26/SpeedProbe/releases/download/v0.1.0/speedprobe_v0.1.0_linux_amd64.tar.gz
curl -fLO https://github.com/AhsokaTano26/SpeedProbe/releases/download/v0.1.0/checksums.txt
sha256sum --ignore-missing --check checksums.txt
tar -xzf speedprobe_v0.1.0_linux_amd64.tar.gz
./speedprobe version
```

版本命令应显示 `SpeedProbe v0.1.0`。macOS 可使用 `shasum -a 256 --check` 校验对应文件；Windows 解压后运行 `speedprobe.exe`。

从 `v0.1.1` 起，发布包包含二进制、MIT 许可证、README、`.env.example`、本指南和 Nginx 示例。`v0.1.0` 使用原来的单文件 README。所有发布二进制禁用 CGO，并写入标签版本。

第一次运行见 [README 快速开始](../README.md#快速开始)。健康检查：

```sh
curl --fail http://127.0.0.1:8080/healthz
```

成功时返回 `{"status":"ok"}`。健康检查不需要 Token，但不能据此判断 Token 是否有效。

## 客户端自动读取 .env

> 自动读取 `.env` 从 `v0.1.1` 开始支持。`v0.1.0` 请使用环境变量或 `--token-file`，或升级客户端。

`test` 模式在启动时自动读取**当前工作目录**的 `.env`，无需执行 `source`，也无需在命令行传入 Token。将以下配置写入 `.env`，替换为实际服务地址和 Token：

```dotenv
SPEEDPROBE_SERVER=https://speed.example.com
SPEEDPROBE_TOKEN='replace-with-your-token'
SPEEDPROBE_CONNECTIONS=4
SPEEDPROBE_DURATION=10s
# 定时测速按需启用：
# SPEEDPROBE_INTERVAL=5m
# SPEEDPROBE_LOG_FILE=logs/speedprobe.jsonl
```

```sh
chmod 600 .env
./speedprobe test
```

配置优先级为：**命令行参数 > 已有非空环境变量 > .env > 内置默认值**；环境变量和文件中的空值按未配置处理。认证方式也遵循此优先级，例如 `--token-file .speedprobe-tokens` 会覆盖 `.env` 中的 Token。同一优先级不能同时配置非空 Token 和 Token 文件。

```sh
./speedprobe test --env-file config/client.env  # 指定配置文件
./speedprobe test --env-file= --server https://speed.example.com --token-file .speedprobe-tokens
```

默认 `.env` 不存在时照常运行；显式指定的文件不存在或已有文件格式错误时会报错。`--env-file=` 关闭文件加载。相对文件路径以当前工作目录为准，不搜索父目录或可执行文件目录。

支持 `KEY=VALUE`、可选 `export` 前缀、单/双引号、注释、CRLF 和 UTF-8 BOM。未加引号的值中，行首或空白后的 `#` 开始注释；值内的 `#` 可用引号保留。双引号支持 `\\`、`\"`、`\n`、`\r`、`\t` 等转义，单引号内保持字面值。配置不执行 shell 命令、不展开 `$VAR` 或 `$(...)`，不支持多行值，也不会修改进程环境。文件需为 UTF-8，总大小最多 1 MiB，每行小于 8 KiB；解析错误不会回显配置行内容。

## 命令与配置

```sh
./speedprobe serve --help
./speedprobe test --help
./speedprobe version
```

### 服务端参数

| 参数 | 环境变量 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `--addr` | `SPEEDPROBE_ADDR` | `:8080` | 监听地址；默认监听所有可用地址 |
| `--network` | `SPEEDPROBE_NETWORK` | `tcp` | `tcp`、`tcp4` 或 `tcp6` |
| `--tokens` | `SPEEDPROBE_TOKENS` | 必填，或使用文件 | 逗号分隔多个 Token；未配置时可回退到 `SPEEDPROBE_TOKEN` |
| `--tokens-file` | `SPEEDPROBE_TOKENS_FILE` | 无 | 每行一个 Token，可有空行及 `#` 开头的注释 |
| `--max-concurrent` | `SPEEDPROBE_MAX_CONCURRENT` | `32` | 全局同时进行的上传/下载请求上限 |
| `--max-per-token` | `SPEEDPROBE_MAX_PER_TOKEN` | `8` | 单 Token 同时进行的上传/下载请求上限 |
| `--max-bytes` | `SPEEDPROBE_MAX_BYTES` | `1073741824` | 每个上传或下载请求的有效载荷字节上限 |
| `--max-duration` | `SPEEDPROBE_MAX_DURATION` | `60s` | 每个测速请求的硬时长上限 |
| `--bytes-per-second` | `SPEEDPROBE_BYTES_PER_SECOND` | `0` | 全部 Token、上传和下载共享的有效载荷带宽上限；`0` 为不限速 |
| `--tls-cert` | `SPEEDPROBE_TLS_CERT` | 无 | 原生 HTTPS 证书 PEM 路径 |
| `--tls-key` | `SPEEDPROBE_TLS_KEY` | 无 | 原生 HTTPS 私钥 PEM 路径；与证书同时配置 |

Token 与 Token 文件只能选择一种来源。空 Token、重复 Token、包含空白/逗号/非 ASCII 字符的 Token 会被拒绝。没有 Token 的服务端无法启动。全局并发最大 4096；单 Token 上限不能超过全局上限。时长上限为 24 小时。

### 客户端参数

| 参数 | 环境变量 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `--env-file` | 无 | `.env` | 自动加载客户端配置；空字符串禁用；默认文件不存在时忽略 |
| `--server` | `SPEEDPROBE_SERVER` | 必填 | HTTP/HTTPS 服务地址，支持路径前缀 |
| `--token` | `SPEEDPROBE_TOKEN` | 必填，或使用文件 | 客户端 Bearer Token |
| `--token-file` | `SPEEDPROBE_TOKEN_FILE` | 无 | 只包含一个 Token 的文件 |
| `--connections` | `SPEEDPROBE_CONNECTIONS` | `4` | 每方向的独立 TCP 连接数，范围 1–256 |
| `--duration` | `SPEEDPROBE_DURATION` | `10s` | 每个方向的测速窗口，最短 `100ms` |
| `--timeout` | `SPEEDPROBE_TIMEOUT` | `5s` | 连接、TLS、响应头超时，以及上传结束后的确认等待时间；最大 `1m` |
| `--latency-samples` | `SPEEDPROBE_LATENCY_SAMPLES` | `20` | 空载延迟样本数，范围 1–10000 |
| `--latency-interval` | `SPEEDPROBE_LATENCY_INTERVAL` | `50ms` | 样本间隔，允许 `0`，最大 `1m` |
| `--ip` | `SPEEDPROBE_IP` | `auto` | `auto`、`4` 或 `6` |
| `--json` | `SPEEDPROBE_JSON` | `false` | 输出 JSON；可用 `--json=false` 关闭 |
| `--interval` | `SPEEDPROBE_INTERVAL` | `0` | 两轮开始时间的间隔，如 `5m`、`1h`；`0` 表示单次测速 |
| `--count` | `SPEEDPROBE_COUNT` | `0` | 周期测速的最大轮数；`0` 表示持续执行；多轮需要正数间隔 |
| `--log-file` | `SPEEDPROBE_LOG_FILE` | 单次无日志；周期默认 `speedprobe.jsonl` | JSON Lines 追加日志，可指定目录和文件路径 |

连接数和时长必须满足服务端公布的限制。客户端依次执行延迟、下载、上传，每轮耗时约为 `2 × duration + 延迟采样 + 预热 + 上传确认时间`。任一连接失败会取消本轮其它连接；单次模式返回非零退出码，周期模式记录失败后继续下一轮。`Ctrl+C`/SIGTERM 会取消客户端；服务端最多等待 5 秒让活跃请求结束，然后关闭连接。

`test` 自动加载的 `.env` 只为客户端的 `SPEEDPROBE_*` 参数提供默认值。`serve` 仍从命令行、进程环境和 Token 文件读取配置；Docker Compose 和 systemd 可各自负责加载服务端环境文件。客户端代理仍遵循**进程环境**中的 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`，而不是 `.env` 中的代理变量；指定 IP 版本也会影响代理连接的 IP 版本。

### Token 与环境变量

Token 文件每行一个 Token，客户端文件必须恰好包含一个。可使用 `openssl rand -hex 32` 生成随机 Token，类 Unix 系统上建议将文件权限设为 `0600`。

客户端使用单数 `SPEEDPROBE_TOKEN`；服务端使用复数 `SPEEDPROBE_TOKENS`。以下是环境变量方式（Linux / macOS 的 Bash / Zsh）：

```sh
# 服务端：替换为独立的随机 Token，多个 Token 以逗号分隔。
export SPEEDPROBE_TOKENS='replace-with-token-a,replace-with-token-b'
./speedprobe serve --addr 127.0.0.1:8080
```

```sh
# 客户端：使用服务端接受的一个 Token。
export SPEEDPROBE_TOKEN='replace-with-token-a'
./speedprobe test --server http://127.0.0.1:8080
```

环境变量或 Token 文件可避免 Token 出现在命令行参数中。程序的服务日志、结果 JSON 和帮助输出不包含 Token。配置样例见 [.env.example](../.env.example)。

## 定时测速与日志

立即执行一轮，之后每隔 5 分钟测速并追加日志（需先设置 `SPEEDPROBE_TOKEN`，并将示例地址替换为实际服务地址）：

```sh
./speedprobe test --server https://speed.example.com \
  --interval 5m --duration 10s --log-file logs/speedprobe.jsonl
```

指定次数后自动退出：

```sh
./speedprobe test --server https://speed.example.com \
  --interval 5m --count 12 --log-file logs/speedprobe.jsonl
```

后台运行示例：

```sh
nohup ./speedprobe test --server https://speed.example.com \
  --interval 5m --log-file logs/speedprobe.jsonl \
  > speedprobe-console.log 2>&1 &

tail -f logs/speedprobe.jsonl
```

间隔按每轮实际开始时间计算。若一轮耗时超过间隔，完成后立即启动下一轮，始终串行执行，不积压或补发过去的轮次。每个方向的时长仍由 `--duration` 决定。关闭进程即停止调度，重新启动后立即测一次，轮次从 1 开始，已有日志会保留并继续追加。

日志每行是一个独立 JSON 对象：

- `schema_version`：日志结构版本，当前为 1。
- `run`、`server`：本次进程内的轮次和测速地址。
- `started_at`、`completed_at`：UTC 开始及结束时间。
- `status`：`success`、`error` 或 `canceled`。
- `result`：成功时包含完整客户端结果，包括上传/下载速度、字节数、耗时、延迟样本和抖动；失败时省略。
- `error`：失败或中断原因；不会记录 Token。

失败记录示例（实际文件中占一行）：

```json
{"schema_version":1,"run":2,"server":"https://speed.example.com","started_at":"2026-10-14T15:00:00Z","completed_at":"2026-10-14T15:00:05Z","status":"error","error":"server preflight: request timed out"}
```

父目录会自动创建；在类 Unix 系统上，新目录权限为 `0700`、新日志文件权限为 `0600`，Windows 使用系统文件权限。每轮结束立即写入并同步日志；日志打开或写入失败会停止运行。可以在单次模式下使用 `--log-file` 保存一轮结果。

周期模式中，网络或测速失败会被记录，并等待下一轮继续。指定 `--count` 时，若完成的批次有失败，最终退出码为 1。`Ctrl+C`/SIGTERM 可中断等待或正在进行的测速，正在测速的中断会记为 `canceled`，调度进程正常退出。

`--interval ... --json` 会在 stdout 输出同样的 JSON Lines 记录，包含成功和失败，适合管道处理；单次 `--json` 仍输出原有完整结果对象。

## 测速方法与准确性

- **连接**：每个并发请求拥有独立的 HTTP/1.1 Transport；禁用 HTTP/2 多路复用。每个方向开始前，先对每个连接执行一次 ping，完成 TCP/TLS 预热。
- **下载**：所有连接共享一个单调时钟计时窗口，累计窗口内完成的有效载荷读取，速度为 `总字节数 × 8 / 窗口秒数 / 1,000,000` Mbps。请求调度和请求头往返处于窗口内，连接预热不计入；跨越窗口末尾的最后一块读取不计入，因此短窗口可能略低估。
- **上传**：客户端在窗口结束时停止生成数据，以正常 EOF 完成分块上传。服务端返回实际读取的字节数；客户端核对确认字节数。速度分母是从公共开始时间到最后一个服务端确认抵达的时间，包含发送缓冲区排空和确认往返时间，避免只计算“写入本机缓冲区”的虚高值。服务端有限速时，客户端会按其公布的上限控制上传发送节奏。
- **延迟**：复用已经预热的连接测量 HTTP 请求/响应 RTT，包含网络、TLS 与应用处理开销，**不是 ICMP ping**。输出平均值、最小值、最大值、中位数和 nearest-rank p95。
- **抖动**：相邻延迟样本差值绝对值的平均值，即 `mean(abs(RTT[i] - RTT[i-1]))`；单样本为 0。延迟和抖动均在空载时测量。
- **数据**：上传/下载使用随机生成的 64 KiB 数据块循环传输，数据生成发生在测量窗口之外。统计应用有效载荷，不包含 HTTP、TCP、TLS 或链路层开销；单位是十进制 Mbps/MB，字节上限示例中的 GiB 是二进制单位。
- **限制**：下载提前 EOF、上传缺少/错误确认、401、429、413、压缩或缓存响应都会使测速失败。上传确认等待也受服务端硬时长上限约束；在慢链路或高延迟场景，可增大 `--timeout` 和服务端 `--max-duration`。

短测试易受调度、TCP 慢启动和计时误差影响，公网测速推荐 10–30 秒并重复多次；低带宽下连接数可减至 1。实际值还受客户端/服务端 CPU、TLS、代理、其它用户、链路拥塞和服务端限速影响。配置带宽上限时，结果体现该受限路径的吞吐量。

默认服务端在同一时刻最多有 32 个测速流，每个上传流持有最多 64 KiB 缓冲，下载共享一个只读 64 KiB 数据块。HTTP/TLS/操作系统仍有额外缓冲和连接开销；总内存不随传输字节数线性增长。

## 流量与并发限制

`max-bytes` 是 **每请求** 上限，`max-duration` 是 **每请求** 时长上限；不是整个测速会话或月度配额。下载达到字节上限会结束；上传超限返回 413。测速时长到期会中断未完成的流。可选带宽限制使用共享 token bucket，突发容量约为 50ms 的字节量，至少 1 字节，最多 64 KiB；计数涵盖所有 Token 的上传/下载有效载荷。

并发限制只约束上传/下载流，info、ping、healthz 不占测速槽位。同一 Token 的多个客户端共享单 Token 上限。客户端只会预检查静态上限，正在运行的其它用户仍可能导致 429。服务端无需持久存储，重启会清空内存状态。

## Docker Compose

需要 Docker 和 Compose 插件。以下命令在项目源码根目录执行；可先使用 `git clone https://github.com/AhsokaTano26/SpeedProbe.git` 获取源码。

```sh
cp .env.example .env
# 编辑 .env 中 SPEEDPROBE_TOKENS，填入随机 Token。
docker compose up -d --build
docker compose logs -f speedprobe
```

默认只将服务发布到宿主机 `127.0.0.1:8080`，适合宿主机 Nginx 反向代理。直接对外发布时修改 `SPEEDPROBE_BIND_IP` 并配置 HTTPS。容器使用非 root 用户、只读根文件系统、移除 Linux capabilities；默认限制为 128 MiB 内存和 2 个 CPU，可按带宽需求调整 [compose.yaml](https://github.com/AhsokaTano26/SpeedProbe/blob/main/compose.yaml)。

镜像采用多阶段构建，最终基于 scratch，只有静态二进制和 HTTPS CA 证书包。单独运行容器前，先按[环境变量示例](#token-与环境变量)导出服务端的 `SPEEDPROBE_TOKENS` 或客户端的 `SPEEDPROBE_TOKEN`；Compose 的 `.env` 不会自动导入当前 shell。

```sh
docker build --build-arg VERSION=dev -t speedprobe:local .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e SPEEDPROBE_TOKENS="$SPEEDPROBE_TOKENS" speedprobe:local

docker run --rm -e SPEEDPROBE_TOKEN="$SPEEDPROBE_TOKEN" \
  speedprobe:local test --server https://speed.example.com --json
```

`/healthz` 可供外部探针检查；scratch 镜像不包含 shell、curl 或内置 HEALTHCHECK。更改 Token 后重启服务生效。不要把 `.env`、Token 文件或私钥加入镜像。

## Ubuntu 与 systemd

以下适用于使用 systemd 的 Ubuntu，需要 sudo 权限。当前目录应已有解压后的 `speedprobe` 和准备好的 `.speedprobe-tokens`。本例监听 `127.0.0.1:8080`，供 Nginx 反向代理。

安装程序、创建专用用户并设置 Token 文件权限（Bash / Zsh）：

```sh
sudo install -m 0755 ./speedprobe /usr/local/bin/speedprobe
getent group speedprobe >/dev/null || sudo groupadd --system speedprobe
id -u speedprobe >/dev/null 2>&1 || sudo useradd --system --gid speedprobe \
  --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin speedprobe
sudo install -d -o root -g speedprobe -m 0750 /etc/speedprobe
sudo install -o root -g speedprobe -m 0640 .speedprobe-tokens /etc/speedprobe/tokens
```

创建服务单元：

```sh
sudo tee /etc/systemd/system/speedprobe.service >/dev/null <<'EOF'
[Unit]
Description=SpeedProbe network speed test server
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=speedprobe
Group=speedprobe
ExecStart=/usr/local/bin/speedprobe serve --addr 127.0.0.1:8080 --tokens-file /etc/speedprobe/tokens
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now speedprobe
sudo systemctl status speedprobe --no-pager -l
curl --fail http://127.0.0.1:8080/healthz
```

查看日志与重启：

```sh
sudo journalctl -u speedprobe -f
sudo systemctl restart speedprobe
```

本例直接在 `ExecStart` 中指定参数，不需要环境文件。使用 `EnvironmentFile=` 配置时，由 systemd 读取该文件，再传给 `serve`，文件需为 systemd 支持的语法。

服务运行 `/usr/local/bin/speedprobe`，确认安装和启动成功后，可删除下载目录中的原始二进制副本。更改 Token 后需重启服务。

## Nginx 与 HTTPS

将 [deploy/nginx.conf](../deploy/nginx.conf) 的 `server` 块放入 Nginx 的 `http` 上下文，修改域名、证书路径及 upstream 地址，执行 `nginx -t` 后重载。

测速所需的关键配置：

```nginx
proxy_http_version 1.1;
proxy_set_header Connection "";
proxy_set_header Authorization $http_authorization;
proxy_set_header Accept-Encoding "identity";
proxy_buffering off;
proxy_request_buffering off;
proxy_cache off;
proxy_next_upstream off;
gzip off;
client_max_body_size 0;
```

上传缓冲关闭后，未知长度的 chunked body 可持续转发到 Go 服务；服务端负责逐流字节上限。代理超时应高于服务端 `max-duration` 和预期确认等待时间。不要对测速端点启用 gzip/Brotli、响应缓存、请求体缓存、CDN 转换或自动重试；Go 的 Flush 和 `X-Accel-Buffering: no` 无法覆盖所有中间代理的行为。

路径前缀示例：Nginx 使用 `location /speed/ { proxy_pass http://127.0.0.1:8080/; ... }` 去除 `/speed/` 前缀，客户端连接 `https://speed.example.com/speed`。原配置其它流式设置也应保留。

原生 HTTPS 也可用：

```sh
./speedprobe serve --addr :8443 --tokens-file .speedprobe-tokens \
  --tls-cert fullchain.pem --tls-key privkey.pem
```

客户端验证 TLS 证书，禁止跟随重定向。服务端使用 TLS 1.2+；Token 仅从 `Authorization: Bearer ...` 读取，不接受 URL 中的 Token。不启用 CORS，不信任 `X-Forwarded-For` 进行鉴权或配额分组。公开部署使用 HTTPS，保护访问凭据。

## IPv4 / IPv6

```sh
./speedprobe serve --addr 0.0.0.0:8080 --network tcp4 --tokens-file .speedprobe-tokens
./speedprobe serve --addr '[::]:8080' --network tcp6 --tokens-file .speedprobe-tokens
./speedprobe test --server 'http://[::1]:8080' --ip 6 --token-file .speedprobe-tokens
```

默认 `--network tcp --addr :8080` 遵循 Go/操作系统的双栈监听行为；具体是否同时接受两类地址取决于系统配置。IPv6 字面地址和 URL 需要方括号。Docker IPv6 需单独配置 Docker 网络及端口发布，或通过双栈 Nginx 对外提供服务。

## REST API

除 `/healthz` 外均要求单个 `Authorization: Bearer <token>` 请求头。路径严格匹配，不自动跳转；不支持 HEAD。测速响应包含 `Cache-Control: no-store, no-cache, must-revalidate, no-transform`、`X-Accel-Buffering: no`、`X-Speedprobe-Protocol: 1`。

| 方法与路径 | 参数 | 响应 |
| --- | --- | --- |
| `GET /healthz` | 无 | 200，`{"status":"ok"}`，公开存活检查 |
| `GET /api/v1/info` | 无 | 200，协议版本和当前限制 |
| `GET /api/v1/ping` | 无 | 204，无响应体 |
| `GET /api/v1/download` | 可选 `duration=10s`、`bytes=1048576` | 200，流式 `application/octet-stream`；参数不能超过服务端上限 |
| `POST /api/v1/upload` | 可选 `duration=15s` | 原始二进制请求体，支持 chunked；200 返回确认字节数和服务端读取耗时 |

下载/上传省略 `duration` 使用服务端上限；下载省略 `bytes` 使用服务端字节上限。不接受重复/未知/无效测速查询参数，上传不接受 `bytes` 参数或压缩请求体。

上传确认示例：

```json
{"bytes": 1048576, "elapsed_seconds": 1.023}
```

错误格式为 `{"error":"..."}`；常见状态包括 400（参数/请求体错误）、401（鉴权失败）、404、405、408（上传超时）、413（上传超限）、415（压缩上传）、429（并发超限，带 `Retry-After: 1`）。已经开始的下载无法改写状态码，超时/断连会直接中断响应，客户端检测不完整流。

客户端结果 JSON schema 版本为 `schema_version: 1`，含 `server`、UTC `started_at`、`connections`、`requested_duration_seconds`、`server_limits`、`latency`、`download` 和 `upload`。每个方向包含 `bytes`、`elapsed_seconds` 和 `mbps`；延迟包含全部 `samples_ms` 及聚合统计。单次 JSON 模式 stdout 只输出成功结果，错误写 stderr，失败退出码为 1；周期模式的记录结构及退出行为见[定时测速与日志](#定时测速与日志)。

## 故障排查

- **连接超时**：核对监听地址、端口、HTTP/HTTPS、容器端口映射和防火墙规则；先在服务器本机请求 `/healthz`。
- **401**：核对客户端 Token 是否属于服务端配置；客户端变量为单数 `SPEEDPROBE_TOKEN`。
- **429**：检查全局及单 Token 并发限制，其他客户端也会占用槽位。
- **early download EOF**：检查每请求字节上限和代理缓冲，可提高 `--max-bytes` 或缩短 `--duration`。
- **上传确认超时**：检查链路及代理上传缓冲，按需调整客户端 `--timeout`、服务端 `--max-duration` 和代理超时。
- **systemd 217/USER**：确认服务单元中的用户和组存在，并检查[专用用户创建步骤](#ubuntu-与-systemd)。

仍无法解决时，请提交 [Issue](https://github.com/AhsokaTano26/SpeedProbe/issues)，附上版本、运行环境、脱敏后的命令和错误输出。不要提交真实 Token 或私钥。

## 开发与测试

从源码构建需要 Go **1.23+**；以下命令在源码根目录执行。`make` 示例适用于安装了 Make 的环境，竞态检测还需要可用的 C 编译器。

```sh
git clone https://github.com/AhsokaTano26/SpeedProbe.git
cd SpeedProbe
make build
./bin/speedprobe version
```

也可以直接构建：

```sh
go build -trimpath -o speedprobe ./cmd/speedprobe
```

```sh
make check           # race tests + go vet + formatting check
go test -cover ./...

# 交叉编译，不需要 CGO
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/speedprobe-linux ./cmd/speedprobe
```

代码结构：

```text
cmd/speedprobe/     CLI、环境配置、信号与 TLS 启动
internal/protocol/ REST wire types 与协议常量
internal/server/   鉴权、并发准入、共享限流、流式处理
internal/client/   预热、计时、服务端确认、统计与输出
deploy/            Nginx 配置示例
```

测试覆盖鉴权、多 Token、输入校验、并发与流量上限、短读计费、取消、真实 HTTP 流式传输、客户端/服务端联调、确认耗时、缓存/压缩/重定向拒绝、路径代理、配置优先级、`.env` 自动加载和安全解析、周期调度、日志追加及失败后继续。CI 在 Linux 上验证 Go 1.23/1.26，同时在 macOS、Windows 上运行 Go 1.26 测试，执行竞态检测、静态检查、Docker 构建和发布包校验。

## 自动构建与 Release

GitHub Actions 分为 [CI](https://github.com/AhsokaTano26/SpeedProbe/blob/main/.github/workflows/ci.yml) 和 [Release](https://github.com/AhsokaTano26/SpeedProbe/blob/main/.github/workflows/release.yml)：

- 分支推送、PR 或手动运行 CI：测试通过后构建七个平台的开发包，在对应 Actions 运行页面的 **Artifacts** 中下载，保留 14 天。这些包的版本为 `v0.0.0-ci.<运行编号>`。
- 推送 `v<主版本>.<次版本>.<修订版本>` 标签：测试和 Docker 构建通过后，自动创建 GitHub Release，上传压缩包和 `checksums.txt`，并生成发布说明。
- 带后缀的标签，例如 `v0.1.0-rc.1`：自动标记为预发布，不覆盖稳定版的 Latest。
- Release 支持 **Run workflow** 手动输入已有标签。测试、构建始终使用该标签指向的提交；标签不存在会失败，不会自动创建标签。上传中断留下的草稿可通过重跑继续完成；已发布版本的附件保持原样，修改产物请发布新版本。

发布前，先将工作流和项目源码提交并推送到 GitHub，再创建并推送尚未使用的版本标签。例如，发布下一版 `v0.2.0`：

```sh
git tag -a v0.2.0 -m "SpeedProbe v0.2.0"
git push origin v0.2.0
```

预发布示例：

```sh
git tag -a v0.2.0-rc.1 -m "SpeedProbe v0.2.0 release candidate"
git push origin v0.2.0-rc.1
```

使用 GitHub 内置的 `GITHUB_TOKEN`，无需额外配置个人访问令牌；只有发布 job 获得 `contents: write`，检查与构建 job 只读。官方 Actions 固定到完整提交 SHA。流程先完整上传所有附件，再核对标签并发布 Release；构建或上传期间标签发生变化会停止发布。

发布包命名为 `speedprobe_<标签>_<系统>_<架构>.<格式>`，平台与校验方式见[安装](#安装)。打包仅包含指定的公开文档和配置示例，不会包含本地 `.env`、Token 文件、测速日志或开发计划。

本地生成发布包和校验文件（需要 Go、tar、zip 和 SHA-256 工具）：

```sh
# 版本号仅为本地打包示例，不会发布到 GitHub。
make release VERSION=v0.2.0
make release-check
```

产物默认输出到 `dist/`。`make release-check` 使用 Python 3 标准库执行七平台构建、包内容/版本校验及发布流程回归测试。
