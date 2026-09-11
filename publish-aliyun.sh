#!/usr/bin/env bash
#
# 构建 tdx-api Docker 镜像并推送到阿里云容器镜像服务。
#
# 用法:
#   ./publish-aliyun.sh [--tag TAG] [--username USER --password PASS]
#                       [--skip-login] [--skip-build] [--use-buildx]
#
# 凭证优先使用 --username/--password 参数，未指定时回退到环境变量
# ALIYUN_REGISTRY_USER / ALIYUN_REGISTRY_PASSWORD。
#
# 示例:
#   ./publish-aliyun.sh
#   ./publish-aliyun.sh --tag v1.2.0 --username myname --password mypass
#   ALIYUN_REGISTRY_USER=xxx ALIYUN_REGISTRY_PASSWORD=yyy ./publish-aliyun.sh
#
set -euo pipefail

# ---------------- 配置 ----------------
REGISTRY='registry.cn-chengdu.aliyuncs.com'
NAMESPACE='anchovyprivate'
REPO='tdx-api-stock-web'
DOCKERFILE='Dockerfile'

# ---------------- 默认参数 ----------------
TAG='latest'
USERNAME="${ALIYUN_REGISTRY_USER:-}"
PASSWORD="${ALIYUN_REGISTRY_PASSWORD:-}"
SKIP_LOGIN=0
SKIP_BUILD=0
USE_BUILDX=0

# ---------------- 参数解析 ----------------
usage() {
    sed -n '3,16p' "$0" | sed 's/^# \{0,1\}//'
    exit "${1:-0}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --tag|-t)       TAG="$2"; shift 2 ;;
        --username|-u)  USERNAME="$2"; shift 2 ;;
        --password|-p)  PASSWORD="$2"; shift 2 ;;
        --skip-login)   SKIP_LOGIN=1; shift ;;
        --skip-build)   SKIP_BUILD=1; shift ;;
        --use-buildx)   USE_BUILDX=1; shift ;;
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

# 检测本机是否已登录目标 registry（docker config.json 中是否有该 registry 的凭据）
test_registry_login() {
    local registry="$1"
    local config_path
    if [ -n "${DOCKER_CONFIG:-}" ]; then
        config_path="$DOCKER_CONFIG/config.json"
    else
        config_path="${HOME}/.docker/config.json"
    fi

    [ -f "$config_path" ] || return 1

    # 用 python 解析（docker config.json 是合法 JSON）
    command -v python3 >/dev/null 2>&1 || return 1
    python3 - "$config_path" "$registry" << 'PYEOF'
import json, sys
try:
    cfg = json.load(open(sys.argv[1]))
except Exception:
    sys.exit(1)
reg = sys.argv[2]
auths = cfg.get("auths") or {}
entry = auths.get(reg)
if isinstance(entry, dict):
    if entry.get("auth") or entry.get("identitytoken"):
        sys.exit(0)
    # 条目存在但凭证为空 -> 可能由 credsStore / credHelpers 托管
    if cfg.get("credsStore") or cfg.get("credHelpers"):
        sys.exit(0)
sys.exit(1)
PYEOF
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

# ---------------- 登录 ----------------
if [ "$SKIP_LOGIN" -eq 1 ]; then
    write_step '跳过登录（--skip-login）'
elif test_registry_login "$REGISTRY"; then
    write_step '登录阿里云镜像仓库'
    write_info "检测到本机已登录 $REGISTRY，跳过登录"
    write_ok '复用已有登录状态'
else
    write_step '登录阿里云镜像仓库'
    if [ -z "$USERNAME" ] || [ -z "$PASSWORD" ]; then
        write_fail '缺少凭证：请使用 --username/--password 参数，'
        write_warn '       或设置环境变量 ALIYUN_REGISTRY_USER / ALIYUN_REGISTRY_PASSWORD；'
        write_warn '       若本机已 docker login 过但未被识别，可加 --skip-login 强制跳过。'
        exit 1
    fi
    write_info "用户名: $USERNAME"
    # 通过 stdin 传递密码，避免出现在进程参数中
    printf '%s' "$PASSWORD" | docker login "$REGISTRY" --username "$USERNAME" --password-stdin \
        || die '登录失败'
    write_ok '登录成功'
fi

# ---------------- 构建 ----------------
if [ "$SKIP_BUILD" -eq 1 ]; then
    write_step '跳过构建（--skip-build）'
elif [ "$USE_BUILDX" -eq 1 ]; then
    write_step '构建并推送（docker buildx, linux/amd64）'
    docker buildx build --platform linux/amd64 --provenance=false --sbom=false \
        --tag "$IMAGE_NAME" --push --file "$DOCKERFILE" . \
        || die 'buildx 构建并推送失败'
    write_ok '构建并推送完成'
else
    write_step '构建镜像'
    # Dockerfile 内已固定 GOOS=linux / GOARCH=amd64，产物即为 linux/amd64
    # --provenance=false --sbom=false: 禁用 attestation,产出 Docker v2 格式
    # (否则新版BuildKit会生成 OCI 空层 manifest,阿里云registry报
    #  "unknown manifest class for application/vnd.oci.empty.v1+json")
    docker build --provenance=false --sbom=false --tag "$IMAGE_NAME" --file "$DOCKERFILE" . || die '构建失败'
    img_size=$(docker image inspect "$IMAGE_NAME" --format '{{.Size}}' 2>/dev/null || true)
    if [ -n "$img_size" ]; then
        write_ok "构建完成（镜像大小: $((img_size / 1024 / 1024)) MB）"
    else
        write_ok '构建完成'
    fi
fi

# ---------------- 推送 ----------------
if [ "$USE_BUILDX" -eq 0 ]; then
    write_step '推送镜像'
    docker push "$IMAGE_NAME" || die '推送失败'
    write_ok '推送完成'
fi

# ---------------- 结束 ----------------
printf '\n=====================================\n'
printf ' 发布成功\n'
printf '=====================================\n'
printf ' 镜像: %s\n' "$IMAGE_NAME"
printf '\n 拉取命令:\n'
printf '   docker pull %s\n' "$IMAGE_NAME"
printf '\n 运行命令:\n'
printf '   docker run -d -p 8080:8080 -v $(pwd)/data:/app/data %s\n\n' "$IMAGE_NAME"
