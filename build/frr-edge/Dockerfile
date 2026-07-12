# frr-edge 在官方 FRR 镜像上补充 iptables,供 dcedge(边界 SNAT)
# 与 external(WAN 口 masquerade)使用。基础镜像版本必须与
# internal/compiler/containerlab.DefaultOptions 的 FRRImage 保持一致。
FROM quay.io/frrouting/frr:10.2.1

RUN apk add --no-cache iptables
