# dcnetlab/server 是 server 节点的镜像:FRR(服务器与机架 leaf 说
# BGP)加上进程管理的 node-agent、操作员 CLI 与 mtr(路径诊断)。
# 抓包工具不烤进镜像,由 controller 在初始化时通过包制品库下发安
# 装,以此验证 pkg 分发通道。构建上下文为仓库根目录:
#   docker build -t dcnetlab/server:10.2.1 -f build/server/Dockerfile .
FROM quay.io/frrouting/frr:10.2.1

RUN apk add --no-cache mtr

COPY bin/dcnetlab-node-agent /opt/dcnetlab/bin/node-agent
COPY bin/dcnetlab-node-cli /opt/dcnetlab/bin/node-cli
RUN ln -sf /opt/dcnetlab/bin/node-agent /usr/local/bin/node-agent \
    && ln -sf /opt/dcnetlab/bin/node-cli /usr/local/bin/node-cli \
    && ln -sf /opt/dcnetlab/bin/node-cli /usr/local/bin/pkg \
    && ln -sf /opt/dcnetlab/bin/node-cli /usr/local/bin/program
