// RyoMonitor — 轻量服务器监控（Go 版后端）。
// 单进程：后台 goroutine 分层采集指标到内存，HTTP 服务提供鉴权网关 + 静态页面 + /status.json。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------- 配置 ----------

const (
	tickCore     = time.Second
	tickServices = 5 * time.Second
	tickProcs    = 3 * time.Second
)

var (
	webRoot      string
	host         string
	port         string
	iface        string
	servicesSpec string
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "missing required env %s\n", key)
		os.Exit(1)
	}
	return v
}

func loadConfig() {
	root, err := filepath.Abs(env("MON_AUTH_WEB_ROOT", "/opt/ryo-monitor/app"))
	if err != nil {
		root = "/opt/ryo-monitor/app"
	}
	webRoot = root
	host = env("MON_AUTH_HOST", "127.0.0.1")
	port = env("MON_AUTH_PORT", "8090")
	loadAuthConfig()
	iface = env("RYO_MONITOR_IFACE", "eth0")
	servicesSpec = env("RYO_MONITOR_SERVICES", "OpenList=openlist Caddy=caddy SSH=ssh")
}

// ---------- 指标采集 ----------

type service struct {
	Name   string `json:"name"`
	Unit   string `json:"unit"`
	Status string `json:"status"`
}

type process struct {
	PID  string `json:"pid"`
	Name string `json:"name"`
	CPU  string `json:"cpu"`
	Mem  string `json:"mem"`
	RSS  string `json:"rss"`
}

type status struct {
	Updated       string    `json:"updated"`
	UptimeSeconds string    `json:"uptime_seconds"`
	Uptime        string    `json:"uptime"`
	CPU           string    `json:"cpu"`
	MemPct        string    `json:"mem_pct"`
	MemUsed       string    `json:"mem_used"`
	MemTotal      string    `json:"mem_total"`
	SwapPct       string    `json:"swap_pct"`
	SwapUsed      string    `json:"swap_used"`
	SwapTotal     string    `json:"swap_total"`
	DiskPct       string    `json:"disk_pct"`
	DiskUsed      string    `json:"disk_used"`
	DiskTotal     string    `json:"disk_total"`
	RxKb          string    `json:"rx_kb"`
	TxKb          string    `json:"tx_kb"`
	Load1         string    `json:"load1"`
	Load5         string    `json:"load5"`
	Load15        string    `json:"load15"`
	Cores         int       `json:"cores"`
	Services      []service `json:"services"`
	Processes     []process `json:"processes"`
}

const clkTck = 100 // USER_HZ
const pageSize = 4096

var (
	statusMu      sync.RWMutex
	statusJSON    = []byte("{}")
	cachedSvcMu   sync.RWMutex
	cachedSvc     []service
	cachedProcMu  sync.RWMutex
	cachedProc    []process
)

func readUintFile(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return v
}

func cpuTotalIdle() (total, idle uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	fs := strings.Fields(line)
	for i, v := range fs[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 {
			idle = n
		}
	}
	return total, idle
}

func meminfo() map[string]uint64 {
	out := map[string]uint64{}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		fs := strings.Fields(line)
		if len(fs) >= 2 {
			v, _ := strconv.ParseUint(fs[1], 10, 64)
			out[strings.TrimSuffix(fs[0], ":")] = v
		}
	}
	return out
}

func humanDF(b uint64) string {
	const unit = 1024.0
	v := float64(b)
	units := []string{"B", "K", "M", "G", "T", "P"}
	i := 0
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	if v >= 10 || v == float64(int64(v)) {
		return fmt.Sprintf("%.0f%s", v, units[i])
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

func diskStats(path string) (used, total, pct string) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return "0", "0", "0%"
	}
	bs := uint64(st.Bsize)
	totalB := st.Blocks * bs
	freeB := st.Bfree * bs
	availB := st.Bavail * bs
	usedB := totalB - freeB
	p := 0
	if denom := usedB + availB; denom > 0 {
		p = int((usedB*100 + denom - 1) / denom)
	}
	return humanDF(usedB), humanDF(totalB), strconv.Itoa(p)
}

