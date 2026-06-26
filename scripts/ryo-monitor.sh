#!/usr/bin/env bash
set -u

OUT="${RYO_MONITOR_STATUS_FILE:-/opt/ryo-monitor/app/status.json}"
IFACE="${RYO_MONITOR_IFACE:-eth0}"

mkdir -p "$(dirname "$OUT")"

while true; do
  rx1=$(cat "/sys/class/net/$IFACE/statistics/rx_bytes" 2>/dev/null || echo 0)
  tx1=$(cat "/sys/class/net/$IFACE/statistics/tx_bytes" 2>/dev/null || echo 0)
  cpu1=($(head -n1 /proc/stat))

  sleep 1

  rx2=$(cat "/sys/class/net/$IFACE/statistics/rx_bytes" 2>/dev/null || echo 0)
  tx2=$(cat "/sys/class/net/$IFACE/statistics/tx_bytes" 2>/dev/null || echo 0)
  cpu2=($(head -n1 /proc/stat))

  idle1=${cpu1[4]}
  idle2=${cpu2[4]}
  total1=0
  total2=0

  for v in "${cpu1[@]:1}"; do total1=$((total1 + v)); done
  for v in "${cpu2[@]:1}"; do total2=$((total2 + v)); done

  total_diff=$((total2 - total1))
  idle_diff=$((idle2 - idle1))
  cpu=0
  [ "$total_diff" -gt 0 ] && cpu=$((100 * (total_diff - idle_diff) / total_diff))

  mem_total=$(free -m | awk '/^Mem:/ {print $2}')
  mem_used=$(free -m | awk '/^Mem:/ {print $3}')

  swap_total=$(free -m | awk '/^Swap:/ {print $2}')
  swap_used=$(free -m | awk '/^Swap:/ {print $3}')

  mem_pct=0
  swap_pct=0
  [ "$mem_total" -gt 0 ] && mem_pct=$((100 * mem_used / mem_total))
  [ "$swap_total" -gt 0 ] && swap_pct=$((100 * swap_used / swap_total))

  rx_kb=$(((rx2 - rx1) / 1024))
  tx_kb=$(((tx2 - tx1) / 1024))

  disk_used=$(df -h / | awk 'NR==2 {print $3}')
  disk_total=$(df -h / | awk 'NR==2 {print $2}')
  disk_pct=$(df -h / | awk 'NR==2 {print $5}')

  read -r load1 load5 load15 _ < /proc/loadavg

  up_seconds=$(awk '{print int($1)}' /proc/uptime)
  up_days=$((up_seconds / 86400))
  up_hours=$(((up_seconds % 86400) / 3600))
  up_minutes=$(((up_seconds % 3600) / 60))

  if [ "$up_days" -gt 0 ]; then
    uptime_text="已运行 ${up_days}天 ${up_hours}小时 ${up_minutes}分钟"
  elif [ "$up_hours" -gt 0 ]; then
    uptime_text="已运行 ${up_hours}小时 ${up_minutes}分钟"
  else
    uptime_text="已运行 ${up_minutes}分钟"
  fi

  openlist_status=$(systemctl is-active openlist 2>/dev/null || echo unknown)
  caddy_status=$(systemctl is-active caddy 2>/dev/null || echo unknown)
  ssh_status=$(systemctl is-active ssh 2>/dev/null || echo unknown)

  top_processes=$(ps -eo pid,comm,%cpu,%mem,rss --sort=-rss | head -11 | awk '
    NR==1 {next}
    {
      gsub(/"/, "\\\"", $2);
      printf "{\"pid\":\"%s\",\"name\":\"%s\",\"cpu\":\"%s\",\"mem\":\"%s\",\"rss\":\"%.1f\"},", $1, $2, $3, $4, $5/1024
    }' | sed 's/,$//')

  cat > "$OUT.tmp" <<JSON
{
  "updated": "$(date '+%Y-%m-%d %H:%M:%S')",
  "uptime_seconds": "$up_seconds",
  "uptime": "$uptime_text",

  "cpu": "$cpu",

  "mem_pct": "$mem_pct",
  "mem_used": "$mem_used",
  "mem_total": "$mem_total",

  "swap_pct": "$swap_pct",
  "swap_used": "$swap_used",
  "swap_total": "$swap_total",

  "disk_pct": "$disk_pct",
  "disk_used": "$disk_used",
  "disk_total": "$disk_total",

  "rx_kb": "$rx_kb",
  "tx_kb": "$tx_kb",

  "load1": "$load1",
  "load5": "$load5",
  "load15": "$load15",

  "openlist": "$openlist_status",
  "caddy": "$caddy_status",
  "ssh": "$ssh_status",

  "processes": [$top_processes]
}
JSON

  mv "$OUT.tmp" "$OUT"
done
