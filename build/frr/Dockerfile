# dcnetlab/frr 是所有交换机/路由器节点的 NOS 镜像:官方 FRR 加上
# 平台内建的抓包工具(相当于 NOS 自带 tcpdump)。镜像 tag 跟随基础
# FRR 版本,必须与 internal/compiler/containerlab.DefaultOptions
# 保持一致。构建上下文为仓库根目录(需要 bin/ 下的静态二进制):
#   docker build -t dcnetlab/frr:10.2.1 -f build/frr/Dockerfile .
FROM quay.io/frrouting/frr:10.2.1

COPY bin/dcnetlab-capture /opt/dcnetlab/bin/capture
RUN ln -sf /opt/dcnetlab/bin/capture /usr/local/bin/capture
