# SpeedProbe

自托管的命令行网络测速工具，测量客户端与指定服务器之间的下载速度、上传速度、延迟和抖动。适合运维排查、自建服务和周期性链路监测。

使用 Go 标准库实现，一个二进制文件提供 `serve` 服务端和 `test` 客户端，无需数据库或额外运行时。

[下载](https://github.com/AhsokaTano26/SpeedProbe/releases) · [使用与部署指南](docs/guide.md) · [报告问题](https://github.com/AhsokaTano26/SpeedProbe/issues)

## 功能

- 支持 IPv4 / IPv6、多连接并发测速和自定义测速时长。
- 支持多个 Bearer Token，以及并发、每请求流量和共享带宽限制。
- 支持单次和定时测速，提供终端输出、JSON 和 JSON Lines 日志。
- 提供 Docker Compose 和 Nginx 配置示例，可部署为 Ubuntu systemd 服务。

## 快速开始

从 [Releases](https://github.com/AhsokaTano26/SpeedProbe/releases) 下载并解压对应平台的二进制，无需安装 Go。支持 Linux、macOS 和 Windows；[平台与校验说明](docs/guide.md#安装)。

以下在 Linux / macOS 的解压目录执行，需要 OpenSSL。先启动一个限速的本机测试服务：

```sh
openssl rand -hex 32 > .speedprobe-tokens
chmod 600 .speedprobe-tokens

./speedprobe serve --addr 127.0.0.1:8080 \
  --tokens-file .speedprobe-tokens --bytes-per-second 10485760
```

另开终端，进入同一目录，完成一次测速：

```sh
./speedprobe test --server http://127.0.0.1:8080 \
  --token-file .speedprobe-tokens --connections 2 --duration 1s
```

成功后会显示 `Download`、`Upload`、`Latency` 和 `Jitter`。Windows 使用 `speedprobe.exe`，并按[配置指南](docs/guide.md#命令与配置)准备 Token。

本例将共享带宽限制为 10 MiB/s，用于验证安装。测量实际网络时，在目标服务器运行 `serve`，另一台机器运行 `test`，调整服务地址、带宽限制及测速时长；公网部署使用 HTTPS。详见[部署指南](docs/guide.md#nginx-与-https)。

## 文档

| 需要做什么 | 说明 |
| --- | --- |
| 配置客户端、服务端和 `.env` | [配置与参数](docs/guide.md#命令与配置)、[自动读取 .env](docs/guide.md#客户端自动读取-env) |
| 定时测速、保存或处理结果 | [定时测速与日志](docs/guide.md#定时测速与日志) |
| 部署服务 | [Docker Compose](docs/guide.md#docker-compose)、[Ubuntu / systemd](docs/guide.md#ubuntu-与-systemd)、[Nginx / HTTPS](docs/guide.md#nginx-与-https) |
| 理解结果和限制 | [测速方法](docs/guide.md#测速方法与准确性)、[流量与并发限制](docs/guide.md#流量与并发限制) |
| 集成或排查问题 | [REST API](docs/guide.md#rest-api)、[故障排查](docs/guide.md#故障排查) |
| 从源码构建或参与开发 | [开发与测试](docs/guide.md#开发与测试)、[自动构建与 Release](docs/guide.md#自动构建与-release) |

## 贡献与帮助

问题和建议请提交到 [Issues](https://github.com/AhsokaTano26/SpeedProbe/issues)，欢迎提交 [Pull Request](https://github.com/AhsokaTano26/SpeedProbe/pulls)。报告问题时请附版本、运行环境和脱敏后的命令及错误输出。

从源码构建需要 Go 1.23+。提交代码前运行相关测试和 `make check`，不要提交 Token、私钥或真实 `.env`。

## 许可证

[MIT License](LICENSE)。
