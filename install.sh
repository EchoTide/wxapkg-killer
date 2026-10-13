#!/usr/bin/env sh
# wxapkg-killer 安装脚本（macOS / Linux）
#
#   curl -fsSL https://raw.githubusercontent.com/EchoTide/wxapkg-killer/main/install.sh | sh
#
# 可选环境变量：
#   WXAPKG_REPO      仓库，默认 EchoTide/wxapkg-killer
#   WXAPKG_VERSION   版本号，默认 latest（如 v1.0.0）
#   WXAPKG_BIN_DIR   安装目录，默认 ~/.local/bin
# 可选参数：
#   --no-setup       只下载二进制，不注册到 Agent
#   --bin-dir DIR    同 WXAPKG_BIN_DIR

set -eu

REPO="${WXAPKG_REPO:-EchoTide/wxapkg-killer}"
VERSION="${WXAPKG_VERSION:-latest}"
BIN_DIR="${WXAPKG_BIN_DIR:-$HOME/.local/bin}"
DO_SETUP=1

while [ $# -gt 0 ]; do
  case "$1" in
    --no-setup) DO_SETUP=0 ;;
    --bin-dir) shift; BIN_DIR="${1:-$BIN_DIR}" ;;
    -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
  shift
done

log() { printf '==> %s\n' "$*" >&2; }
die() { printf '\033[0;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ---------- 平台识别 ----------
case "$(uname -s)" in
  Linux|linux)  OS=linux ;;
  Darwin|darwin) OS=darwin ;;
  *) die "不支持的操作系统: $(uname -s)（Windows 请用 install.ps1）" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "不支持的架构: $(uname -m)" ;;
esac

ASSET="wxapkg-killer-$OS-$ARCH"
if [ "$VERSION" = latest ]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi

# ---------- 下载 ----------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

need() { command -v "$1" >/dev/null 2>&1 || die "需要 $1，请先安装"; }

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
else
  die "需要 curl 或 wget"
fi

log "下载 $ASSET ($VERSION)"
ARCHIVE="$TMP/$ASSET.tar.gz"
fetch "$BASE/$ASSET.tar.gz" "$ARCHIVE" || die "下载失败，请检查版本是否存在：$BASE/$ASSET.tar.gz"

# ---------- 校验压缩包（与 Release 的 checksums.txt 一致；没有该文件则跳过） ----------
if fetch "$BASE/checksums.txt" "$TMP/checksums.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    sumcmd() { sha256sum "$1" | awk '{print $1}'; }
  elif command -v shasum >/dev/null 2>&1; then
    sumcmd() { shasum -a 256 "$1" | awk '{print $1}'; }
  else
    sumcmd() { return 0; }
  fi
  if command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; then
    expect="$(awk -v name="${ASSET}.tar.gz" '$2 == name || $2 == "*" name { print $1; exit }' "$TMP/checksums.txt")"
    if [ -z "${expect:-}" ]; then
      die "checksums.txt 中没有 ${ASSET}.tar.gz"
    fi
    actual="$(sumcmd "$ARCHIVE")"
    [ "$expect" = "$actual" ] || die "SHA256 校验不通过（期望 $expect，实际 $actual）"
    log "SHA256 校验通过"
  fi
fi

need tar
tar -xzf "$ARCHIVE" -C "$TMP" || die "解压失败"
BIN="$TMP/$ASSET"
[ -f "$BIN" ] || die "压缩包内未找到 $ASSET"

# ---------- 安装 ----------
mkdir -p "$BIN_DIR"
TARGET="$BIN_DIR/wxapkg-killer"
cp "$BIN" "$TARGET"
chmod +x "$TARGET"
log "已安装到 $TARGET"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) log "提示：$BIN_DIR 不在 PATH 中，请加入 shell 配置："
     log "     export PATH=\"\$PATH:$BIN_DIR\"" ;;
esac

# ---------- 注册到 Agent ----------
if [ "$DO_SETUP" = 1 ]; then
  log "注册到本机 Agent"
  "$TARGET" setup || log "setup 未自动完成，可稍后手动执行：$TARGET setup"
fi

log "完成。运行 $TARGET doctor 可检查状态。"
