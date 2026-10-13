#Requires -Version 5.1
<#
.SYNOPSIS
    wxapkg-killer 安装脚本（Windows）

.DESCRIPTION
    irm https://raw.githubusercontent.com/EchoTide/wxapkg-killer/main/install.ps1 | iex

    参数：
      -Version   版本号，默认 latest
      -BinDir    安装目录，默认 %LOCALAPPDATA%\wxapkg-killer\bin
      -NoSetup   只下载二进制，不注册到 Agent
#>
[CmdletBinding()]
param(
    [string]$Version = 'latest',
    [string]$BinDir = (Join-Path $env:LOCALAPPDATA 'wxapkg-killer\bin'),
    [switch]$NoSetup
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$Repo = if ($env:WXAPKG_REPO) { $env:WXAPKG_REPO } else { 'EchoTide/wxapkg-killer' }

function Write-Step { param($Message) Write-Host "==> $Message" -ForegroundColor Cyan }
function Fail { param($Message) Write-Host "error: $Message" -ForegroundColor Red; exit 1 }

# ---------- 架构识别 ----------
switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { $Arch = 'amd64' }
    'ARM64' { $Arch = 'arm64' }
    default { Fail "不支持的架构: $env:PROCESSOR_ARCHITECTURE" }
}
$Asset = "wxapkg-killer-windows-$Arch"

if ($Version -eq 'latest') {
    $Base = "https://github.com/$Repo/releases/latest/download"
}
else {
    $Base = "https://github.com/$Repo/releases/download/$Version"
}

# ---------- 下载 ----------
$Tmp = Join-Path $env:TEMP ("wxapkg-killer-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null
try {
    $Zip = Join-Path $Tmp "$Asset.zip"
    Write-Step "下载 $Asset ($Version)"
    try {
        Invoke-WebRequest -Uri "$Base/$Asset.zip" -OutFile $Zip -UseBasicParsing
    }
    catch {
        Fail "下载失败，请检查版本是否存在：$Base/$Asset.zip"
    }

    # ---------- 校验压缩包（与 Release 的 checksums.txt 一致；没有该文件则跳过） ----------
    $SumFile = Join-Path $Tmp 'checksums.txt'
    try {
        Invoke-WebRequest -Uri "$Base/checksums.txt" -OutFile $SumFile -UseBasicParsing
        $archiveName = "$Asset.zip"
        # sha256sum 默认是「哈希 + 两个空格 + 文件名」，不能只吃一个空白
        $line = Get-Content $SumFile | Where-Object {
            $parts = @($_ -split '\s+' | Where-Object { $_ -ne '' })
            $parts.Count -ge 2 -and ($parts[1] -eq $archiveName -or $parts[1] -eq "*$archiveName")
        } | Select-Object -First 1
        if (-not $line) { Fail "checksums.txt 中没有 $archiveName" }
        $expected = (($line -split '\s+')[0]).ToLower()
        $actual = (Get-FileHash -Path $Zip -Algorithm SHA256).Hash.ToLower()
        if ($expected -ne $actual) { Fail "SHA256 校验不通过（期望 $expected，实际 $actual）" }
        Write-Step 'SHA256 校验通过'
    }
    catch {
        if ($_.Exception.Message -match 'SHA256 校验不通过') { throw }
    }

    Write-Step '解压'
    Expand-Archive -Path $Zip -DestinationPath $Tmp -Force
    $Bin = Join-Path $Tmp "$Asset.exe"
    if (-not (Test-Path $Bin)) { Fail "压缩包内未找到 $Asset.exe" }

    # ---------- 安装 ----------
    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    $Target = Join-Path $BinDir 'wxapkg-killer.exe'
    Copy-Item -Path $Bin -Destination $Target -Force
    Write-Step "已安装到 $Target"

    # ---------- PATH ----------
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($userPath -notlike "*$BinDir*") {
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$BinDir", 'User')
        $env:Path = "$env:Path;$BinDir"
        Write-Step "已把 $BinDir 加入用户 PATH（新窗口生效）"
    }

    # ---------- 注册到 Agent ----------
    if (-not $NoSetup) {
        Write-Step '注册到本机 Agent'
        & $Target setup
        if ($LASTEXITCODE -ne 0) { Write-Host "setup 未自动完成，可稍后手动执行：$Target setup" -ForegroundColor Yellow }
    }
}
finally {
    Remove-Item -Path $Tmp -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host '==> 完成。运行 wxapkg-killer doctor 可检查状态。' -ForegroundColor Green
