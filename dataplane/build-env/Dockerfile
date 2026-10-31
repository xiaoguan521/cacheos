# sycache 兼容构建环境：glibc 2.17（CentOS 7 / manylinux2014）+ devtoolset GCC。
# 产物只依赖 glibc ≥ 2.17 与 libsystemd.so.0，覆盖 CentOS 7、麒麟 V10、统信 UOS、openEuler 及更新的发行版。
#
# 构建镜像：docker build -t cacheos-build:el7 --build-arg ARCH=x86_64 dataplane/build-env
#          （aarch64 主机上用 --build-arg ARCH=aarch64）
# 使用方式见 dataplane/build-compat.sh。
ARG ARCH=x86_64
FROM quay.io/pypa/manylinux2014_${ARCH}

# perl-IPC-Cmd 等：OpenSSL 3.x 的 Configure 需要；systemd-devel：sd_notify（Type=notify）；rpm-build：打 RPM 包
RUN yum install -y -q perl-IPC-Cmd perl-Data-Dumper perl-Time-Piece systemd-devel rpm-build which patch \
 && yum clean all

# gen-sbom.py 需要 python3：使用镜像自带的 CPython 3.12
RUN ln -sf /opt/python/cp312-cp312/bin/python3 /usr/local/bin/python3 && python3 --version

# manylinux2014 的 devtoolset 已在 PATH 中；git 用于拉取上游源码与生成可复现的源码树 sha
ENV MAX_GLIBC=2.17 USE_SYSTEMD=yes
WORKDIR /work
