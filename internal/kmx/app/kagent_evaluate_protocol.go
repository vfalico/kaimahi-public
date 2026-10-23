package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Decode the blocking Go Task contract, never status-message/history fallbacks.
// Errors deliberately contain no remotely supplied identifiers or payloads.
func parseKagentEvaluationResponse(raw []byte, request kagentEvaluationExecution, input string) (kagentEvaluationExecution, string, error) {
	execution := request
	unavailable := func() (kagentEvaluationExecution, string, error) {
		return execution, "", fmt.Errorf("Kagent evaluation response unavailable")
	}
	if len(raw) > 4<<20 || !utf8.Valid(raw) || !unambiguousSessionsReceiptJSON(raw) {
		return unavailable()
	}
	root, ok := kagentEvaluationObject(raw, "jsonrpc", "id", "result", "error")
	if !ok || kagentEvaluationString(root["jsonrpc"]) != "2.0" || kagentEvaluationString(root["id"]) != request.RPCID {
		return unavailable()
	}
	if _, present := root["error"]; present {
		return unavailable()
	}
	resultRaw := root["result"]
	var union map[string]json.RawMessage
	if json.Unmarshal(resultRaw, &union) == nil && union["task"] != nil {
		normalized, err := normalizeKagentV1Task(resultRaw)
		if err != nil {
			return unavailable()
		}
		resultRaw = normalized
	}
	task, ok := kagentEvaluationObject(resultRaw, "kind", "id", "contextId", "status", "artifacts", "history", "metadata")
	if !ok || kagentEvaluationString(task["kind"]) != "task" {
		return unavailable()
	}
	taskID, contextID := kagentEvaluationString(task["id"]), kagentEvaluationString(task["contextId"])
	status, ok := kagentEvaluationObject(task["status"], "state", "timestamp", "message")
	state := kagentEvaluationString(status["state"])
	if !ok || !safeKagentEvaluationID(taskID) || !safeKagentEvaluationID(contextID) {
		return unavailable()
	}
	switch state {
	case "completed", "failed", "canceled", "rejected", "submitted", "working", "input-required", "auth-required", "unknown":
	default:
		return unavailable()
	}
	execution.TaskID, execution.ContextID, execution.State = taskID, contextID, state
	if message, present := status["message"]; present {
		if _, _, _, ok := kagentEvaluationMessage(message, execution); !ok {
			return unavailable()
		}
	}
	if history, present := task["history"]; present {
		var messages []json.RawMessage
		if json.Unmarshal(history, &messages) != nil || messages == nil {
			return unavailable()
		}
		users := 0
		for _, message := range messages {
			role, id, text, ok := kagentEvaluationMessage(message, execution)
			if !ok {
				return unavailable()
			}
			if role == "user" {
				if id != request.RequestMessageID || text != input {
					return unavailable()
				}
				// v0.10.3 repeats the submitted message in Task history.
				// The same identity/input is one turn, not prior conversation.
				users = 1
			}
		}
		if users != 1 {
			return unavailable()
		}
	}
	if state != "completed" {
		return execution, "", nil
	}
	var artifacts []json.RawMessage
	if json.Unmarshal(task["artifacts"], &artifacts) != nil || len(artifacts) == 0 {
		return unavailable()
	}
	seen := map[string]bool{}
	answer, candidates := "", 0
	for _, rawArtifact := range artifacts {
		artifact, ok := kagentEvaluationObject(rawArtifact, "artifactId", "name", "description", "parts", "metadata", "extensions")
		id := kagentEvaluationString(artifact["artifactId"])
		if !ok || !safeKagentEvaluationID(id) || seen[id] {
			return unavailable()
		}
		seen[id] = true
		text, hasText, ok := kagentEvaluationParts(artifact["parts"])
		if !ok {
			return unavailable()
		}
		if hasText {
			candidates++
			answer = text
		}
	}
	if candidates != 1 || strings.TrimSpace(answer) == "" || strings.IndexFunc(answer, func(r rune) bool { return !unicode.IsPrint(r) && r != '\n' && r != '\t' }) >= 0 {
		return unavailable()
	}
	return execution, answer, nil
}

func kagentEvaluationObject(raw json.RawMessage, fields ...string) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	for key, value := range object {
		allowed := false
		for _, field := range fields {
			if key == field {
				allowed = true
				break
			}
		}
		if !allowed || string(value) == "null" {
			return nil, false
		}
	}
	return object, true
}

func kagentEvaluationString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func kagentEvaluationMessage(raw json.RawMessage, execution kagentEvaluationExecution) (string, string, string, bool) {
	message, ok := kagentEvaluationObject(raw, "kind", "role", "messageId", "taskId", "contextId", "parts", "metadata", "extensions", "referenceTaskIds")
	role, id := kagentEvaluationString(message["role"]), kagentEvaluationString(message["messageId"])
	if !ok || kagentEvaluationString(message["kind"]) != "message" || (role != "agent" && role != "user") || !safeKagentEvaluationID(id) {
		return "", "", "", false
	}
	for _, identity := range []struct{ key, want string }{{"taskId", execution.TaskID}, {"contextId", execution.ContextID}} {
		if value, present := message[identity.key]; present && kagentEvaluationString(value) != identity.want {
			return "", "", "", false
		}
	}
	text, _, ok := kagentEvaluationParts(message["parts"])
	return role, id, text, ok
}

