# 局域网 Registry 部署说明

本文档说明如何把 tdx-api 镜像构建并推送到**局域网自建 registry**，然后在目标机器上拉取运行。

- Registry 地址：`192.168.0.201:5000`
- 镜像地址：`192.168.0.201:5000/anchovyprivate/tdx-api-stock-web:latest`
- 认证：免认证（无需 `docker login`）
- 协议：HTTP 明文（需给 Docker 配 `insecure-registries`）

---

## 一、构建机器：配置 insecure-registry（只需一次）

局域网 registry 通常是 HTTP 明文，Docker 默认拒绝，必须先放行。

1. 编辑（或新建）`/etc/docker/daemon.json`：

```bash
sudo tee /etc/docker/daemon.json >/dev/null <<'EOF'
{
  "insecure-registries": ["192.168.0.201:5000"]
}
EOF
```

> ⚠️ 若该文件**已存在**（例如已配置 `registry-mirrors`），**不要整个覆盖**，
> 手动把 `"insecure-registries": ["192.168.0.201:5000"]` 这一项加进现有 JSON 即可。

2. 重启 Docker 生效：

```bash
sudo systemctl daemon-reload
sudo systemctl restart docker
```

3. 验证：

```bash
docker info | grep -A2 "Insecure Registries"
# 应能看到 192.168.0.201:5000
```

---

## 二、构建机器：推送镜像

### 方式一：一键脚本（推荐）

```bash
cd ~/project/tdx-api
./publish-lan.sh
```

脚本会：预检 Docker 与 insecure-registry → 构建（`--provenance=false --sbom=false`）→ 推送。

常用参数：

```bash
./publish-lan.sh --tag v1.0.0     # 指定 tag
./publish-lan.sh --skip-build     # 复用本地已构建镜像，只推送
./publish-lan.sh --help           # 查看帮助
```

### 方式二：docker compose（compose 内已配好 image 名）

`docker-compose.yml` 的服务已带 `image:` 字段，构建即得同名镜像：

```bash
docker compose build     # 构建，镜像名= 192.168.0.201:5000/anchovyprivate/tdx-api-stock-web:latest
docker compose push      # 推送
```

### 方式三：手动

```bash
docker compose build
docker tag tdx-api-stock-web 192.168.0.201:5000/anchovyprivate/tdx-api-stock-web:latest
docker push 192.168.0.201:5000/anchovyprivate/tdx-api-stock-web:latest
```

### 推送失败排查

| 报错 | 原因 | 处理 |
|---|---|---|
| `http: server gave HTTP response to HTTPS client` | 未配 insecure-registry | 执行第一步，重启 Docker |
| `connect: connection refused` | registry 未启动 / 地址端口错 | 确认 `192.168.0.201:5000` 可达：`curl -v http://192.168.0.201:5000/v2/` |
| `dial tcp ... i/o timeout` | 网络不通 / 代理干扰 | 检查网络；确认 Docker daemon 未走失效代理 |

---

## 三、目标机器：拉取并运行

1. **目标机器也要配 insecure-registry**（同上第一步），因为默认拉取同样会用 HTTPS。

2. 拉取镜像：

```bash
docker pull 192.168.0.201:5000/anchovyprivate/tdx-api-stock-web:latest
```

3. 运行（二选一）：

**A. docker run**

```bash
docker run -d \
  --name tdx-stock-web \
  --restart unless-stopped \
  -p 18080:8080 \
  -e TZ=Asia/Shanghai \
  -v $(pwd)/data:/app/data \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  192.168.0.201:5000/anchovyprivate/tdx-api-stock-web:latest
```

**B. docker compose**（推荐，把 `docker-compose.yml` 拷到目标机器）：

`docker-compose.yml` 已带 `image:` 字段，直接：

```bash
docker compose up -d
```

> 若目标机器不想现场构建，可把 compose 里的 `build:` 段删掉，只保留 `image:`，
> 这样 `up -d` 会直接拉取镜像而非重新构建。

4. 访问：

```
http://<目标机器IP>:18080
```

---

## 四、更新镜像（改代码后重新发布）

构建机器：

```bash
git pull
./publish-lan.sh
```

目标机器：

```bash
docker pull 192.168.0.201:5000/anchovyprivate/tdx-api-stock-web:latest
docker compose up -d        # 或 docker compose up -d --force-recreate
```

---

## 五、验证容器启动正常

```bash
docker compose logs | grep -E "连接池大小|已注册|并发度|调度配置"
```

预期看到：

- `调度配置已加载：notify=... excal_concurrency=N`
- `TDX 连接池大小 N`
- 6 行 `任务 xxx 已注册：enabled=... cron=... run_at_start=...`
- `服务启动成功，访问 http://localhost:8080`

---

## 相关文件

| 文件 | 说明 |
|---|---|
| `publish-lan.sh` | 构建并推送到局域网 registry 的一键脚本 |
| `docker-compose.yml` | 含 `image: 192.168.0.201:5000/...`，构建/推送/运行统一用 |
| `publish-aliyun.sh` | 推送到阿里云 registry（另一套，保留备用） |
| `docker-start.sh` | 本机构建并启动（`docker compose up -d --build`） |
