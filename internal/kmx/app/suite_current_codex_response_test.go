package app

import (
	"encoding/json"
	"os"
	"testing"
)

// Optional real public gateway result, produced by the separately running
// admitted native Codex harness. This is answer-attribution qualification only:
// it does not qualify placement, current-profile receipts, or deployment pins.
func TestCurrentCodexActualGatewayAnswerAttribution(t *testing.T) {
	path := os.Getenv("KMX_CODEX_GATEWAY_RESPONSE")
	if path == "" {
		t.Skip("explicit current native gateway response required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	request := kagentEvaluationExecution{RPCID: "compiled-public", RequestMessageID: "compiled-public-msg"}
	input := "Run the native network probe and answer complete."
	execution, answer, err := parseKagentEvaluationResponse(raw, request, input)
	if err != nil || answer != "complete" || execution.State != "completed" || execution.TaskID == "" || execution.ContextID == "" || !suiteKagentResponseHistory(raw) {
		t.Fatalf("actual current result refused: %+v answer=%q err=%v", execution, answer, err)
	}
	// Data-only native tool artifacts remain evidence, never answer fallbacks.
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	task := root["result"].(map[string]any)["task"].(map[string]any)
	original := task["artifacts"].([]any)
	if len(original) < 3 {
		t.Fatal("real command invocation, result and final answer artifacts required")
	}
	task["artifacts"] = original[:len(original)-1]
	toolOnly, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseKagentEvaluationResponse(toolOnly, request, input); err == nil {
		t.Fatal("tool output substituted for missing final answer")
	}
	task["artifacts"] = original
	task["contextId"] = "wrong-context"
	wrong, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseKagentEvaluationResponse(wrong, request, input); err == nil {
		t.Fatal("cross-context native answer accepted")
	}
	if _, _, err := parseKagentEvaluationResponse(raw, request, "other input"); err == nil {
		t.Fatal("wrong authored input accepted")
	}
	t.Log("CURRENT_CODEX_ACTUAL_GATEWAY_ANSWER_ATTRIBUTION_PASS final text only exact RPC message Task context and user history; no public placement or receipt qualification claimed")
}
