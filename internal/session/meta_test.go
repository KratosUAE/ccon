package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMetaPath(t *testing.T) {
	got := MetaPath("/x/subagents/agent-a1a73a07.jsonl")
	want := "/x/subagents/agent-a1a73a07.meta.json"
	if got != want {
		t.Errorf("MetaPath()=%q, ожидалось %q", got, want)
	}
}

// Состав .meta.json подтверждён по живому корпусу: те же четыре поля в том же
// порядке, значения здесь синтетические.
func TestReadMeta(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "agent-a1a73a07.jsonl")
	body := `{"agentType":"kotlin-verifier","description":"Review the parser changes","toolUseId":"toolu_01RqKs","spawnDepth":1}`
	if err := os.WriteFile(MetaPath(transcript), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMeta(transcript)
	if err != nil {
		t.Fatalf("ReadMeta вернул ошибку: %v", err)
	}
	if got.AgentType != "kotlin-verifier" {
		t.Errorf("AgentType=%q", got.AgentType)
	}
	if got.Description != "Review the parser changes" {
		t.Errorf("Description=%q", got.Description)
	}
	if got.ToolUseID != "toolu_01RqKs" {
		t.Errorf("ToolUseID=%q", got.ToolUseID)
	}
	if got.SpawnDepth != 1 {
		t.Errorf("SpawnDepth=%d", got.SpawnDepth)
	}
}

func TestReadMetaFailures(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name string
		body string // пусто — файла нет вовсе
	}{
		{name: "файла нет"},
		{name: "битый json", body: "{не json"},
		{name: "пустой файл", body: ""},
		{name: "нет agentType", body: `{"description":"без типа"}`},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transcript := filepath.Join(dir, "agent-"+string(rune('a'+i))+".jsonl")
			if tt.name != "файла нет" {
				if err := os.WriteFile(MetaPath(transcript), []byte(tt.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			_, err := ReadMeta(transcript)
			if !errors.Is(err, ErrNoMeta) {
				t.Errorf("ошибка %v, ожидалась ErrNoMeta", err)
			}
		})
	}
}