func uptimeText(sec uint64) string {
	d := sec / 86400
	h := (sec % 86400) / 3600
	m := (sec % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("已运行 %d天 %d小时 %d分钟", d, h, m)
	case h > 0:
		return fmt.Sprintf("已运行 %d小时 %d分钟", h, m)
	default:
		return fmt.Sprintf("已运行 %d分钟", m)
	}
}

var dockerClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", "/var/run/docker.sock")
		},
	},
}

func dockerContainerActive(name string) string {
	resp, err := dockerClient.Get("http://docker/containers/" + name + "/json")
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "inactive"
	}
	var v struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return "unknown"
	}
	if v.State.Running {
		return "active"
	}
	return "inactive"
}

func parseServiceEntries(spec string) [][2]string {
	var raw []string
	if strings.ContainsAny(spec, ",\n") {
		raw = strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == '\n' })
	} else {
		raw = strings.Fields(spec)
	}
	var out [][2]string
	for _, item := range raw {
		item = strings.TrimSpace(item)
		eq := strings.Index(item, "=")
		if eq < 0 {
			continue
		}
		name := strings.TrimSpace(item[:eq])
		unit := strings.TrimSpace(item[eq+1:])
		if name == "" || unit == "" {
			continue
		}
		out = append(out, [2]string{name, unit})
	}
	return out
}

func collectServices() []service {
	var list []service
	for _, entry := range parseServiceEntries(servicesSpec) {
		name, unit := entry[0], entry[1]
		var st string
		if strings.HasPrefix(unit, "docker:") {
			st = dockerContainerActive(strings.TrimPrefix(unit, "docker:"))
		} else {
			st = "unknown"
			if out, err := exec.Command("systemctl", "is-active", unit).Output(); err == nil {
				if s := strings.TrimSpace(string(out)); s != "" {
					st = s
				}
			} else if s := strings.TrimSpace(string(out)); s != "" {
				st = s
			}
		}
		list = append(list, service{Name: name, Unit: unit, Status: st})
	}
	return list
}

func topProcesses(memTotalKB uint64) []process {
	entries, _ := os.ReadDir("/proc")
	uptimeSec := 0.0
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f, e := strconv.ParseFloat(strings.Fields(string(b))[0], 64); e == nil {
			uptimeSec = f
		}
	}
	type row struct {
		pid, name      string
		cpu, mem, rssM float64
	}
	var rows []row
	for _, e := range entries {
		pid := e.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		data, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil {
			continue
		}
		s := string(data)
		o := strings.IndexByte(s, '(')
		c := strings.LastIndexByte(s, ')')
		if o < 0 || c < 0 || c < o {
			continue
		}
		name := s[o+1 : c]
		fs := strings.Fields(s[c+1:])
		if len(fs) < 22 {
			continue
		}
		utime, _ := strconv.ParseFloat(fs[11], 64)
		stime, _ := strconv.ParseFloat(fs[12], 64)
		starttime, _ := strconv.ParseFloat(fs[19], 64)
		rssPages, _ := strconv.ParseFloat(fs[21], 64)
		cpuSec := (utime + stime) / clkTck
		elapsed := uptimeSec - starttime/clkTck
		cpu := 0.0
		if elapsed > 0 {
			cpu = 100 * cpuSec / elapsed
		}
		rssKB := rssPages * pageSize / 1024
		mem := 0.0
		if memTotalKB > 0 {
			mem = 100 * rssKB / float64(memTotalKB)
		}
		rows = append(rows, row{pid, name, cpu, mem, rssKB / 1024})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].rssM > rows[j].rssM })
	var out []process
	for i := 0; i < len(rows) && i < 10; i++ {
		out = append(out, process{
			PID:  rows[i].pid,
			Name: rows[i].name,
			CPU:  fmt.Sprintf("%.1f", rows[i].cpu),
			Mem:  fmt.Sprintf("%.1f", rows[i].mem),
			RSS:  fmt.Sprintf("%.1f", rows[i].rssM),
		})
	}
	return out
}

