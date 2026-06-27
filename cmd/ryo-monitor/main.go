// RyoMonitor — 轻量服务器监控（Go 版后端）。
// 单进程：后台 goroutine 每秒采集指标到内存，HTTP 服务提供鉴权网关 + 静态页面 + /status.json。
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
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

	_ "embed"
	_ "time/tzdata"
)

//go:embed login.html
var loginPage string

// ---------- 配置 ----------

var (
	webRoot      string
	host         string
	port         string
	cookieName   string
	sessionTTL   int64
	passwordHash string
	secret       []byte
	iface        string
	servicesSpec string
	trustProxy   bool
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
	cookieName = env("MON_AUTH_COOKIE", "ryo_mon_session")
	ttl, err := strconv.ParseInt(env("MON_AUTH_SESSION_TTL", strconv.Itoa(7*24*60*60)), 10, 64)
	if err != nil {
		ttl = 7 * 24 * 60 * 60
	}
	sessionTTL = ttl
	// MON_AUTH_TRUST_PROXY=1：信任前置反代/SSO 已鉴权，本服务跳过内置登录直接服务。
	trustProxy = os.Getenv("MON_AUTH_TRUST_PROXY") == "1"
	if trustProxy {
		passwordHash = os.Getenv("MON_AUTH_PASSWORD_HASH")
		secret = []byte(os.Getenv("MON_AUTH_SECRET"))
	} else {
		passwordHash = mustEnv("MON_AUTH_PASSWORD_HASH")
		secret = []byte(mustEnv("MON_AUTH_SECRET"))
	}
	iface = env("RYO_MONITOR_IFACE", "eth0")
	servicesSpec = env("RYO_MONITOR_SERVICES", "OpenList=openlist Caddy=caddy SSH=ssh")
}

// ---------- 鉴权 ----------

var b64 = base64.RawURLEncoding

// pbkdf2-hmac-sha256（标准库实现，避免外部依赖）
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha256.Size
	numBlocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, numBlocks*hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf := hmac.New(sha256.New, password)
		prf.Write(salt)
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		prf.Write(idx[:])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

func verifyPassword(password string) bool {
	parts := strings.SplitN(passwordHash, "$", 4)
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err := b64.DecodeString(parts[2])
	if err != nil {
		return false
	}
	derived := pbkdf2SHA256([]byte(password), salt, iter, sha256.Size)
	return subtle.ConstantTimeCompare([]byte(b64.EncodeToString(derived)), []byte(parts[3])) == 1
}

func signPayload(payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return b64.EncodeToString([]byte(payload)) + "." + b64.EncodeToString(mac.Sum(nil))
}

func validSession(cookieHeader string) bool {
	if cookieHeader == "" {
		return false
	}
	var token string
	for _, part := range strings.Split(cookieHeader, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == cookieName {
			token = kv[1]
		}
	}
	if token == "" || !strings.Contains(token, ".") {
		return false
	}
	dot := strings.LastIndex(token, ".")
	payloadB64, signature := token[:dot], token[dot+1:]
	payloadBytes, err := b64.DecodeString(payloadB64)
	if err != nil {
		return false
	}
	payload := string(payloadBytes)
	expected := signPayload(payload)
	expectedSig := expected[strings.LastIndex(expected, ".")+1:]
	if subtle.ConstantTimeCompare([]byte(signature), []byte(expectedSig)) != 1 {
		return false
	}
	colon := strings.Index(payload, ":")
	if colon < 0 {
		return false
	}
	expiresAt, err := strconv.ParseInt(payload[:colon], 10, 64)
	if err != nil {
		return false
	}
	return expiresAt >= time.Now().Unix()
}

func makeSession() string {
	nonceBytes := make([]byte, 18)
	rand.Read(nonceBytes)
	payload := fmt.Sprintf("%d:%s", time.Now().Unix()+sessionTTL, b64.EncodeToString(nonceBytes))
	return signPayload(payload)
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
	OpenList      string    `json:"openlist"`
	Caddy         string    `json:"caddy"`
	SSH           string    `json:"ssh"`
	Services      []service `json:"services"`
	Processes     []process `json:"processes"`
}

const clkTck = 100 // USER_HZ
const pageSize = 4096

var (
	statusMu   sync.RWMutex
	statusJSON = []byte("{}")
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
		if i == 3 { // idle 列（与原 shell 一致，仅 idle）
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
			out[strings.TrimSuffix(fs[0], ":")] = v // kB
		}
	}
	return out
}

