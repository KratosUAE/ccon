package parse

import (
	"strings"
	"testing"
)

// system/<subtype> — это ПАРА полей type+subtype, а не литерал типа записи.
// Семь подтипов подтверждены выборкой по живому корпусу.
func TestDecodeSystemSubtypes(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantKind   Kind
		wantDetail []string // подстроки, обязанные быть в детали
		wantLevel  string
	}{
		{
			name:       "turn_duration показывает длительность хода",
			line:       `{"type":"system","subtype":"turn_duration","timestamp":"2026-08-04T09:00:01Z","durationMs":274440,"messageCount":91}`,
			wantKind:   KindSystem,
			wantDetail: []string{"4m34s", "91"},
		},
		{
			name:       "away_summary показывает сводку",
			line:       `{"type":"system","subtype":"away_summary","timestamp":"2026-08-04T09:00:02Z","content":"Сводка: перенесли конфиг на zsh."}`,
			wantKind:   KindSystem,
			wantDetail: []string{"Сводка: перенесли конфиг на zsh."},
		},
		{
			name:       "local_command показывает имя команды",
			line:       `{"type":"system","subtype":"local_command","timestamp":"2026-08-04T09:00:03Z","level":"info","content":"<command-name>/context</command-name>\n  <command-message>context</command-message>\n  <command-args></command-args>"}`,
			wantKind:   KindSystem,
			wantDetail: []string{"/context"},
			wantLevel:  "info",
		},
		{
			name:       "compact_boundary показывает сжатие контекста",
			line:       `{"type":"system","subtype":"compact_boundary","timestamp":"2026-08-04T09:00:04Z","level":"info","content":"Conversation compacted","compactMetadata":{"trigger":"manual","preTokens":385077,"postTokens":24939}}`,
			wantKind:   KindSystem,
			wantDetail: []string{"385077", "24939", "manual"},
			wantLevel:  "info",
		},
		{
			name:       "stop_hook_summary показывает число хуков, а не их код",
			line:       `{"type":"system","subtype":"stop_hook_summary","timestamp":"2026-08-04T09:00:07Z","level":"info","hookCount":6,"hookInfos":[{"command":"node -e \"огромный скрипт\""}]}`,
			wantKind:   KindSystem,
			wantDetail: []string{"6"},
			wantLevel:  "info",
		},
		{
			name:       "informational показывает содержимое",
			line:       `{"type":"system","subtype":"informational","timestamp":"2026-08-04T09:00:06Z","level":"warning","content":"Unknown command: /remember"}`,
			wantKind:   KindSystem,
			wantDetail: []string{"Unknown command: /remember"},
			wantLevel:  "warning",
		},
		{
			// Поля подмены лежат на ВЕРХНЕМ уровне записи, не в message.
			name:       "model_refusal_fallback показывает обе модели и причину",
			line:       `{"type":"system","subtype":"model_refusal_fallback","timestamp":"2026-08-04T09:00:05Z","level":"warning","originalModel":"claude-fable-5","fallbackModel":"claude-opus-5","apiRefusalCategory":"cyber","content":"safeguards flagged"}`,
			wantKind:   KindFallback,
			wantDetail: []string{"claude-fable-5", "claude-opus-5", "cyber"},
			wantLevel:  "warning",
		},
		{
			name:       "неизвестный подтип не теряется молча",
			line:       `{"type":"system","subtype":"новый_подтип","timestamp":"2026-08-04T09:00:08Z","content":"что-то новое"}`,
			wantKind:   KindSystem,
			wantDetail: []string{"новый_подтип"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := Decode([]byte(tt.line))
			if !ok {
				t.Fatalf("Decode вернул ok=false")
			}
			if d.Usage != nil {
				t.Errorf("системная запись дала расход %+v", d.Usage)
			}
			if len(d.Events) != 1 {
				t.Fatalf("событий %d (%+v), ожидалось одно", len(d.Events), d.Events)
			}

			ev := d.Events[0]
			if ev.Kind != tt.wantKind {
				t.Errorf("Kind=%v, ожидался %v", ev.Kind, tt.wantKind)
			}
			for _, want := range tt.wantDetail {
				if !strings.Contains(ev.Detail, want) {
					t.Errorf("Detail=%q, в нём нет %q", ev.Detail, want)
				}
			}
			if tt.wantLevel != "" && ev.Level != tt.wantLevel {
				t.Errorf("Level=%q, ожидался %q", ev.Level, tt.wantLevel)
			}
			if ev.Source != "main" {
				t.Errorf("Source=%q, ожидался main", ev.Source)
			}
			if ev.Time.IsZero() {
				t.Errorf("время события не разобрано")
			}
		})
	}
}

// Системная запись без полезных полей не должна ронять разбор.
func TestDecodeSystemTolerance(t *testing.T) {
	lines := []string{
		`{"type":"system"}`,
		`{"type":"system","subtype":"turn_duration"}`,
		`{"type":"system","subtype":"compact_boundary","compactMetadata":null}`,
		`{"type":"system","subtype":"model_refusal_fallback"}`,
		`{"type":"system","subtype":"stop_hook_summary","hookInfos":null}`,
		`{"type":"system","subtype":"local_command","content":"<command-name>оборвано"}`,
	}

	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			if _, ok := Decode([]byte(line)); !ok {
				t.Errorf("Decode вернул ok=false")
			}
		})
	}
}

