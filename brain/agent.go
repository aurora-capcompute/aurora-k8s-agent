//go:build tinygo

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/extism/go-pdk"
)

//go:wasmimport extism:host/compute play
func hostPlay(uint64) uint64

const protocol = `You are Aurora, an AI Kubernetes and Helm operator.
The host owns every side effect. Inspect before mutating and never request secrets.
Use only the tools listed below and match their JSON schemas exactly.
Reply with one compact JSON object:
{"actions":[{"action":"<tool name>","content":{...}}]}
After tool observations, request more tools or finish with:
{"actions":[{"action":"final","content":{"answer":"concise Telegram-friendly answer"}}]}
Do not combine final with tool calls. Explain intended mutations clearly; the host handles human approval.`

type input struct {
	Message      string       `json:"message"`
	History      []message    `json:"history,omitempty"`
	SystemPrompt string       `json:"system_prompt,omitempty"`
	Capabilities []capability `json:"capabilities,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type capability struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type actionBatch struct {
	Actions []action `json:"actions"`
}

type action struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content"`
}

type finalContent struct {
	Answer string `json:"answer"`
}

type call struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type hostResponse struct {
	Status  string          `json:"status"`
	Result  json.RawMessage `json:"result,omitempty"`
	Message string          `json:"message,omitempty"`
}

type output struct {
	Status string `json:"status"`
	Answer string `json:"answer,omitempty"`
}

type observation struct {
	Action  string          `json:"action"`
	Status  string          `json:"status"`
	Content json.RawMessage `json:"content,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

var errYielded = errors.New("host yielded")

//go:wasmexport run
func run() int32 {
	err := runAgent()
	if errors.Is(err, errYielded) {
		_ = pdk.OutputJSON(output{Status: "yielded"})
		return 0
	}
	if err != nil {
		pdk.SetError(err)
		return 1
	}
	return 0
}

func runAgent() error {
	in, err := fetchInput()
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Message) == "" {
		return errors.New("message is required")
	}
	system, allowed, err := systemPrompt(in.SystemPrompt, in.Capabilities)
	if err != nil {
		return err
	}
	messages := []message{{Role: "system", Content: system}}
	messages = append(messages, in.History...)
	messages = append(messages, message{Role: "user", Content: in.Message})

	parseErrors := 0
	for turn := 0; turn < 12; turn++ {
		content, err := chat(messages)
		if err != nil {
			return err
		}
		batch, err := decodeActions(content)
		if err != nil {
			parseErrors++
			if parseErrors >= 3 {
				return finish(content)
			}
			messages = append(messages,
				message{Role: "assistant", Content: content},
				message{Role: "user", Content: "Return only the required compact JSON action object. Error: " + err.Error()},
			)
			continue
		}
		parseErrors = 0
		if len(batch.Actions) == 1 && batch.Actions[0].Action == "final" {
			var final finalContent
			if err := json.Unmarshal(batch.Actions[0].Content, &final); err != nil || strings.TrimSpace(final.Answer) == "" {
				return errors.New("final action is missing an answer")
			}
			return finish(final.Answer)
		}
		messages = append(messages, message{Role: "assistant", Content: content})
		results := make([]observation, 0, len(batch.Actions))
		for _, requested := range batch.Actions {
			if requested.Action == "final" {
				continue
			}
			if _, ok := allowed[requested.Action]; !ok {
				results = append(results, observation{
					Action: requested.Action, Status: "failed", Error: "capability is not available",
				})
				continue
			}
			emitProgress(requested.Action, requested.Content)
			response, err := dispatch(call{Name: requested.Action, Args: requested.Content})
			if err != nil {
				return err
			}
			item := observation{Action: requested.Action, Status: response.Status}
			if response.Status == "failed" {
				item.Error = response.Message
			} else {
				item.Content = response.Result
			}
			results = append(results, item)
		}
		raw, _ := json.Marshal(results)
		messages = append(messages, message{Role: "user", Content: string(raw)})
	}
	return errors.New("agent exceeded the maximum tool turns")
}

func systemPrompt(custom string, capabilities []capability) (string, map[string]struct{}, error) {
	var prompt strings.Builder
	if custom = strings.TrimSpace(custom); custom != "" {
		prompt.WriteString(custom)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString(protocol)
	prompt.WriteString("\n\nAvailable operational tools:")
	allowed := make(map[string]struct{}, len(capabilities))
	for _, tool := range capabilities {
		if tool.Name == "" {
			return "", nil, errors.New("capability name is required")
		}
		allowed[tool.Name] = struct{}{}
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{}`)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, schema); err != nil {
			return "", nil, err
		}
		fmt.Fprintf(&prompt, "\n- %s: %s\n  schema: %s",
			tool.Name, tool.Description, compact.String())
	}
	if len(capabilities) == 0 {
		prompt.WriteString("\n- none; answer without tool calls")
	}
	return prompt.String(), allowed, nil
}

