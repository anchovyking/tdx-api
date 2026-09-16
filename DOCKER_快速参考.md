# 🐳 Docker快速参考卡

## 🚀 一键启动

```bash
chmod +x docker-start.sh
./docker-start.sh
```

---

## 📝 常用命令

### 启动服务
```powershell
docker-compose up -d
```

### 停止服务
```powershell
docker-compose stop
```

### 重启服务
```powershell
docker-compose restart
```

### 查看日志
```powershell
# 实时查看
docker-compose logs -f

# 最近100行
docker-compose logs --tail=100
```

### 查看状态
```powershell
# 查看容器状态
docker-compose ps

# 查看资源使用
docker stats tdx-stock-web
```

### 完全清理
```powershell
# 停止并删除容器
docker-compose down

# 同时删除镜像
docker-compose down --rmi all
```

---

## 🔧 故障排查

### 查看容器日志
```powershell
docker logs tdx-stock-web
docker logs -f tdx-stock-web  # 实时查看
```

### 进入容器
```powershell
docker exec -it tdx-stock-web sh
```

### 重新构建
```powershell
docker-compose up -d --build
```

### 检查端口
```powershell
# Windows
netstat -ano | findstr :8080

# Linux
netstat -tulpn | grep :8080
```

### 清理Docker系统
```powershell
# 清理未使用的容器和镜像
docker system prune

# 清理所有（谨慎使用）
docker system prune -a
```

---

## 📊 监控命令

### 实时资源监控
```powershell
docker stats
```

### 查看容器进程
```powershell
docker top tdx-stock-web
```

### 查看容器详情
```powershell
docker inspect tdx-stock-web
```

### 健康检查
```powershell
docker ps  # 查看HEALTH列
```

---

## 🌐 访问地址

- **本地访问**: http://localhost:8080
- **局域网访问**: http://你的IP:8080

---

## ⚙️ 配置修改

### 修改端口（docker-compose.yml）
```yaml
ports:
  - "9090:8080"  # 将8080改为9090
```

### 修改时区（docker-compose.yml）
```yaml
environment:
  - TZ=Asia/Shanghai  # 修改为你的时区
```

---

## 🔄 更新流程

```powershell
# 1. 停止服务
docker-compose down

# 2. 拉取最新代码（如使用Git）
git pull

# 3. 重新构建并启动
docker-compose up -d --build
```

---

## 📦 备份还原

### 导出镜像
```powershell
docker save -o stock-web.tar tdx-stock-web:latest
```

### 导入镜像
```powershell
docker load -i stock-web.tar
```

### 导出容器
```powershell
docker export tdx-stock-web > stock-web-container.tar
```

---

## 🎯 快速检查

### 服务正常运行的标志

1. ✅ 容器状态为 `Up`
```powershell
docker ps
```

2. ✅ 健康检查为 `healthy`
```powershell
docker ps  # 查看STATUS列
```

3. ✅ 日志无错误
```powershell
docker-compose logs | findstr "error"  # 应该无结果
```

4. ✅ 可以访问网页
```powershell
# 浏览器打开: http://localhost:8080
```

---

## 🆘 紧急处理

### 服务无响应
```powershell
docker-compose restart
```

### 端口冲突
```powershell
# 修改docker-compose.yml中的端口
# 或停止占用端口的程序
netstat -ano | findstr :8080
taskkill /PID <进程ID> /F
```

### 重新部署
```powershell
docker-compose down
docker-compose up -d --build
```

### 完全重置
```powershell
docker-compose down --rmi all --volumes
docker-compose up -d --build
```

---

## 📚 相关文档

- **详细部署指南**: `DOCKER_DEPLOY.md`
- **使用说明**: `web/USAGE.md`
- **快速演示**: `web/DEMO.md`
- **项目总结**: `PROJECT_SUMMARY.md`

---

## 💡 小技巧

### 查看构建过程
```powershell
docker-compose build --progress=plain
```

### 不使用缓存重建
```powershell
docker-compose build --no-cache
```

### 后台运行并查看日志
```powershell
docker-compose up -d && docker-compose logs -f
```

### 停止所有容器
```powershell
docker stop $(docker ps -aq)
```

### 删除所有容器
```powershell
docker rm $(docker ps -aq)
```

---

## 🎉 成功标志

当看到以下输出，表示成功：

```
Creating network "tdx-master_stock-network" with driver "bridge"
Creating tdx-stock-web ... done

访问地址: http://localhost:8080
```

浏览器能够正常打开页面并查看股票数据！

---

**保存此文档以便快速查阅！** 📌

