#Requires -Version 5.1
<#
.SYNOPSIS
    构建 tdx-api Docker 镜像并推送到阿里云容器镜像服务。

.DESCRIPTION
    在项目根目录执行：构建镜像 -> 登录阿里云 registry -> 推送镜像。
    凭证优先使用 -Username/-Password 参数，未指定时回退到环境变量
    ALIYUN_REGISTRY_USER / ALIYUN_REGISTRY_PASSWORD。

.PARAMETER Tag
    镜像标签，默认 latest。

.PARAMETER Username
    阿里云镜像仓库用户名（一般为主账号名或 RAM 子账号名）。

.PARAMETER Password
    阿里云镜像仓库密码 / 固定密码 / 临时令牌。建议使用 Token 方式。

.PARAMETER SkipLogin
    跳过登录步骤（已在本机 docker login 过时使用）。

.PARAMETER SkipBuild
    跳过构建，仅推送已存在的本地镜像。

.PARAMETER UseBuildx
    使用 docker buildx 构建并直接推送（显式指定 linux/amd64 平台，与 CI 保持一致）。
    默认使用普通 docker build，再 docker push。

.EXAMPLE
    .\publish-aliyun.ps1

.EXAMPLE
    .\publish-aliyun.ps1 -Tag v1.2.0 -Username myname -Password mypass

.EXAMPLE
    $env:ALIYUN_REGISTRY_USER='xxx'; $env:ALIYUN_REGISTRY_PASSWORD='yyy'; .\publish-aliyun.ps1
#>
[CmdletBinding()]
param(
    [string]$Tag = 'latest',
    [string]$Username = $env:ALIYUN_REGISTRY_USER,
    [string]$Password = $env:ALIYUN_REGISTRY_PASSWORD,
    [switch]$SkipLogin,
    [switch]$SkipBuild,
    [switch]$UseBuildx
)

$ErrorActionPreference = 'Stop'

# ---------------- 配置 ----------------
$Registry = 'registry.cn-chengdu.aliyuncs.com'
$Namespace = 'anchovyprivate'
$Repo = 'tdx-api-stock-web'
$ImageName = "$Registry/$Namespace/${Repo}:$Tag"
$Dockerfile = 'Dockerfile'

# ---------------- 工具函数 ----------------
function Write-Step {
    param([string]$Message)
    Write-Host "`n[STEP] $Message" -ForegroundColor Cyan
}

function Write-Ok {
    param([string]$Message)
    Write-Host "[ OK ] $Message" -ForegroundColor Green
}

function Write-Warn {
    param([string]$Message)
    Write-Host "[WARN] $Message" -ForegroundColor Yellow
}

function Write-Fail {
    param([string]$Message)
    Write-Host "[FAIL] $Message" -ForegroundColor Red
}

function Assert-LastExitCode {
    param([string]$Action)
    if ($LASTEXITCODE -ne 0) {
        Write-Fail "$Action 失败（退出码 $LASTEXITCODE）"
        exit $LASTEXITCODE
    }
}

<#
.SYNOPSIS
检测本机是否已登录目标 registry。

说明：
  1. 若配置了 credsStore / credHelpers（如 Docker Desktop 的 desktop.exe），
     凭证由系统钥匙串管理，config.json 中的 auth 字段为空，无法直接读取，
     但只要 registry 出现在 auths 中，即认为已由凭据助手托管，可直接推送。
  2. 若 auths 中存在该 registry 且带 auth / identitytoken 字段，视为已登录。
#>
function Test-RegistryLogin {
    param([string]$Registry)

    $configPath = if ($env:DOCKER_CONFIG) {
        Join-Path $env:DOCKER_CONFIG 'config.json'
    }
    else {
        Join-Path $HOME '.docker/config.json'
    }

    if (-not (Test-Path -LiteralPath $configPath)) {
        return $false
    }

    try {
        $cfg = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    }
    catch {
        return $false
    }

    $auths = $cfg.auths
    if (-not $auths) { return $false }

    # 情形 2：auths 中直接带有凭据字段
    $entry = $auths.PSObject.Properties[$Registry]
    if ($entry -and $entry.Value) {
        $v = $entry.Value
        if ($v.auth -or $v.identitytoken) {
            return $true
        }
        # 情形 1：条目存在但凭证为空 -> 可能由 credsStore / credHelpers 托管
        if ($cfg.credsStore -or $cfg.credHelpers) {
            return $true
        }
    }

    return $false
}