// 仿 df -h 的人类可读（1024 进制）
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
		p = int((usedB*100 + denom - 1) / denom) // 仿 df 向上取整
	}
	return humanDF(usedB), humanDF(totalB), strconv.Itoa(p) + "%"
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

// dockerContainerActive 查询 docker 容器运行状态：active / inactive / unknown。
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

// parseServiceEntries 解析 RYO_MONITOR_SERVICES：每项为「显示名=监控目标」。
// 监控目标为 systemd 单元名，或 docker:<容器名>。
// 含逗号/换行时按逗号或换行分隔条目（显示名可包含空格，如 "App Store=docker:asspp"）；
// 否则回退到按空白分隔，兼容旧的 "OpenList=openlist Caddy=caddy" 写法。
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

func collectServices() ([]service, string, string, string) {
	openlist, caddy, ssh := "unknown", "unknown", "unknown"
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
				st = s // is-active 对非 active 会返回非零退出码，但 stdout 仍是状态
			}
		}
		switch unit {
		case "openlist":
			openlist = st
		case "caddy":
			caddy = st
		case "ssh", "sshd":
			ssh = st
		}
		list = append(list, service{Name: name, Unit: unit, Status: st})
	}
	return list, openlist, caddy, ssh
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

func collectOnce(prevRx, prevTx, prevTotal, prevIdle uint64) (status, uint64, uint64, uint64, uint64) {
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
	svcs, openlist, caddy, ssh := collectServices()

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
		OpenList:      openlist,
		Caddy:         caddy,
		SSH:           ssh,
		Services:      svcs,
		Processes:     topProcesses(memTotal),
	}
	return st, rx, tx, total, idle
}

func collector() {
	prevRx := readUintFile("/sys/class/net/" + iface + "/statistics/rx_bytes")
	prevTx := readUintFile("/sys/class/net/" + iface + "/statistics/tx_bytes")
	prevTotal, prevIdle := cpuTotalIdle()
	for {
		time.Sleep(time.Second)
		st, rx, tx, total, idle := collectOnce(prevRx, prevTx, prevTotal, prevIdle)
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

func sendLogin(w http.ResponseWriter, r *http.Request, statusCode int, errorKey, nextPath string) {
	page := strings.ReplaceAll(loginPage, "__ERROR_KEY__", htmlEscapeAttr(errorKey))
	page = strings.ReplaceAll(page, "__NEXT__", htmlEscapeAttr(nextPath))
	body := []byte(page)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

func htmlEscapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")
	return r.Replace(s)
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
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: 0, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	case path == "/login":
		if r.Method == http.MethodPost {
			handleLoginPost(w, r)
			return
		}
		if trustProxy || validSession(cookie) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		next := r.URL.Query().Get("next")
		if !strings.HasPrefix(next, "/") {
			next = "/"
		}
		sendLogin(w, r, http.StatusOK, "", next)
		return
	case path == "/favicon.svg" || strings.HasPrefix(path, "/assets/"):
		serveStatic(w, r)
		return
	}

	if r.Method == http.MethodPost {
		http.NotFound(w, r)
		return
	}

	if !trustProxy && !validSession(cookie) {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
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

func handleLoginPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	password := r.PostFormValue("password")
	next := r.PostFormValue("next")
	if !strings.HasPrefix(next, "/") {
		next = "/"
	}
	if !verifyPassword(password) {
		time.Sleep(800 * time.Millisecond)
		sendLogin(w, r, http.StatusUnauthorized, "invalidPassword", next)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: makeSession(), Path: "/",
		MaxAge: int(sessionTTL), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// genEnv 打印一份完整的环境变量文件（含 PBKDF2 密码哈希与随机 secret），供安装脚本使用，免去 python 依赖。
func genEnv(password string) {
	salt := make([]byte, 16)
	rand.Read(salt)
	const iter = 260000
	dk := pbkdf2SHA256([]byte(password), salt, iter, sha256.Size)
	sec := make([]byte, 36)
	rand.Read(sec)
	fmt.Println("MON_AUTH_HOST=127.0.0.1")
	fmt.Println("MON_AUTH_PORT=8090")
	fmt.Println("MON_AUTH_WEB_ROOT=/opt/ryo-monitor/app")
	fmt.Println("MON_AUTH_SESSION_TTL=604800")
	fmt.Printf("MON_AUTH_PASSWORD_HASH=pbkdf2_sha256$%d$%s$%s\n", iter, b64.EncodeToString(salt), b64.EncodeToString(dk))
	fmt.Println("MON_AUTH_SECRET=" + b64.EncodeToString(sec))
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
