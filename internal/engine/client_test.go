package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arrase/code-reducer/internal/config"
)

func TestPrepareOllamaRequest(t *testing.T) {
	t.Run("omits num_predict and sends think when unset", func(t *testing.T) {
		c := newLLMClient(&config.Config{ModelID: "m", OllamaBaseURL: "http://localhost:11434", OllamaNumCtx: 2048})

		req, err := c.prepareOllamaRequest(context.Background(), "system", []Message{{Role: "user", Content: "hi"}}, false, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var body ollamaRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body.Think == nil {
			t.Error("expected think to be sent")
		} else if *body.Think {
			t.Error("expected think to default to false")
		}
		if body.Options.NumPredict != 0 {
			t.Errorf("expected num_predict to be unset, got %d", body.Options.NumPredict)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" {
			t.Errorf("expected a system message followed by the user message, got %+v", body.Messages)
		}
		if body.Format != "" {
			t.Errorf("expected no format for non-json calls, got %s", body.Format)
		}
	})

	t.Run("per-call num_predict overrides the client budget", func(t *testing.T) {
		c := newLLMClient(&config.Config{ModelID: "m", OllamaBaseURL: "http://localhost:11434", OllamaNumCtx: 8192, NumPredict: 4000})

		req, err := c.prepareOllamaRequest(context.Background(), "system", nil, false, 96)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var body ollamaRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body.Options.NumPredict != 96 {
			t.Errorf("expected num_predict 96, got %d", body.Options.NumPredict)
		}
	})

	t.Run("sends num_predict and json format when configured", func(t *testing.T) {
		c := newLLMClient(&config.Config{
			ModelID:       "m",
			OllamaBaseURL: "http://localhost:11434",
			OllamaNumCtx:  8192,
			Think:         true,
			NumPredict:    4000,
		})

		req, err := c.prepareOllamaRequest(context.Background(), "system", nil, true, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var body ollamaRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body.Think == nil || !*body.Think {
			t.Error("expected think true to be sent")
		}
		if body.Options.NumPredict != 4000 {
			t.Errorf("expected num_predict 4000, got %d", body.Options.NumPredict)
		}
		if body.Format != "json" {
			t.Errorf("expected json format, got %q", body.Format)
		}
	})
}

func TestCallLLMParsesGenerationMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"docs"},"done_reason":"length","eval_count":7,"prompt_eval_count":1234}`))
	}))
	defer srv.Close()

	c := newLLMClient(&config.Config{ModelID: "m", OllamaBaseURL: srv.URL, OllamaNumCtx: 2048})
	res, err := c.CallLLM(context.Background(), "system", []Message{{Role: "user", Content: "hi"}}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != "docs" {
		t.Errorf("expected content 'docs', got %q", res.Content)
	}
	if res.DoneReason != "length" {
		t.Errorf("expected done_reason 'length', got %q", res.DoneReason)
	}
	if res.EvalCount != 7 {
		t.Errorf("expected eval_count 7, got %d", res.EvalCount)
	}
	if res.PromptCount != 1234 {
		t.Errorf("expected prompt_eval_count 1234, got %d", res.PromptCount)
	}
}

func TestUserMessage(t *testing.T) {
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{name: "single part", parts: []string{"only"}, want: "only"},
		{name: "keeps the run invariant part first", parts: []string{"STYLE", "PAYLOAD\n\nINSTRUCTION"}, want: "STYLE\n\nPAYLOAD\n\nINSTRUCTION"},
		{name: "drops empty parts", parts: []string{"STYLE", "  ", "INSTRUCTION"}, want: "STYLE\n\nINSTRUCTION"},
		{name: "no parts", parts: nil, want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := userMessage(tc.parts...); got != tc.want {
				t.Fatalf("userMessage(%q) = %q, want %q", tc.parts, got, tc.want)
			}
		})
	}
}

func TestRequireCompleteContent(t *testing.T) {
	t.Run("strips fences from a valid result", func(t *testing.T) {
		res := stopResult("```markdown\n# Title\n```")
		content, err := requireCompleteContent(res, "subject")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if content != "# Title" {
			t.Errorf("expected '# Title', got %q", content)
		}
	})

	t.Run("truncation wins over empty content", func(t *testing.T) {
		_, err := requireCompleteContent(truncatedResult(""), "subject")
		if !errors.Is(err, ErrTruncatedOutput) {
			t.Fatalf("expected ErrTruncatedOutput, got %v", err)
		}
	})

	t.Run("truncation names every budget lever and the thinking cost", func(t *testing.T) {
		_, err := requireCompleteContent(truncatedResult("half an answer"), "subject")
		if !errors.Is(err, ErrTruncatedOutput) {
			t.Fatalf("expected ErrTruncatedOutput, got %v", err)
		}
		for _, want := range []string{"num_predict", "slot_num_predict", "paragraph_num_predict", "think: false"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected the truncation message to name %q, got %q", want, err)
			}
		}
	})
}
