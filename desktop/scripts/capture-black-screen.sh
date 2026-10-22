#!/bin/bash
# Snapshot of a running HarmFlow instance whose window is black: the shell, the WebKit helpers it started (matched
# by start time), its sockets to the daemon, and the WebKit data files touched in the last 10 minutes.
# Usage: capture-black-screen.sh [out-dir]
OUT=${1:-$HOME/harmflow-black-$(date +%Y%m%d-%H%M%S)}
mkdir -p "$OUT"
pid=$(pgrep -f 'HarmFlow.app/Contents/MacOS/harmflow-desktop' | head -1)
if [ -z "$pid" ]; then
  echo "HarmFlow is not running"
  exit 1
fi
epoch() { date -j -f '%a %b %d %T %Y' "$(ps -o lstart= -p "$1" | sed 's/  */ /g; s/^ //; s/ $//')" +%s; }
start=$(epoch "$pid")
ps -o pid,lstart,etime,%cpu,rss,command -p "$pid" > "$OUT/shell.txt"
sample "$pid" 2 -file "$OUT/sample-shell-$pid.txt" >/dev/null 2>&1
for p in $(pgrep -f 'com\.apple\.WebKit\.(WebContent|Networking|GPU)'); do
  d=$(( $(epoch "$p") - start ))
  if [ "$d" -ge -1 ] && [ "$d" -le 15 ]; then
    name=$(ps -o comm= -p "$p" | sed 's#.*/##')
    ps -o pid,lstart,etime,%cpu,rss,command -p "$p" | tail -1 >> "$OUT/helpers.txt"
    sample "$p" 2 -file "$OUT/sample-$name-$p.txt" >/dev/null 2>&1
  fi
done
lsof -nP -a -p "$pid" -iTCP > "$OUT/sockets.txt" 2>&1
find ~/Library/WebKit/com.harmflow.desktop ~/Library/Caches/com.harmflow.desktop -mmin -10 -exec stat -f '%Sm %N' {} \; > "$OUT/webkit-files.txt" 2>&1
vm_stat > "$OUT/vm_stat.txt"
uptime > "$OUT/uptime.txt"
echo "$OUT"
