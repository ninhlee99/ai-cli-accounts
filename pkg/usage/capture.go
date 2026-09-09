package usage

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-cli-accounts/pkg/types"
)

func UsageLogPath() string { return filepath.Join(types.BaseDir(), "usage.log") }

func AppendUsageEntry(e types.UsageEntry) {
	_ = os.MkdirAll(types.BaseDir(), 0o700)
	f, err := os.OpenFile(UsageLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

type closeBothCloser struct {
	a io.Closer
	b io.Closer
}

func (c closeBothCloser) Close() error {
	e1 := c.a.Close()
	e2 := c.b.Close()
	if e1 != nil {
		return e1
	}
	return e2
}

// WrapUsageCapture tees resp.Body through a parser that pulls out token counts.
func WrapUsageCapture(resp *http.Response, account string) {
	debug := os.Getenv("AM_PROXY_DEBUG") != ""
	if resp.Body == nil || resp.Request == nil {
		return
	}
	path := resp.Request.URL.Path
	if !strings.HasSuffix(path, "/messages") && !strings.HasSuffix(path, "/chat/completions") {
		return
	}

	pr, pw := io.Pipe()
	orig := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(orig, pw), orig}

	project, session := "", ""
	if resp.Request != nil {
		project = ProjectForRemoteAddr(resp.Request.RemoteAddr)
		session = resp.Request.Header.Get("X-Claude-Code-Session-Id")
	}

	gzipped := strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip")
	go func() {
		e := types.UsageEntry{Time: time.Now(), Account: account, Project: project, Session: session}
		var src io.Reader = pr
		if gzipped {
			if gr, err := gzip.NewReader(pr); err == nil {
				defer gr.Close()
				src = gr
			} else {
				src = nil
			}
		}
		if src != nil {
			parseUsageStream(src, &e)
		} else {
			_, _ = io.Copy(io.Discard, pr)
		}
		_ = pr.CloseWithError(io.EOF)
		if debug {
			log.Printf("usage: parsed in=%d out=%d model=%q account=%q status=%d", e.Input, e.Output, e.Model, e.Account, resp.StatusCode)
		}
		if e.Input > 0 || e.Output > 0 {
			AppendUsageEntry(e)
		}
	}()

	closer := resp.Body.(interface {
		io.Reader
		io.Closer
	})
	resp.Body = struct {
		io.Reader
		io.Closer
	}{closer, closeBothCloser{closer, pw}}
}

func parseUsageStream(r io.Reader, e *types.UsageEntry) {
	br := bufio.NewReaderSize(r, 64*1024)
	first, _ := br.Peek(512)
	if bytes.HasPrefix(bytes.TrimSpace(first), []byte("{")) && !bytes.Contains(first, []byte("event:")) {
		b, _ := io.ReadAll(br)
		applyUsageJSON(b, e)
		return
	}
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		applyUsageJSON([]byte(payload), e)
	}
}

func applyUsageJSON(b []byte, e *types.UsageEntry) {
	var v struct {
		Model   string `json:"model"`
		Message struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &v) != nil {
		return
	}
	if e.Model == "" {
		if v.Message.Model != "" {
			e.Model = v.Message.Model
		} else if v.Model != "" {
			e.Model = v.Model
		}
	}
	if v.Message.Usage.InputTokens > 0 {
		e.Input = v.Message.Usage.InputTokens
	}
	if v.Message.Usage.OutputTokens > 0 {
		// message_delta events report a cumulative running total, and can
		// fire more than once per response — overwrite, don't accumulate.
		e.Output = v.Message.Usage.OutputTokens
	}
	if v.Usage.InputTokens > 0 {
		e.Input = v.Usage.InputTokens
	}
	if v.Usage.OutputTokens > 0 {
		e.Output = v.Usage.OutputTokens
	}
	if v.Usage.PromptTokens > 0 {
		e.Input = v.Usage.PromptTokens
	}
	if v.Usage.CompletionTokens > 0 {
		e.Output = v.Usage.CompletionTokens
	}
}