func kagentEvaluationParts(raw json.RawMessage) (string, bool, bool) {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", false, false
	}
	var texts []string
	for _, rawPart := range parts {
		part, ok := kagentEvaluationObject(rawPart, "kind", "text", "data", "metadata")
		if !ok {
			return "", false, false
		}
		switch kagentEvaluationString(part["kind"]) {
		case "text":
			if _, present := part["data"]; present {
				return "", false, false
			}
			var text string
			if value, present := part["text"]; !present || json.Unmarshal(value, &text) != nil {
				return "", false, false
			}
			texts = append(texts, text)
		case "data":
			if _, present := part["text"]; present {
				return "", false, false
			}
			var data map[string]json.RawMessage
			if json.Unmarshal(part["data"], &data) != nil || data == nil {
				return "", false, false
			}
		default:
			return "", false, false
		}
	}
	return strings.Join(texts, "\n"), len(texts) > 0, true
}

// The current fork speaks A2A v1. Normalize only a strictly validated v1
// Task union into the internal legacy representation so the same identity,
// history, duplicate-key and exact-text checks govern both formats. This is
// not a lossy SDK re-marshal: unknown fields and ambiguous parts are refused.
func normalizeKagentV1Task(raw json.RawMessage) (json.RawMessage, error) {
	bad := func() (json.RawMessage, error) { return nil, fmt.Errorf("Kagent v1 response unavailable") }
	union, ok := kagentEvaluationObject(raw, "task")
	if !ok || len(union) != 1 {
		return bad()
	}
	task, ok := kagentEvaluationObject(union["task"], "id", "contextId", "status", "artifacts", "history", "metadata")
	if !ok {
		return bad()
	}
	status, ok := kagentEvaluationObject(task["status"], "state", "timestamp", "message")
	if !ok {
		return bad()
	}
	states := map[string]string{"TASK_STATE_COMPLETED": "completed", "TASK_STATE_FAILED": "failed", "TASK_STATE_CANCELED": "canceled", "TASK_STATE_REJECTED": "rejected", "TASK_STATE_SUBMITTED": "submitted", "TASK_STATE_WORKING": "working", "TASK_STATE_INPUT_REQUIRED": "input-required", "TASK_STATE_AUTH_REQUIRED": "auth-required"}
	state, ok := states[kagentEvaluationString(status["state"])]
	if !ok {
		return bad()
	}
	status["state"], _ = json.Marshal(state)
	if msg, present := status["message"]; present {
		v, e := normalizeKagentV1Message(msg)
		if e != nil {
			return bad()
		}
		status["message"] = v
	}
	task["status"], _ = json.Marshal(status)
	task["kind"] = json.RawMessage(`"task"`)
	if history, present := task["history"]; present {
		var messages []json.RawMessage
		if json.Unmarshal(history, &messages) != nil || messages == nil {
			return bad()
		}
		for i, msg := range messages {
			v, e := normalizeKagentV1Message(msg)
			if e != nil {
				return bad()
			}
			messages[i] = v
		}
		task["history"], _ = json.Marshal(messages)
	}
	if artifacts, present := task["artifacts"]; present {
		var list []json.RawMessage
		if json.Unmarshal(artifacts, &list) != nil || list == nil {
			return bad()
		}
		for i, raw := range list {
			artifact, ok := kagentEvaluationObject(raw, "artifactId", "name", "description", "parts", "metadata", "extensions")
			if !ok {
				return bad()
			}
			parts, e := normalizeKagentV1Parts(artifact["parts"])
			if e != nil {
				return bad()
			}
			artifact["parts"] = parts
			list[i], _ = json.Marshal(artifact)
		}
		task["artifacts"], _ = json.Marshal(list)
	}
	return json.Marshal(task)
}
func normalizeKagentV1Message(raw json.RawMessage) (json.RawMessage, error) {
	msg, ok := kagentEvaluationObject(raw, "role", "messageId", "taskId", "contextId", "parts", "metadata", "extensions", "referenceTaskIds")
	if !ok {
		return nil, fmt.Errorf("invalid v1 message")
	}
	roles := map[string]string{"ROLE_USER": "user", "ROLE_AGENT": "agent"}
	role, ok := roles[kagentEvaluationString(msg["role"])]
	if !ok {
		return nil, fmt.Errorf("invalid v1 role")
	}
	msg["role"], _ = json.Marshal(role)
	msg["kind"] = json.RawMessage(`"message"`)
	parts, err := normalizeKagentV1Parts(msg["parts"])
	if err != nil {
		return nil, err
	}
	msg["parts"] = parts
	return json.Marshal(msg)
}
func normalizeKagentV1Parts(raw json.RawMessage) (json.RawMessage, error) {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return nil, fmt.Errorf("invalid v1 parts")
	}
	for i, raw := range parts {
		part, ok := kagentEvaluationObject(raw, "text", "data", "metadata")
		if !ok {
			return nil, fmt.Errorf("invalid v1 part")
		}
		_, text := part["text"]
		_, data := part["data"]
		if text == data {
			return nil, fmt.Errorf("ambiguous v1 part")
		}
		if text {
			part["kind"] = json.RawMessage(`"text"`)
		} else {
			part["kind"] = json.RawMessage(`"data"`)
		}
		parts[i], _ = json.Marshal(part)
	}
	return json.Marshal(parts)
}