# ---------------- 前置检查 ----------------
Write-Step '前置检查'

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Fail '未找到 docker 命令，请先安装 Docker Desktop 并确保已加入 PATH。'
    exit 1
}

docker version --format '{{.Server.Version}}' *> $null
if ($LASTEXITCODE -ne 0) {
    Write-Fail 'Docker 守护进程未运行或无法连接，请启动 Docker Desktop。'
    exit 1
}
Write-Ok 'Docker 可用'

# 切换到项目根目录（脚本所在目录）
Set-Location -Path $PSScriptRoot
if (-not (Test-Path -LiteralPath $Dockerfile)) {
    Write-Fail "当前目录未找到 $Dockerfile（$PSScriptRoot）"
    exit 1
}
Write-Ok "工作目录: $PSScriptRoot"

Write-Host "  目标镜像: $ImageName" -ForegroundColor Gray
Write-Host "  平台:     linux/amd64" -ForegroundColor Gray

# ---------------- 登录 ----------------
$alreadyLoggedIn = Test-RegistryLogin -Registry $Registry

if ($SkipLogin) {
    Write-Step '跳过登录（-SkipLogin）'
}
elseif ($alreadyLoggedIn) {
    Write-Step '登录阿里云镜像仓库'
    # 凭证由 Docker Desktop 凭据助手（credsStore）托管，或 config.json 中已有有效凭据
    Write-Host "  检测到本机已登录 $Registry，跳过登录" -ForegroundColor Gray
    Write-Ok '复用已有登录状态'
}
else {
    Write-Step '登录阿里云镜像仓库'
    if ([string]::IsNullOrWhiteSpace($Username) -or [string]::IsNullOrWhiteSpace($Password)) {
        Write-Fail '缺少凭证：请使用 -Username/-Password 参数，'
        Write-Host '       或设置环境变量 ALIYUN_REGISTRY_USER / ALIYUN_REGISTRY_PASSWORD；' -ForegroundColor Yellow
        Write-Host '       若本机已 docker login 过但未被识别，可加 -SkipLogin 强制跳过。' -ForegroundColor Yellow
        exit 1
    }
    Write-Host "  用户名: $Username" -ForegroundColor Gray

    # 通过 stdin 传递密码，避免出现在进程参数中
    $Password | docker login $Registry --username $Username --password-stdin
    Assert-LastExitCode '登录'
    Write-Ok '登录成功'
}

# ---------------- 构建 ----------------
if ($SkipBuild) {
    Write-Step '跳过构建（-SkipBuild）'
}
elseif ($UseBuildx) {
    Write-Step '构建并推送（docker buildx, linux/amd64）'
    docker buildx build --platform linux/amd64 --tag $ImageName --push --file $Dockerfile .
    Assert-LastExitCode 'buildx 构建并推送'
    Write-Ok '构建并推送完成'
}
else {
    Write-Step '构建镜像'
    # Dockerfile 内已固定 GOOS=linux / GOARCH=amd64，产物即为 linux/amd64
    docker build --tag $ImageName --file $Dockerfile .
    Assert-LastExitCode '构建'

    $size = docker image inspect $ImageName --format '{{.Size}}'
    if ($size) {
        Write-Ok "构建完成（镜像大小: {0:N1} MB）" -f ($size / 1MB)
    }
    else {
        Write-Ok '构建完成'
    }
}

# ---------------- 推送 ----------------
if (-not $UseBuildx) {
    Write-Step '推送镜像'
    docker push $ImageName
    Assert-LastExitCode '推送'
    Write-Ok '推送完成'
}

# ---------------- 结束 ----------------
Write-Host "`n=====================================" -ForegroundColor Green
Write-Host " 发布成功" -ForegroundColor Green
Write-Host "=====================================" -ForegroundColor Green
Write-Host " 镜像: $ImageName"
Write-Host "`n 拉取命令:"
Write-Host "   docker pull $ImageName" -ForegroundColor Gray
Write-Host "`n 运行命令:"
Write-Host "   docker run -d -p 8080:8080 -v `${PWD}/data:/app/data $ImageName" -ForegroundColor Gray
Write-Host ""
