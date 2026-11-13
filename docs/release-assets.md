# CacheOS 发布包与下载安装说明

每个 `v*` 标签会触发兼容构建，并创建一个 GitHub Release。发布页：

<https://github.com/xiaoguan521/cacheos/releases/latest>

当前发布工作流提供数据面 `sycache` 的安装包。控制台、agent 和 wrapper 仍按
[`docs/quickstart.md`](https://github.com/xiaoguan521/cacheos/blob/main/docs/quickstart.md) 中的部署方式安装；它们不属于本工作流的
数据面安装包。

## 发布资产

| 资产 | 用途 |
| --- | --- |
| `sycache-<版本>-x86_64.tar.gz` | x86_64 数据面完整归档 |
| `sycache-<版本>-aarch64.tar.gz` | aarch64 数据面完整归档 |
| `sycache_<版本>-1_amd64.deb` | Debian/Ubuntu 系列安装包 |
| `sycache_<版本>-1_arm64.deb` | Debian/Ubuntu 系列 ARM64 安装包 |
| `sycache-<版本>-1.x86_64.rpm` | RHEL、openEuler 等 x86_64 RPM 安装包 |
| `sycache-<版本>-1.aarch64.rpm` | RHEL、openEuler 等 aarch64 RPM 安装包 |
| `sycache-modules_<版本>-1_amd64.deb` | 可选：扩展数据类型模块包（JSON、搜索 / 向量、布隆过滤器），Debian/Ubuntu x86_64 |
| `sycache-modules-<版本>-1.x86_64.rpm` | 可选：扩展数据类型模块包，RPM 系 x86_64 |
| `sycache-modules-<版本>-x86_64.tar.gz` | 可选：扩展数据类型模块包归档 |
| `MODULES-BUILDINFO-x86_64` | 模块包的构建来源（模块版本、所需 glibc / libstdc++） |
| `sycache-debuginfo-<版本>-<架构>.tar.gz` | 调试符号，故障分析使用，不用于安装 |
| `sycache-<版本>-<架构>.spdx.json` | 对应架构的 SPDX SBOM |
| `BUILDINFO-<架构>` | 对应架构的构建来源与补丁信息 |
| `<版本>.csaf.json` | VEX 记录 |
| `SHA256SUMS` | Release 全部资产的 SHA-256 校验清单 |

其中 `<版本>` 不带 `v`，例如标签 `v0.1.0` 对应版本 `0.1.0`。正式发布包
使用 `-1` 作为 RPM Release 和 DEB 修订号；预发布版本以实际 Release 页面
资产名为准。

## 容器镜像

`v*` 标签还会向 GHCR 推送运行时镜像（x86_64 / aarch64 多架构）：

```bash
docker pull ghcr.io/xiaoguan521/cacheos/sycache:<版本>          # 数据面
docker pull ghcr.io/xiaoguan521/cacheos/sycache-console:<版本>  # 控制台
```

预发布版本不更新 `latest`。使用方式见 [`docs/deployment.md`](https://github.com/xiaoguan521/cacheos/blob/main/docs/deployment.md) 第 6 节。

## 下载并校验

下面以 `v0.1.0`、x86_64 为例。下载 ARM64 时将 `amd64`/`x86_64` 替换为
`arm64`/`aarch64`。

```bash
export VERSION=0.1.0
export TAG="v${VERSION}"
export BASE="https://github.com/xiaoguan521/cacheos/releases/download/${TAG}"

curl -fLO "${BASE}/SHA256SUMS"
curl -fLO "${BASE}/sycache_${VERSION}-1_amd64.deb"
sha256sum -c SHA256SUMS --ignore-missing
```

`sha256sum` 报告 `OK` 后再安装。若只下载一个包，`--ignore-missing` 会忽略
清单中没有下载的其他资产；生产环境建议下载并校验 Release 中的全部文件。

## Debian/Ubuntu 安装

```bash
sudo apt install "./sycache_${VERSION}-1_amd64.deb"
```

## RPM 系安装

```bash
curl -fLO "${BASE}/sycache-${VERSION}-1.x86_64.rpm"
sha256sum -c SHA256SUMS --ignore-missing
sudo dnf install "./sycache-${VERSION}-1.x86_64.rpm"
```

如果目标系统使用 `yum`，将最后一条命令替换为 `sudo yum install ...`。

## tar.gz 安装

tar.gz 适合没有对应包管理器或需要手工控制文件的环境。它会解压到归档内的
`/home/atwasoft/sycache` 路径，并包含 systemd 单元、默认配置、许可证和
构建信息；生产环境优先使用 DEB/RPM，以便获得包管理器的安装与升级记录。

```bash
curl -fLO "${BASE}/sycache-${VERSION}-x86_64.tar.gz"
sha256sum -c SHA256SUMS --ignore-missing
sudo tar -C / -xzf "sycache-${VERSION}-x86_64.tar.gz"
```

实例配置和启动方式见 [`docs/quickstart.md`](https://github.com/xiaoguan521/cacheos/blob/main/docs/quickstart.md) 以及包内的
`/home/atwasoft/sycache/instances` 和 systemd 模板单元。不要直接用 tar.gz
覆盖正在运行的实例；升级前应先按运维流程停止或切换实例并保留回滚包。

## 可选：扩展数据类型模块包

JSON、搜索 / 向量、布隆过滤器三个模块（ADR 0002）作为独立可选包发布，不在 `sycache` 主包里。它们在 Ubuntu 24.04
工具链上构建，**需要 glibc ≥ 2.38 与 libstdc++ ≥ GCC 13.1**（例如 Ubuntu 24.04 及更新、Debian 13 及更新），
CentOS 7、麒麟 V10、统信 UOS、openEuler 等较老的发行版上不能安装；目前只发布 x86_64。

```bash
sudo apt install "./sycache-modules_${VERSION}-1_amd64.deb"   # 依赖同版本以上的 sycache
```

安装后模块文件在 `/home/atwasoft/sycache/modules/`，默认不加载：只有在控制台创建部署单元时勾选“扩展数据类型”，
对应实例才会加载。**卸载本包前须先停用并重建使用模块的实例**，否则它们重启时会因找不到模块文件而启动失败。
包内附各模块与第三方依赖的许可文本（`licenses/modules/`）和 SBOM（`share/modules/`）。

## 控制台与缓存主机部署

控制台安装需要 `sycache-console`，缓存主机安装需要
`sycache-agent` 与 `sycache-wrapper`。当前 Release 工作流只发布数据面
`sycache` 包，不会把这三个控制面二进制伪装成数据面安装包。请按快速开始
中的命令安装：

```bash
sudo deploy/install-console.sh /path/to/sycache-console /path/to/console.env
sudo deploy/install-agent.sh /path/to/sycache-agent /path/to/sycache-wrapper
```

控制台导入版本库时，仍需使用交付流程提供的厂商签名文件（`.sig`）；
`SHA256SUMS` 只证明 Release 下载文件的完整性，不能替代包签名或 License
校验。
