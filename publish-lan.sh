#!/usr/bin/env bash
#
# 构建 tdx-api Docker 镜像并推送到局域网 registry。
#
# 目标: 192.168.0.201:5000/anchovyprivate/tdx-api-stock-web:latest
#
# 用法:
#   ./publish-lan.sh [--tag TAG] [--skip-build]
#
# 示例:
#   ./publish-lan.sh
#   ./publish-lan.sh --tag v1.2.0
#   ./publish-lan.sh --skip-build      # 复用本地已构建的同名镜像，只推送
#
# 前置条件:
#   1) registry 192.168.0.201:5000 免认证（无需 docker login）
#   2) Docker 已允许该 registry 使用明文 HTTP，即 /etc/docker/daemon.json 内:
#        { "insecure-registries": ["192.168.0.201:5000"] }
#      改完需 sudo systemctl restart docker 生效。
#
set -euo pipefail

# ---------------- 配置 ----------------
REGISTRY='192.168.0.201:5000'
NAMESPACE='anchovyprivate'
REPO='tdx-api-stock-web'
DOCKERFILE='Dockerfile'

# ---------------- 默认参数 ----------------
TAG='latest'
SKIP_BUILD=0

# ---------------- 参数解析 ----------------
usage() {
    sed -n '3,20p' "$0" | sed 's/^# \{0,1\}//'
    exit "${1:-0}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --tag|-t)       TAG="$2"; shift 2 ;;
        --skip-build)   SKIP_BUILD=1; shift ;;
        --help|-h)      usage 0 ;;
        *) echo "未知参数: $1" >&2; usage 1 ;;
    esac
done

IMAGE_NAME="$REGISTRY/$NAMESPACE/$REPO:$TAG"

# ---------------- 工具函数 ----------------
write_step() { printf '\n[STEP] %s\n' "$1"; }
write_ok()   { printf '[ OK ] %s\n' "$1"; }
write_warn() { printf '[WARN] %s\n' "$1"; }
write_fail() { printf '[FAIL] %s\n' "$1" >&2; }
write_info() { printf '  %s\n' "$1"; }

die() { write_fail "$1"; exit 1; }

# 检测 Docker daemon 是否已把该 registry 加入 insecure-registries
test_insecure_registry() {
    local registry="$1"
    docker info --format '{{json .RegistryConfig.InsecureRegistryCIDRs}}' >/dev/null 2>&1 || return 1
    # 直接翻 daemon 配置更可靠（docker info 不一定暴露全部）
    if [ -f /etc/docker/daemon.json ]; then
        command -v python3 >/dev/null 2>&1 || return 1
        python3 - "$registry" << 'PYEOF'
import json, sys
try:
    cfg = json.load(open("/etc/docker/daemon.json"))
except Exception:
    sys.exit(1)
reg = sys.argv[1]
for item in (cfg.get("insecure-registries") or []):
    # 支持带/不带 http:// 前缀的写法
    if item.replace("http://", "").replace("https://", "").rstrip("/") == reg:
        sys.exit(0)
sys.exit(1)
PYEOF
        return $?
    fi
    return 1
}

# ---------------- 前置检查 ----------------
write_step '前置检查'

command -v docker >/dev/null 2>&1 || die '未找到 docker 命令，请先安装 Docker。'

if ! docker version --format '{{.Server.Version}}' >/dev/null 2>&1; then
    die 'Docker 守护进程未运行或无法连接，请启动 Docker。'
fi
write_ok 'Docker 可用'

# 切换到脚本所在目录（项目根）
cd "$(cd "$(dirname "$0")" && pwd)"
[ -f "$DOCKERFILE" ] || die "当前目录未找到 $DOCKERFILE（$(pwd)）"
write_ok "工作目录: $(pwd)"

write_info "目标镜像: $IMAGE_NAME"
write_info "平台:     linux/amd64"

# 局域网 registry 多为明文 HTTP，Docker 默认拒绝；提前提示而不是等到 push 才报错
if test_insecure_registry "$REGISTRY"; then
    write_ok "已配置 insecure-registries: $REGISTRY"
else
    write_warn "未检测到 $REGISTRY 已在 insecure-registries 中。"
    write_warn "若推送报 'http: server gave HTTP response to HTTPS client'，请添加:"
    write_warn "  /etc/docker/daemon.json -> {\"insecure-registries\": [\"$REGISTRY\"]}"
    write_warn "  然后 sudo systemctl restart docker"
fi

# ---------------- 构建 ----------------
if [ "$SKIP_BUILD" -eq 1 ]; then
    write_step '跳过构建（--skip-build）'
    docker image inspect "$IMAGE_NAME" >/dev/null 2>&1 || die "本地不存在镜像 $IMAGE_NAME，无法跳过构建"
    write_ok '使用本地已有镜像'
else
    write_step '构建镜像'
    # Dockerfile 内已固定 GOOS=linux / GOARCH=amd64，产物即为 linux/amd64
    # --provenance=false --sbom=false: 禁用 attestation，产出 Docker v2 格式
    # （自建 registry 对 OCI 空层 manifest 兼容性差，禁用更稳）
    docker build --provenance=false --sbom=false --tag "$IMAGE_NAME" --file "$DOCKERFILE" . || die '构建失败'
    img_size=$(docker image inspect "$IMAGE_NAME" --format '{{.Size}}' 2>/dev/null || true)
    if [ -n "$img_size" ]; then
        write_ok "构建完成（镜像大小: $((img_size / 1024 / 1024)) MB）"
    else
        write_ok '构建完成'
    fi
fi

# ---------------- 推送 ----------------
write_step '推送镜像'
docker push "$IMAGE_NAME" || die '推送失败（若为 http 明文仓库，请先配置 insecure-registries 并重启 docker）'
write_ok '推送完成'

# ---------------- 结束 ----------------
printf '\n=====================================\n'
printf ' 发布成功\n'
printf '=====================================\n'
printf ' 镜像: %s\n' "$IMAGE_NAME"
printf '\n 拉取命令:\n'
printf '   docker pull %s\n' "$IMAGE_NAME"
printf '\n 运行命令:\n'
printf '   docker run -d -p 18080:8080 -v $(pwd)/data:/app/data %s\n\n' "$IMAGE_NAME"
