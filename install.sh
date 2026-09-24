#!/bin/sh
# Установка mirea-moodle-mcp (macOS / Linux):
#   curl -fsSL https://raw.githubusercontent.com/1RAFTIK1/mirea-moodle-mcp/main/install.sh | sh
set -e
REPO="1RAFTIK1/mirea-moodle-mcp"
BIN="mirea-moodle-mcp"
DIR="${INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "Неподдерживаемая архитектура: $arch"; exit 1 ;;
esac
case "$os" in darwin|linux) ;; *) echo "Неподдерживаемая ОС: $os (для Windows — install.ps1)"; exit 1 ;; esac

mkdir -p "$DIR"
url="https://github.com/$REPO/releases/latest/download/${BIN}-${os}-${arch}"
echo "→ Скачиваю $url"
if curl -fL --progress-bar -o "$DIR/$BIN.tmp" "$url"; then
  mv "$DIR/$BIN.tmp" "$DIR/$BIN"
elif command -v go >/dev/null 2>&1; then
  rm -f "$DIR/$BIN.tmp"
  echo "→ Готового бинарника нет, собираю из исходников через go install"
  GOBIN="$DIR" go install "github.com/$REPO@latest"
else
  rm -f "$DIR/$BIN.tmp"
  echo "✗ Не удалось скачать бинарник, и Go не установлен."; exit 1
fi
chmod +x "$DIR/$BIN"
echo "✓ Установлено: $DIR/$BIN"

case ":$PATH:" in
  *":$DIR:"*) ;;
  *) echo "  (добавь $DIR в PATH, чтобы вызывать $BIN из любой папки:"
     echo "   echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.zshrc )" ;;
esac

# setup is interactive: read from the terminal even when piped from curl
if [ -r /dev/tty ]; then
  "$DIR/$BIN" setup < /dev/tty
else
  echo "Теперь запусти: $DIR/$BIN setup"
fi
