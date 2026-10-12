// Wiki Agent — 状态机式 AI 文档生成，直接集成进思源
// 用原始 gin handler，不走 contractHandler 框架，更简单
// 复用思源 LLM 配置（GetAgentModel + NewAIClientWithModel + CreateOpenAICompletionStream）
// 生成后用 model.CreateDocByMd 落盘到笔记本
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/siyuan-note/siyuan/kernel/conf"
	"github.com/siyuan-note/siyuan/kernel/model"
)

// wikiPM provider+model 候选对
type wikiPM struct {
	p *conf.Provider
	m *conf.Model
}

// WikiAgent 原始 gin handler
func WikiAgent(c *gin.Context) {
	var req struct {
		NotebookID string            `json:"notebookId"`
		Topic      string            `json:"topic"`
		State      string            `json:"state"`
		Answers    map[string]string `json:"answers"`
		FormValues map[string]any    `json:"formValues"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": err.Error()})
		return
	}
	if req.Topic == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": "topic is required"})
		return
	}

	selectedProvider, selectedModel := model.Conf.AI.GetAgentModel()
	if selectedProvider == nil || selectedModel == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": "AI 模型未配置，请在 设置→AI 配置 Agent 模型"})
		return
	}

	// 收集所有 enabled 的 provider/model 做 fallback（dashscope 某些模型可能无权限）
	candidates := []wikiPM{}
	candidates = append(candidates, wikiPM{selectedProvider, selectedModel})
	for _, p := range model.Conf.AI.Providers {
		if p == nil || !p.Enabled || p.ID == selectedProvider.ID {
			continue
		}
		for _, m := range p.Models {
			if m != nil && m.Enabled {
				candidates = append(candidates, wikiPM{p, m})
			}
		}
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"code": -1, "msg": "streaming unsupported"})
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	switch req.State {
	case "", "ask":
		text := wikiRawLLMStreamWithFallback(c.Request.Context(), candidates,
			fmt.Sprintf("用户想创作主题为「%s」的文档。先问 2-3 个关键选择题。只输出 JSON：{\"questions\":[{\"id\":\"q1\",\"question\":\"问题\",\"options\":[\"选项1\",\"选项2\"]}]}。不要 markdown，直接 JSON。", req.Topic), nil)
		wikiWriteSSE(c, "question", map[string]any{"raw": text})
	case "questionnaire":
		ansJSON, _ := json.Marshal(req.Answers)
		text := wikiRawLLMStreamWithFallback(c.Request.Context(), candidates,
			fmt.Sprintf("主题: %s\n用户回答: %s\n根据主题和回答生成详细问卷。字段尽量选择题。只输出JSON：{\"title\":\"问卷\",\"fields\":[{\"id\":\"tone\",\"type\":\"radio\",\"label\":\"语气\",\"options\":[\"正式\",\"活泼\"]}]}。不要markdown。", req.Topic, string(ansJSON)), nil)
		wikiWriteSSE(c, "questionnaire", map[string]any{"raw": text})
	case "generate":
		wikiDoGenerateRaw(c, flusher, candidates, req)
	default:
		wikiWriteSSE(c, "error", map[string]any{"message": "unknown state"})
	}
	flusher.Flush()
}

// wikiDoGenerateRaw 流式生成 + 落盘
func wikiDoGenerateRaw(c *gin.Context, flusher http.Flusher, candidates []wikiPM, req struct {
	NotebookID string            `json:"notebookId"`
	Topic      string            `json:"topic"`
	State      string            `json:"state"`
	Answers    map[string]string `json:"answers"`
	FormValues map[string]any    `json:"formValues"`
}) {
	allAns := map[string]any{}
	for k, v := range req.Answers {
		allAns[k] = v
	}
	for k, v := range req.FormValues {
		allAns[k] = v
	}
	ansJSON, _ := json.Marshal(allAns)
	prompt := fmt.Sprintf("请根据以下信息创作文档（Markdown格式）。\n\n主题: %s\n用户要求:\n%s\n\n要求：结构清晰含标题层级，内容详实，直接输出正文。", req.Topic, string(ansJSON))

	wikiWriteSSE(c, "start", map[string]any{})
	flusher.Flush()

	fullText := wikiRawLLMStreamWithFallback(c.Request.Context(), candidates, prompt, func(contentDelta string) {
		wikiWriteSSE(c, "content", map[string]any{"text": contentDelta})
		flusher.Flush()
	})

	// 落盘
	notebookID := req.NotebookID
	if notebookID == "" {
		notebookID = ensureWikiAgentNotebook()
	}
	title := req.Topic
	if len([]rune(title)) > 50 {
		title = string([]rune(title)[:50])
	}
	hPath := "/AI生成/" + title
	tree, err := model.CreateDocByMd(notebookID, hPath, title, fullText, nil, nil)
	docID := ""
	if err == nil && tree != nil && tree.Root != nil {
		docID = tree.Root.ID
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	wikiWriteSSE(c, "done", map[string]any{"docId": docID, "notebookId": notebookID, "error": errMsg})
}

// wikiRawLLMStreamWithFallback 遍历候选 provider/model，失败自动换下一个
func wikiRawLLMStreamWithFallback(ctx context.Context, candidates []wikiPM, prompt string, onContent func(string)) string {
	for _, c := range candidates {
		text := wikiRawLLMStream(ctx, c.p, c.m, prompt, onContent)
		if text != "" {
			return text
		}
		fmt.Fprintln(os.Stderr, "wikiLLM: 换下一个 provider/model")
	}
	return ""
}

// wikiRawLLMStream 直接用 raw HTTP 调 LLM，自己解析 OpenAI 流式（兼容 reasoning_content）
// onContent 回调每个 content delta；返回完整 content 文本
func wikiRawLLMStream(ctx context.Context, provider *conf.Provider, m *conf.Model, prompt string, onContent func(string)) string {
	if provider == nil || m == nil {
		fmt.Fprintln(os.Stderr, "wikiLLM: provider or model is nil")
		return ""
	}
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	url := baseURL + "/chat/completions"
	fmt.Fprintln(os.Stderr, "wikiLLM: url=", url, "model=", m.Name, "apiKey=", provider.APIKey[:8]+"...")
	body, _ := json.Marshal(map[string]any{
		"model":      m.Name,
		"max_tokens": 8192,
		"stream":     true,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wikiLLM newreq err:", err)
		return ""
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", provider.APIKey)
	httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wikiLLM do err:", err)
		return ""
	}
	defer resp.Body.Close()
	fmt.Fprintln(os.Stderr, "wikiLLM resp status:", resp.StatusCode)

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if lineCount < 3 {
			fmt.Fprintln(os.Stderr, "wikiLLM line:", line[:min(len(line), 200)])
			lineCount++
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			break
		}
		var evt struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &evt) == nil && len(evt.Choices) > 0 {
			if evt.Choices[0].Delta.Content != "" {
				full.WriteString(evt.Choices[0].Delta.Content)
				if onContent != nil {
					onContent(evt.Choices[0].Delta.Content)
				}
			}
		}
	}
	fmt.Fprintln(os.Stderr, "wikiLLM full len:", full.Len())
	return full.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func ensureWikiAgentNotebook() string {
	name := "Wiki Agent"
	if nb := model.GetBoxByName(name); nb != nil {
		return nb.ID
	}
	id, err := model.CreateBox(name)
	if err == nil && id != "" {
		return id
	}
	nbs, err := model.ListNotebooks()
	if err == nil {
		for _, nb := range nbs {
			if !nb.Closed {
				return nb.ID
			}
		}
		if len(nbs) > 0 {
			return nbs[0].ID
		}
	}
	return ""
}

func wikiWriteSSE(c *gin.Context, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(c.Writer, "event:%s\ndata:%s\n\n", event, string(b))
}
