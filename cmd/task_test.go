package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golin/c2config"
)

// TestTaskLoop 验证完整任务链路:心跳 → 拉取任务 → 本机执行 → 回传结果
func TestTaskLoop(t *testing.T) {
	var (
		mu         sync.Mutex
		heartbeats int
		results    int
		gotOutput  string
		firstErr   string
	)
	fail := func(format string, args ...interface{}) {
		mu.Lock()
		if firstErr == "" {
			firstErr = fmt.Sprintf(format, args...)
		}
		mu.Unlock()
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == c2config.HealthPath && r.Method == http.MethodPost:
			if r.Header.Get(c2config.HeaderName) != c2config.Token {
				fail("heartbeat missing token")
			}
			var hb c2config.Heartbeat
			json.NewDecoder(r.Body).Decode(&hb)
			if hb.ClientID == "" {
				fail("heartbeat missing client_id")
			}
			mu.Lock()
			heartbeats++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == c2config.TasksPath && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"t1","command":"echo hello-c2","status":"pending"}]`))
		case r.URL.Path == c2config.ResultPath && r.Method == http.MethodPost:
			var res c2config.TaskResult
			json.NewDecoder(r.Body).Decode(&res)
			if res.ID != "t1" {
				fail("result id = %q, want t1", res.ID)
			}
			mu.Lock()
			results++
			gotOutput = res.Output
			mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer ts.Close()

	dataDirFn = func() string { return t.TempDir() }
	serverURL = ts.URL
	heartbeatInterval = 1

	startReporter()
	defer func() {
		serverURL = ""
		stopReporter()
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		done := results > 0 && heartbeats > 0
		out, errMsg := gotOutput, firstErr
		mu.Unlock()
		if errMsg != "" {
			t.Fatal(errMsg)
		}
		if done {
			if !strings.Contains(out, "hello-c2") {
				t.Fatalf("unexpected output: %q", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no result within 10s: heartbeats=%d results=%d", heartbeats, results)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestExecCommandChineseEcho 中文命令执行与回显:Windows cmd 输出 GBK,
// 应经 decodeCmdOutput 转为 UTF-8 回传(中文回显的关键链路)
func TestExecCommandChineseEcho(t *testing.T) {
	out, err := execCommand("echo 中文回显测试")
	if err != nil {
		t.Fatalf("exec error: %v", err)
	}
	if !strings.Contains(out, "中文回显测试") {
		t.Fatalf("output does not contain 中文回显测试: %q", out)
	}
}

// TestExecCommandTimeout 验证命令超时:缩短超时时间,确认长时间命令被及时终止
func TestExecCommandTimeout(t *testing.T) {
	old := taskTimeout
	taskTimeout = 300 * time.Millisecond
	defer func() { taskTimeout = old }()

	start := time.Now()
	_, err := execCommand("ping -n 60 127.0.0.1")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("timeout did not take effect")
	}
}

// TestLimitedBufferTruncate 验证输出超限截断
func TestLimitedBufferTruncate(t *testing.T) {
	var buf limitedBuffer
	chunk := strings.Repeat("A", 4096)
	for i := 0; i < 20; i++ {
		buf.Write([]byte(chunk))
	}
	if !buf.truncated {
		t.Fatal("buffer should be marked truncated")
	}
	if buf.b.Len() != taskMaxOutput {
		t.Fatalf("len = %d, want %d", buf.b.Len(), taskMaxOutput)
	}
}
