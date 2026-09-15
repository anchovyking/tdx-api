# 多阶段构建 - 第一阶段：构建
# 使用阿里云私有仓库的golang镜像（与运行镜像同源,避免拉取docker.io超时）
FROM registry.cn-chengdu.aliyuncs.com/anchovypublic/golang:1.22 AS builder

# 替换Alpine镜像源为阿里云(若基础镜像非alpine则跳过)
RUN (test -f /etc/apk/repositories && sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories) || true

# 设置工作目录
WORKDIR /app

# 设置Go代理（使用国内镜像加速）
ENV GO111MODULE=on \
    GOPROXY=https://goproxy.cn,https://mirrors.aliyun.com/goproxy/,direct \
    GOTOOLCHAIN=auto \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# 复制 Go 模块文件
COPY go.mod go.sum ./
RUN go mod download

# 复制整个项目的源代码
COPY . .

# 在子 shell 中编译,避免模块路径混淆问题; -buildvcs=false 避免git目录权限导致的VCS报错
# 注意:不要在这里跑 go mod tidy,它会下载依赖的测试包,网络一抖就崩;go.mod/go.sum 由开发机维护
RUN (cd web && go build -mod=mod -buildvcs=false -ldflags="-s -w" -o ../stock-web .)

# 多阶段构建 - 第二阶段：运行
FROM registry.cn-chengdu.aliyuncs.com/anchovypublic/golang:1.22

# 安装必要的运行时依赖(Debian基础镜像,已自带ca-certificates/tzdata/wget时跳过)
RUN (command -v wget >/dev/null 2>&1 && command -v tzdata >/dev/null 2>&1) || \
    (apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata wget && rm -rf /var/lib/apt/lists/*) || \
    echo "dependencies already present, skipped"

# 设置时区为上海
ENV TZ=Asia/Shanghai

# 创建非root用户(Debian语法)
RUN groupadd -g 1000 appuser && \
    useradd -m -u 1000 -g appuser appuser

# 设置工作目录
WORKDIR /app

# ===================================================================
# 【语法修正】
# 从构建阶段复制编译好的二进制文件
COPY --from=builder /app/stock-web .
# ===================================================================

# ===================================================================
# 【语法修正】
# 复制静态文件
COPY --from=builder /app/web/static ./static
# ===================================================================

# 更改文件所有者
RUN chown -R appuser:appuser /app

# 切换到非root用户
USER appuser

# 暴露端口
EXPOSE 8080

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/health || exit 1

# 启动应用
CMD ["./stock-web"]