//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
)

// ndjsonResult returns a two-line NDJSON string: one system/init line and one result line.
// Used for step responses where you just need step.context = text.
func ndjsonResult(text string) string {
	type initMsg struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
	}
	type resultMsg struct {
		Type         string  `json:"type"`
		Result       string  `json:"result"`
		DurationMs   int     `json:"duration_ms"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}

	init, _ := json.Marshal(initMsg{Type: "system", Subtype: "init", SessionID: "e2e-session-0001"})
	result, _ := json.Marshal(resultMsg{Type: "result", Result: text, DurationMs: 42, TotalCostUSD: 0.0001})

	return string(init) + "\n" + string(result) + "\n"
}

// ndjsonResultWithText returns a multi-line NDJSON string: init + assistant text blocks + result.
// Used when you also want log lines in the output.
func ndjsonResultWithText(logLines []string, resultText string) string {
	type initMsg struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
	}
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type assistantMsg struct {
		Type    string         `json:"type"`
		Content []contentBlock `json:"content"`
	}
	type resultMsg struct {
		Type         string  `json:"type"`
		Result       string  `json:"result"`
		DurationMs   int     `json:"duration_ms"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}

	var sb strings.Builder
	init, _ := json.Marshal(initMsg{Type: "system", Subtype: "init", SessionID: "e2e-session-0001"})
	sb.Write(init)
	sb.WriteByte('\n')

	for _, line := range logLines {
		msg, _ := json.Marshal(assistantMsg{
			Type:    "assistant",
			Content: []contentBlock{{Type: "text", Text: line}},
		})
		sb.Write(msg)
		sb.WriteByte('\n')
	}

	result, _ := json.Marshal(resultMsg{Type: "result", Result: resultText, DurationMs: 42, TotalCostUSD: 0.0001})
	sb.Write(result)
	sb.WriteByte('\n')

	return sb.String()
}