func loadavg() (string, string, string) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "0", "0", "0"
	}
	fs := strings.Fields(string(b))
	if len(fs) < 3 {
		return "0", "0", "0"
	}
	return fs[0], fs[1], fs[2]
}

func collectCore(prevRx, prevTx, prevTotal, prevIdle uint64, svcs []service, procs []process) (status, uint64, uint64, uint64, uint64) {
	rx := readUintFile("/sys/class/net/" + iface + "/statistics/rx_bytes")
	tx := readUintFile("/sys/class/net/" + iface + "/statistics/tx_bytes")
	total, idle := cpuTotalIdle()

	cpuPct := 0
	if d := int64(total) - int64(prevTotal); d > 0 {
		cpuPct = int((d - (int64(idle) - int64(prevIdle))) * 100 / d)
	}
	rxKb := (int64(rx) - int64(prevRx)) / 1024
	txKb := (int64(tx) - int64(prevTx)) / 1024
	if rxKb < 0 {
		rxKb = 0
	}
	if txKb < 0 {
		txKb = 0
	}

	mi := meminfo()
	memTotal := mi["MemTotal"]
	memUsed := uint64(0)
	if memTotal > 0 {
		cache := mi["Cached"] + mi["SReclaimable"]
		if u := int64(memTotal) - int64(mi["MemFree"]) - int64(mi["Buffers"]) - int64(cache); u > 0 {
			memUsed = uint64(u)
		}
	}
	swapTotal := mi["SwapTotal"]
	swapUsed := uint64(0)
	if swapTotal >= mi["SwapFree"] {
		swapUsed = swapTotal - mi["SwapFree"]
	}
	memPct, swapPct := 0, 0
	if memTotal > 0 {
		memPct = int(memUsed * 100 / memTotal)
	}
	if swapTotal > 0 {
		swapPct = int(swapUsed * 100 / swapTotal)
	}

	diskUsed, diskTotal, diskPct := diskStats("/")

	up := uint64(0)
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f, e := strconv.ParseFloat(strings.Fields(string(b))[0], 64); e == nil {
			up = uint64(f)
		}
	}
	l1, l5, l15 := loadavg()

	st := status{
		Updated:       time.Now().Format("2006-01-02 15:04:05"),
		UptimeSeconds: strconv.FormatUint(up, 10),
		Uptime:        uptimeText(up),
		CPU:           strconv.Itoa(cpuPct),
		MemPct:        strconv.Itoa(memPct),
		MemUsed:       strconv.FormatUint(memUsed/1024, 10),
		MemTotal:      strconv.FormatUint(memTotal/1024, 10),
		SwapPct:       strconv.Itoa(swapPct),
		SwapUsed:      strconv.FormatUint(swapUsed/1024, 10),
		SwapTotal:     strconv.FormatUint(swapTotal/1024, 10),
		DiskPct:       diskPct,
		DiskUsed:      diskUsed,
		DiskTotal:     diskTotal,
		RxKb:          strconv.FormatInt(rxKb, 10),
		TxKb:          strconv.FormatInt(txKb, 10),
		Load1:         l1,
		Load5:         l5,
		Load15:        l15,
		Cores:         runtime.NumCPU(),
		Services:      svcs,
		Processes:     procs,
	}
	return st, rx, tx, total, idle
}

func refreshServices() {
	svcs := collectServices()
	cachedSvcMu.Lock()
	cachedSvc = svcs
	cachedSvcMu.Unlock()
}

func refreshProcesses() {
	mi := meminfo()
	procs := topProcesses(mi["MemTotal"])
	cachedProcMu.Lock()
	cachedProc = procs
	cachedProcMu.Unlock()
}

