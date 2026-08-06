package parse

import (
	"testing"
	"unicode/utf8"
)

// FuzzDecode: произвольный байтовый вход не должен ронять декодер и не должен
// протаскивать наружу управляющие символы или неразобранное время.
func FuzzDecode(f *testing.F) {
	seeds := []string{
		`{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","message":{"id":"m","content":[{"type":"text","text":"привет"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","toolDenialKind":"permission-rule","message":{"content":[{"type":"tool_result","is_error":true,"content":[{"type":"text","text":"ой"}]}]}}`,
		`{"type":"system","subtype":"model_refusal_fallback","originalModel":"a","fallbackModel":"b"}`,
		`{"type":"system","subtype":"turn_duration","durationMs":-1}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":-9223372036854775808}}}`,
		`{"type":"queue-operation"}`,
		`{}`,
		`null`,
		``,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"` + "\x1b" + `[31mкрасный"}]}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		d, ok := Decode(data)
		events, usage := d.Events, d.Usage
		if !ok {
			if len(events) != 0 || usage != nil || len(d.Results) != 0 {
				t.Fatalf("при ok=false вернулись данные: %d событий, %d результатов, usage=%v",
					len(events), len(d.Results), usage)
			}
			return
		}

		// Инварианты держатся и на корме линкера: текст результата обрезается
		// при декоде (сырой content доходит до сотен килобайт), а всё, что
		// пришло извне, вычищено от управляющих символов ещё до показа.
		l := NewLinker()
		for _, r := range d.Results {
			if n := len([]rune(r.Text)); n > maxError+1 {
				t.Fatalf("текст результата не обрезан: %d рун", n)
			}
			for _, value := range []string{r.Text, r.Denial} {
				for _, ch := range value {
					if ch < 0x20 || ch == 0x7f || (ch >= 0x80 && ch <= 0x9f) {
						t.Fatalf("в результате (%q) остался управляющий символ %U", value, ch)
					}
				}
				if !utf8.ValidString(value) {
					t.Fatalf("поле результата (%q) не является валидным UTF-8", value)
				}
			}

			// Сшивка произвольного результата не должна ни падать, ни
			// придумывать обновление на пустом месте.
			if u, sewn := l.Resolve(r); sewn {
				t.Fatalf("результат сшился без единого вызова на учёте: %+v", u)
			}
		}

		// Инвариант держится на ВСЕХ текстовых полях события, а не только на
		// детали: имена инструментов приходят от MCP-серверов, имена агентов —
		// из конфигов, и то и другое так же управляемо извне.
		for _, ev := range events {
			fields := map[string]string{
				"Detail":   ev.Detail,
				"Source":   ev.Source,
				"Tool":     ev.Tool,
				"Level":    ev.Level,
				"Denial":   ev.Denial,
				"Model":    ev.Model,
				"Effort":   ev.Effort,
				"Path":     ev.Path,
				"Line":     Line(ev),
				"Haystack": Haystack(ev),
			}
			for name, value := range fields {
				for _, r := range value {
					if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
						t.Fatalf("в поле %s (%q) остался управляющий символ %U", name, value, r)
					}
				}
				if !utf8.ValidString(value) {
					t.Fatalf("поле %s (%q) не является валидным UTF-8", name, value)
				}
			}
		}

		if usage != nil {
			for _, v := range []int64{usage.Input, usage.Output, usage.CacheRead, usage.Cache5m, usage.Cache1h, usage.WebSearch} {
				if v < 0 || v > MaxTokens {
					t.Fatalf("счётчик %d вне диапазона [0, %d]", v, MaxTokens)
				}
			}
			if usage.Total() < 0 {
				t.Fatalf("Total()=%d переполнился", usage.Total())
			}
		}
	})
}