func chat(messages []message) (string, error) {
	payload, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		return "", err
	}
	response, err := dispatch(call{Name: "openai.chat", Args: payload})
	if err != nil {
		return "", err
	}
	if response.Status != "result" {
		return "", errors.New(response.Message)
	}
	var parsed chatResponse
	if err := json.Unmarshal(response.Result, &parsed); err != nil {
		return "", fmt.Errorf("decode OpenAI-compatible response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", errors.New("OpenAI-compatible response has no message content")
	}
	return parsed.Choices[0].Message.Content, nil
}

func decodeActions(content string) (actionBatch, error) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	}
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end < start {
		return actionBatch{}, errors.New("response does not contain a JSON object")
	}
	var batch actionBatch
	if err := json.Unmarshal([]byte(content[start:end+1]), &batch); err != nil {
		return actionBatch{}, err
	}
	if len(batch.Actions) == 0 {
		return actionBatch{}, errors.New("actions must not be empty")
	}
	for _, item := range batch.Actions {
		if item.Action == "" {
			return actionBatch{}, errors.New("action name is required")
		}
	}
	return batch, nil
}

func emitProgress(action string, content json.RawMessage) {
	summary := progressSummary(action, content)
	msg, _ := json.Marshal(map[string]string{"message": summary})
	dispatch(call{Name: "aurora.log", Args: msg})
}

func progressSummary(action string, content json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(content, &fields) != nil {
		return "⚙ " + action
	}
	if strings.HasPrefix(action, "call.") {
		if msg, ok := fields["message"]; ok {
			var s string
			if json.Unmarshal(msg, &s) == nil && len(s) > 0 {
				if len(s) > 80 {
					s = s[:80] + "…"
				}
				return "🔀 " + action + ": " + s
			}
		}
		return "🔀 " + action
	}
	if strings.HasPrefix(action, "k8s.") || strings.HasPrefix(action, "helm.") {
		var parts []string
		for _, key := range []string{"kind", "namespace", "name", "release", "chart"} {
			if raw, ok := fields[key]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil && s != "" {
					parts = append(parts, s)
				}
			}
		}
		if len(parts) > 0 {
			return "⚙ " + action + " " + strings.Join(parts, "/")
		}
	}
	return "⚙ " + action
}

type finishArgs struct {
	Answer string `json:"answer"`
}

// fetchInput retrieves the run input via the agent.input host call so it is
// recorded on the replay journal (making replay deterministic).
func fetchInput() (input, error) {
	response, err := dispatch(call{Name: "agent.input"})
	if err != nil {
		return input{}, err
	}
	if response.Status != "result" {
		return input{}, errors.New(response.Message)
	}
	var in input
	if err := json.Unmarshal(response.Result, &in); err != nil {
		return input{}, err
	}
	return in, nil
}

// finish reports the answer via the agent.finish host call (recorded on the
// journal, where the host reads it from) and signals completion to the host.
func finish(answer string) error {
	args, err := json.Marshal(finishArgs{Answer: answer})
	if err != nil {
		return err
	}
	if _, err := dispatch(call{Name: "agent.finish", Args: args}); err != nil {
		return err
	}
	return pdk.OutputJSON(output{Status: "completed"})
}

func dispatch(request call) (hostResponse, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return hostResponse{}, err
	}
	memory := pdk.AllocateBytes(raw)
	defer memory.Free()
	offset := hostPlay(memory.Offset())
	var response hostResponse
	if err := pdk.JSONFrom(offset, &response); err != nil {
		return hostResponse{}, err
	}
	switch response.Status {
	case "result", "failed":
		return response, nil
	case "yield":
		return hostResponse{}, errYielded
	default:
		return hostResponse{}, fmt.Errorf("unsupported host status %q", response.Status)
	}
}
