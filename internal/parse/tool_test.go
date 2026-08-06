package parse

import (
	"strings"
	"testing"
)

// Имена взяты из живого корпуса: разбор обязан переживать
// дефисы и подчёркивания в обеих половинах, длину до предела обрезки и
// обрезки правил разрешений, которые именами вызовов не являются.
func TestMCPParts(t *testing.T) {
	tests := []struct {
		name       string
		tool       string
		wantServer string
		wantMethod string
		wantOK     bool
	}{
		{"базовое имя", "mcp__serena__find_symbol", "serena", "find_symbol", true},
		{"дефис в методе", "mcp__context7__resolve-library-id", "context7", "resolve-library-id", true},
		{"подчёркивания в сервере", "mcp__plugin_sentry_sentry__authenticate", "plugin_sentry_sentry", "authenticate", true},
		{"подчёркивания с обеих сторон", "mcp__plugin_playwright_playwright__browser_take_screenshot",
			"plugin_playwright_playwright", "browser_take_screenshot", true},
		{"дефис в сервере, длина 60", "mcp__plugin_ecc_chrome-devtools__performance_analyze_insight",
			"plugin_ecc_chrome-devtools", "performance_analyze_insight", true},
		{"CamelCase и цифры в сервере, длина 60", "mcp__acme_office_suite__create_reply_all_draft_from_messages",
			"acme_office_suite", "create_reply_all_draft_from_messages", true},
		{"метод дублирует сервер", "mcp__grepai__grepai_trace_callees", "grepai", "grepai_trace_callees", true},
		{"__ внутри метода уходит в метод целиком", "mcp__srv__method__with__underscores",
			"srv", "method__with__underscores", true},
		{"пустой метод — правило разрешений, не вызов", "mcp__claude-in-chrome__", "", "", false},
		{"нет второго разделителя", "mcp__serena", "", "", false},
		{"пустой сервер", "mcp____method", "", "", false},
		{"не MCP: Bash", "Bash", "", "", false},
		{"не MCP: Read", "Read", "", "", false},
		{"пустое имя", "", "", "", false},
		{"одна приставка", "mcp__", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, method, ok := MCPParts(tt.tool)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, ожидалось %v", ok, tt.wantOK)
			}
			if server != tt.wantServer {
				t.Errorf("server=%q, ожидался %q", server, tt.wantServer)
			}
			if method != tt.wantMethod {
				t.Errorf("method=%q, ожидался %q", method, tt.wantMethod)
			}
		})
	}
}

// Имя длиннее предела обрезки обязано ломаться ВИДИМО: метод приходит с
// хвостовым многоточием, а не тихо неверным. Граница задокументирована, а не
// случайна: обрезка стоит между сырым именем и любым его разбором.
func TestMCPPartsAtTruncationBoundary(t *testing.T) {
	// Имя ровно в предел разбирается без потерь.
	exact := mcpPrefix + "srv__" + strings.Repeat("m", maxToolName-len(mcpPrefix)-len("srv__"))
	if len([]rune(exact)) != maxToolName {
		t.Fatalf("длина эталонного имени %d, ожидалось %d", len([]rune(exact)), maxToolName)
	}
	if got := Truncate(exact, maxToolName); got != exact {
		t.Fatalf("имя в предел обрезано: %q", got)
	}
	if _, method, ok := MCPParts(Truncate(exact, maxToolName)); !ok || strings.HasSuffix(method, "…") {
		t.Errorf("имя длиной %d разобрано с потерей: method=%q ok=%v", maxToolName, method, ok)
	}

	// Имя сверх предела: сервер цел, метод обрезан и это видно глазом.
	long := exact + "MMM"
	server, method, ok := MCPParts(Truncate(long, maxToolName))
	if !ok {
		t.Fatalf("имя длиной %d не разобралось вовсе", len([]rune(long)))
	}
	if server != "srv" {
		t.Errorf("server=%q, ожидался srv", server)
	}
	if !strings.HasSuffix(method, "…") {
		t.Errorf("метод обрезан молча: %q", method)
	}
}

// Нулевое значение исхода — «результата ещё нет»: незакрытый вызов не должен
// называться успешным ни в сообщении, ни в колонке.
func TestStatusString(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusPending, "pending"},
		{StatusOK, "ok"},
		{StatusError, "err"},
		{StatusDenied, "denied"},
		{Status(42), "pending"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.status.String(); got != tt.want {
				t.Errorf("String()=%q, ожидалось %q", got, tt.want)
			}
		})
	}

	var zero Status
	if zero != StatusPending {
		t.Errorf("нулевое значение исхода %v, ожидалось pending", zero)
	}
}

func TestFileOp(t *testing.T) {
	tests := []struct {
		tool   string
		want   rune
		wantOK bool
	}{
		{"Read", 'R', true},
		{"Edit", 'E', true},
		{"Write", 'W', true},
		{"NotebookEdit", 'N', true},
		{"MultiEdit", 'E', true},
		// Artifact показывает файл пользователю, а не читает и не пишет его:
		// file_path у него есть, но в окне файлов ему не место.
		{"Artifact", 0, false},
		{"Bash", 0, false},
		{"Grep", 0, false},
		{"mcp__serena__replace_symbol_body", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			op, ok := FileOp(tt.tool)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, ожидалось %v", ok, tt.wantOK)
			}
			if ok && op != tt.want {
				t.Errorf("операция %q, ожидалась %q", string(op), string(tt.want))
			}
		})
	}
}

// Фильтр обязан видеть ровно то, что показано в строке: источник, инструмент,
// путь, деталь и текст ошибки — на любом табе.
func TestHaystack(t *testing.T) {
	ev := Event{
		Source: "go-code-adapter",
		Tool:   "mcp__serena__find_symbol",
		Path:   "/home/user/proj/internal/parse/decode.go",
		Detail: "decode.go",
		Fail:   "upstream: status 429",
	}

	stack := Haystack(ev)
	for _, want := range []string{"go-code-adapter", "serena", "find_symbol", "decode.go", "internal/parse", "429"} {
		if !strings.Contains(stack, want) {
			t.Errorf("в стоге нет %q: %q", want, stack)
		}
	}

	// Пустые части не должны оставлять двойных пробелов: по стогу ищут
	// подстроку, и лишний пробел рвал бы совпадение через границу полей.
	sparse := Haystack(Event{Source: "main", Detail: "ls"})
	if sparse != "main ls" {
		t.Errorf("стог разреженного события %q, ожидалось %q", sparse, "main ls")
	}
	if got := Haystack(Event{}); got != "" {
		t.Errorf("стог пустого события %q, ожидался пустым", got)
	}
}

// TestHaystackSeesKindLabel — фильтр обязан находить строки по ярлыку,
// который РЕАЛЬНО показан в колонке (PartsFor.label), а не только по
// ev.Tool: у text/error/system/fallback ev.Tool пуст, а ярлык виден глазом.
func TestHaystackSeesKindLabel(t *testing.T) {
	tests := []struct {
		name   string
		kind   Kind
		needle string
	}{
		{"ERROR", KindError, "error"},
		{"system", KindSystem, "system"},
		{"swap", KindFallback, "swap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := Event{Source: "main", Kind: tt.kind, Detail: "деталь"}
			stack := strings.ToLower(Haystack(ev))
			if !strings.Contains(stack, tt.needle) {
				t.Errorf("стог %q не содержит %q — фильтр не найдёт строку с видимым ярлыком", stack, tt.needle)
			}
		})
	}
}