func snapshotCached() ([]service, []process) {
	cachedSvcMu.RLock()
	svcs := cachedSvc
	cachedSvcMu.RUnlock()
	cachedProcMu.RLock()
	procs := cachedProc
	cachedProcMu.RUnlock()
	if svcs == nil {
		svcs = []service{}
	}
	if procs == nil {
		procs = []process{}
	}
	return svcs, procs
}

func collector() {
	prevRx := readUintFile("/sys/class/net/" + iface + "/statistics/rx_bytes")
	prevTx := readUintFile("/sys/class/net/" + iface + "/statistics/tx_bytes")
	prevTotal, prevIdle := cpuTotalIdle()

	refreshServices()
	refreshProcesses()
	lastSvc := time.Now()
	lastProc := time.Now()

	ticker := time.NewTicker(tickCore)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		if now.Sub(lastSvc) >= tickServices {
			refreshServices()
			lastSvc = now
		}
		if now.Sub(lastProc) >= tickProcs {
			refreshProcesses()
			lastProc = now
		}

		svcs, procs := snapshotCached()
		st, rx, tx, total, idle := collectCore(prevRx, prevTx, prevTotal, prevIdle, svcs, procs)
		prevRx, prevTx, prevTotal, prevIdle = rx, tx, total, idle

		if data, err := json.Marshal(st); err == nil {
			statusMu.Lock()
			statusJSON = data
			statusMu.Unlock()
		}
	}
}

// ---------- HTTP ----------

func resolveStatic(reqPath string) (string, bool) {
	p, err := url.PathUnescape(reqPath)
	if err != nil {
		return "", false
	}
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	switch p {
	case "/":
		p = "/index.html"
	case "/favicon.svg":
		p = "/assets/logo.svg"
	}
	candidate := filepath.Join(webRoot, filepath.Clean("/"+strings.TrimPrefix(p, "/")))
	if candidate != webRoot && !strings.HasPrefix(candidate, webRoot+string(os.PathSeparator)) {
		return "", false
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		candidate = filepath.Join(candidate, "index.html")
	}
	return candidate, true
}

func serveStatic(w http.ResponseWriter, r *http.Request) {
	target, ok := resolveStatic(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(target, ".webmanifest") {
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "private, max-age=30")
	http.ServeFile(w, r, target)
}

func handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	cookie := r.Header.Get("Cookie")

	switch {
	case path == "/healthz":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
		return
	case path == "/logout":
		handleLogout(w, r)
		return
	case path == "/login":
		handleLogin(w, r, cookie)
		return
	case path == "/favicon.svg" || strings.HasPrefix(path, "/assets/"):
		serveStatic(w, r)
		return
	}

	if r.Method == http.MethodPost {
		http.NotFound(w, r)
		return
	}

	if !authenticated(cookie) {
		redirectUnauthenticated(w, r)
		return
	}

	if path == "/status.json" {
		statusMu.RLock()
		data := statusJSON
		statusMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(data)
		return
	}

	serveStatic(w, r)
}

func genEnv(password string) {
	fmt.Println("MON_AUTH_HOST=127.0.0.1")
	fmt.Println("MON_AUTH_PORT=8090")
	fmt.Println("MON_AUTH_WEB_ROOT=/opt/ryo-monitor/app")
	writeGenEnvAuth(password)
	fmt.Println("RYO_MONITOR_IFACE=eth0")
	fmt.Println(`RYO_MONITOR_SERVICES="OpenList=openlist Caddy=caddy SSH=ssh"`)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "genenv" {
		pw := ""
		if len(os.Args) > 2 {
			pw = os.Args[2]
		} else {
			fmt.Fprint(os.Stderr, "password: ")
			fmt.Scanln(&pw)
		}
		genEnv(pw)
		return
	}

	loadConfig()
	go collector()
	addr := host + ":" + port
	fmt.Printf("RyoMonitor listening on %s, serving %s\n", addr, webRoot)
	if err := http.ListenAndServe(addr, http.HandlerFunc(handler)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
