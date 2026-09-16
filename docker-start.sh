#!/bin/bash

echo "========================================"
echo "  TDX股票数据查询系统 - Docker版"
echo "========================================"
echo ""

# 检查Docker是否安装
if ! command -v docker &> /dev/null; then
    echo "[错误] 未检测到Docker，请先安装Docker"
    echo ""
    echo "安装方法: https://docs.docker.com/get-docker/"
    echo ""
    exit 1
fi

echo "[√] Docker已安装"
echo ""

# 检查Docker是否运行
if ! docker ps &> /dev/null; then
    echo "[错误] Docker未运行，请先启动Docker"
    echo ""
    echo "启动命令: sudo systemctl start docker"
    echo ""
    exit 1
fi

echo "[√] Docker正在运行"
echo ""

# 检查 docker compose(v2) 是否可用
if ! docker compose version &> /dev/null; then
    echo "[错误] docker compose 不可用，请先安装 Docker Compose v2"
    echo ""
    echo "安装方法: https://docs.docker.com/compose/install/"
    echo ""
    exit 1
fi

echo "[√] docker compose 可用"
echo ""

echo "----------------------------------------"
echo "正在构建并启动服务（--build，源码改动生效）..."
echo "----------------------------------------"
echo ""

# 构建并启动服务（--build 确保 Go 源码改动重新编译进镜像）
docker compose up -d --build

if [ $? -ne 0 ]; then
    echo ""
    echo "[错误] 启动失败，请查看上面的错误信息"
    echo ""
    exit 1
fi

echo ""
echo "========================================"
echo "  启动成功！"
echo "========================================"
echo ""
echo "访问地址: http://localhost:18080"
echo ""
echo "常用命令:"
echo "  查看日志: docker compose logs -f"
echo "  停止服务: docker compose stop"
echo "  重启服务: docker compose restart"
echo "  完全清理: docker compose down"
echo ""
echo "----------------------------------------"
echo ""

# 等待服务完全启动
sleep 3

# 尝试在浏览器中打开（不同系统）
if command -v xdg-open &> /dev/null; then
    xdg-open http://localhost:18080
elif command -v open &> /dev/null; then
    open http://localhost:18080
else
    echo "请手动在浏览器中打开: http://localhost:18080"
fi

echo "准备就绪！"