// Автономные типы записей — тихий пропуск: ноль событий, ноль расхода, ноль паник.
func TestDecodeAutonomousTypes(t *testing.T) {
	lines := map[string]string{
		"last-prompt":           `{"type":"last-prompt","lastPrompt":"проверь порты","leafUuid":"u1"}`,
		"mode":                  `{"type":"mode","mode":"plan"}`,
		"permission-mode":       `{"type":"permission-mode","permissionMode":"acceptEdits"}`,
		"bridge-session":        `{"type":"bridge-session","bridgeSessionId":"b1"}`,
		"ai-title":              `{"type":"ai-title","title":"починка сборки"}`,
		"agent-name":            `{"type":"agent-name","name":"go-code-adapter"}`,
		"file-history-snapshot": `{"type":"file-history-snapshot","snapshot":{"files":[]}}`,
		"file-history-delta":    `{"type":"file-history-delta","delta":{"a":1}}`,
		"queue-operation":       `{"type":"queue-operation","operation":"enqueue"}`,
		"attachment":            `{"type":"attachment","attachment":{"type":"queued_command","prompt":"привет"}}`,
	}

	for name, line := range lines {
		t.Run(name, func(t *testing.T) {
			d, ok := Decode([]byte(line))
			if !ok {
				t.Fatalf("Decode вернул ok=false")
			}
			if len(d.Events) != 0 {
				t.Errorf("событий %d (%+v), ожидался тихий пропуск", len(d.Events), d.Events)
			}
			if d.Usage != nil {
				t.Errorf("расход %+v, ожидался nil", d.Usage)
			}
		})
	}
}

// is_error бывает и от локального хука: поле toolDenialKind лежит на верхнем
// уровне записи user. В корпусе три значения, а не одно.
func TestDecodeToolDenialKind(t *testing.T) {
	tests := []struct {
		denial string
	}{
		{"permission-rule"},
		{"user-rejected"},
		{"automode-blocked"},
	}

	for _, tt := range tests {
		t.Run(tt.denial, func(t *testing.T) {
			line := `{"type":"user","timestamp":"2026-08-04T09:00:10Z","toolDenialKind":"` + tt.denial +
				`","message":{"content":[{"type":"tool_result","is_error":true,"content":"отказано"}]}}`

			d, ok := Decode([]byte(line))
			if !ok || len(d.Events) != 1 {
				t.Fatalf("ok=%v, событий %d", ok, len(d.Events))
			}
			if d.Events[0].Kind != KindError {
				t.Errorf("Kind=%v, ожидался KindError", d.Events[0].Kind)
			}
			if d.Events[0].Denial != tt.denial {
				t.Errorf("Denial=%q, ожидался %q", d.Events[0].Denial, tt.denial)
			}
		})
	}
}

// Настоящая ошибка инструмента поля отказа не несёт.
func TestDecodeErrorWithoutDenial(t *testing.T) {
	line := `{"type":"user","timestamp":"2026-08-04T09:00:10Z","message":{"content":[{"type":"tool_result","is_error":true,"content":"No such tool"}]}}`

	d, _ := Decode([]byte(line))
	if len(d.Events) != 1 {
		t.Fatalf("событий %d", len(d.Events))
	}
	if d.Events[0].Denial != "" {
		t.Errorf("Denial=%q, ожидалась пустая строка", d.Events[0].Denial)
	}
}

// Структурный content не должен утекать в лог сырым JSON: в нём могут быть
// вложенные объекты и чужие данные.
func TestDecodeSystemNonStringContent(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"объект", `{"type":"system","subtype":"informational","timestamp":"2026-08-04T09:00:01Z","content":{"nested":"object","secret":[1,2]}}`},
		{"массив", `{"type":"system","subtype":"away_summary","timestamp":"2026-08-04T09:00:01Z","content":[{"secret":"утечка"}]}`},
		{"число", `{"type":"system","subtype":"informational","timestamp":"2026-08-04T09:00:01Z","content":42}`},
		{"null", `{"type":"system","subtype":"informational","timestamp":"2026-08-04T09:00:01Z","content":null}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := Decode([]byte(tt.line))
			if !ok || len(d.Events) != 1 {
				t.Fatalf("ok=%v, событий %d", ok, len(d.Events))
			}

			detail := d.Events[0].Detail
			for _, leak := range []string{"nested", "secret", "утечка", "{", "["} {
				if strings.Contains(detail, leak) {
					t.Errorf("в детали %q утёк сырой JSON (%q)", detail, leak)
				}
			}
			if detail == "" {
				t.Errorf("деталь пуста: подтип должен остаться виден")
			}
		})
	}
}

func TestHumanMillis(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{449, "0.4s"},
		{451, "0.5s"}, // округление, не отбрасывание
		{1500, "1.5s"},
		{59_900, "59.9s"},
		{59_949, "59.9s"},
		{59_950, "1m0s"}, // округление не должно давать «60.0с»
		{60_000, "1m0s"},
		{274_440, "4m34s"},
		{3_599_000, "59m59s"},
		{3_600_000, "1h0m"}, // от часа — своя ветка, не «60м0с»
		{86_400_000, "24h0m"},
		{-5, "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := humanMillis(tt.ms); got != tt.want {
				t.Errorf("humanMillis(%d)=%q, ожидалось %q", tt.ms, got, tt.want)
			}
		})
	}
}
