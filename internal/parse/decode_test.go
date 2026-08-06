package parse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type wantEvent struct {
	kind   Kind
	tool   string
	detail string
}

func TestDecodeEvents(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []wantEvent
	}{
		{
			name: "реплика ассистента: пробелы схлопнуты",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","model":"claude-opus-5","content":[{"type":"text","text":"Сейчас  проверю\nсборку."}]}}`,
			want: []wantEvent{{KindText, "", "Сейчас проверю сборку."}},
		},
		{
			name: "пустая реплика события не даёт",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"text","text":"   \n "}]}}`,
			want: nil,
		},
		{
			name: "thinking события не даёт",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"thinking","thinking":"надо подумать"}]}}`,
			want: nil,
		},
		{
			name: "Bash показывает команду",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Bash","input":{"command":"go build ./...   &&\n echo OK"}}]}}`,
			want: []wantEvent{{KindTool, "Bash", "go build ./... && echo OK"}},
		},
		{
			name: "Read показывает две последние компоненты пути",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/home/user/proj/internal/app/app.go"}}]}}`,
			want: []wantEvent{{KindTool, "Read", "app/app.go"}},
		},
		{
			name: "Write с путём из одной компоненты не паникует",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Write","input":{"file_path":"/foo"}}]}}`,
			want: []wantEvent{{KindTool, "Write", "foo"}},
		},
		{
			name: "Edit без file_path даёт пустую деталь",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Edit","input":{}}]}}`,
			want: []wantEvent{{KindTool, "Edit", ""}},
		},
		{
			name: "Grep показывает шаблон",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Grep","input":{"pattern":"func main","path":"."}}]}}`,
			want: []wantEvent{{KindTool, "Grep", "func main"}},
		},
		{
			name: "Agent это делегирование",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Agent","input":{"subagent_type":"go-code-adapter","description":"Фикс потери токенов"}}]}}`,
			want: []wantEvent{{KindDelegate, "Agent", "go-code-adapter Фикс потери токенов"}},
		},
		{
			name: "Task тоже делегирование",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Task","input":{"subagent_type":"go-linter","description":"линт"}}]}}`,
			want: []wantEvent{{KindDelegate, "Task", "go-linter линт"}},
		},
		{
			name: "Agent без subagent_type даёт вопрос",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Agent","input":{"description":"без типа"}}]}}`,
			want: []wantEvent{{KindDelegate, "Agent", "? без типа"}},
		},
		{
			name: "Skill показывает имя скилла",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"Skill","input":{"skill":"superpowers:brainstorming"}}]}}`,
			want: []wantEvent{{KindSkill, "Skill", "superpowers:brainstorming"}},
		},
		{
			name: "прочий инструмент показывает сжатый JSON входа",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"AskUserQuestion","input":{"question":"куда?"}}]}}`,
			want: []wantEvent{{KindTool, "AskUserQuestion", `{"question":"куда?"}`}},
		},
		{
			name: "инструмент без input не паникует",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","name":"ToolSearch"}]}}`,
			want: []wantEvent{{KindTool, "ToolSearch", ""}},
		},
		{
			name: "несколько блоков в одной записи дают несколько событий",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m1","content":[{"type":"thinking","thinking":"ага"},{"type":"text","text":"поехали"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`,
			want: []wantEvent{{KindText, "", "поехали"}, {KindTool, "Bash", "ls"}},
		},
		{
			name: "ошибка инструмента строкой",
			line: `{"type":"user","timestamp":"2026-08-03T10:00:01Z","message":{"content":[{"type":"tool_result","is_error":true,"content":"No such tool  available:\nTask"}]}}`,
			want: []wantEvent{{KindError, "", "No such tool available: Task"}},
		},
		{
			name: "ошибка инструмента массивом блоков даёт ту же деталь",
			line: `{"type":"user","timestamp":"2026-08-03T10:00:01Z","message":{"content":[{"type":"tool_result","is_error":true,"content":[{"type":"text","text":"No such tool  available:\nTask"}]}]}}`,
			want: []wantEvent{{KindError, "", "No such tool available: Task"}},
		},
		{
			name: "успешный tool_result события не даёт",
			line: `{"type":"user","timestamp":"2026-08-03T10:00:01Z","message":{"content":[{"type":"tool_result","is_error":false,"content":"ok"}]}}`,
			want: nil,
		},
		{
			name: "промпт пользователя строкой события не даёт",
			line: `{"type":"user","timestamp":"2026-08-03T10:00:01Z","message":{"content":"почини сборку"}}`,
			want: nil,
		},
		{
			name: "неизвестный тип записи пропускается",
			line: `{"type":"file-history-delta","timestamp":"2026-08-03T10:00:01Z","delta":{"a":1}}`,
			want: nil,
		},
		{
			name: "вложение пропускается",
			line: `{"type":"attachment","timestamp":"2026-08-03T10:00:01Z","attachment":{"type":"queued_command","prompt":"привет"}}`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := Decode([]byte(tt.line))
			if !ok {
				t.Fatalf("Decode вернул ok=false на валидной строке")
			}
			events := d.Events
			if len(events) != len(tt.want) {
				t.Fatalf("событий %d (%+v), ожидалось %d", len(events), events, len(tt.want))
			}
			for i, w := range tt.want {
				got := events[i]
				if got.Kind != w.kind {
					t.Errorf("событие %d: Kind=%v, ожидался %v", i, got.Kind, w.kind)
				}
				if got.Tool != w.tool {
					t.Errorf("событие %d: Tool=%q, ожидался %q", i, got.Tool, w.tool)
				}
				if got.Detail != w.detail {
					t.Errorf("событие %d: Detail=%q, ожидалась %q", i, got.Detail, w.detail)
				}
			}
		})
	}
}

// decodeFixture прогоняет фикстуру целиком: тесты работают на настоящей форме
// записей, а не на выдуманной.
func decodeFixture(t *testing.T, name string) []Event {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("фикстура не открылась: %v", err)
	}
	defer func() { _ = f.Close() }()

	var out []Event
	skipped, err := Scan(f, func(line []byte) error {
		d, ok := Decode(line)
		if !ok {
			t.Errorf("строка фикстуры не разобралась: %s", line)
			return nil
		}
		out = append(out, d.Events...)
		return nil
	})
	if err != nil {
		t.Fatalf("чтение фикстуры: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("пропущено строк: %d", skipped)
	}
	return out
}

// Ключ сшивки и полный путь снимаются с записей живого корпуса: вызовы MCP,
// файловые операции, отказ, незакрытый вызов и Read с нераспарсенными
// аргументами.
func TestDecodeToolIDAndPath(t *testing.T) {
	want := []struct {
		kind   Kind
		tool   string
		toolID string
		path   string
		detail string
	}{
		{KindTool, "mcp__serena__find_symbol", "toolu_01Aw6e6LSYvQvwsVL2ngbZ1y", "",
			`{"name_path":"ViewModel/handleCapture","relative_path":"app/src/main/java/x…`},
		{KindTool, "mcp__netscan__netscan_client_roaming_path", "toolu_01VXZugnrTEX6ZRYAZhVXueY", "",
			`{"mac":"00:00:5e:00:53:00","window":"720h"}`},
		{KindError, "", "", "", "netscan: roaming path upstream query: upstream: status 429 Too Many Requests: too many outstanding requests"},
		// Deталь файловой операции остаётся парой последних компонент: вид
		// транскрипта и текстовый дамп от появления Path не меняются.
		{KindTool, "Edit", "toolu_016XrbPAAqTjo9UMTWnfPQLJ", "/home/user/.ssh/config", ".ssh/config"},
		{KindError, "", "", "", "<tool_use_error>File has not been read yet. Read it first before writing to it.</tool_use_error>"},
		{KindTool, "Read", "toolu_01Read0000000000000001", "/home/user/Devs/proj/internal/parse/decode.go", "parse/decode.go"},
		// Битый JSON аргументов: инструмент файловый, а пути в нём нет —
		// вылавливать его из обрезка не надо, «?» рисует интерфейс.
		{KindTool, "Read", "toolu_01CFJBuxvu6u73HMc3xkib2J", "", ""},
		{KindTool, "Write", "toolu_01Deny0000000000000001", "/home/user/Devs/proj/README.md", "proj/README.md"},
		{KindError, "", "", "", "[Policy Gate] Before creating /home/user/Devs/proj/README.md, present these facts:"},
		// Незакрытый вызов: ключ сшивки есть, результата в файле нет.
		{KindTool, "AskUserQuestion", "toolu_01LxKLTGtabXBsSbWfzjGg3F", "", `{"questions":[{"question":"?"}]}`},
	}

	events := decodeFixture(t, "tools.jsonl")
	if len(events) != len(want) {
		t.Fatalf("событий %d, ожидалось %d: %+v", len(events), len(want), events)
	}

	for i, w := range want {
		ev := events[i]
		if ev.Kind != w.kind {
			t.Errorf("событие %d: Kind=%v, ожидался %v", i, ev.Kind, w.kind)
		}
		if ev.Tool != w.tool {
			t.Errorf("событие %d: Tool=%q, ожидался %q", i, ev.Tool, w.tool)
		}
		if ev.ToolID != w.toolID {
			t.Errorf("событие %d: ToolID=%q, ожидался %q", i, ev.ToolID, w.toolID)
		}
		if ev.Path != w.path {
			t.Errorf("событие %d: Path=%q, ожидался %q", i, ev.Path, w.path)
		}
		if ev.Detail != w.detail {
			t.Errorf("событие %d: Detail=%q, ожидалась %q", i, ev.Detail, w.detail)
		}
		// Исход ставит линкер по результату; декодер о нём не знает.
		if ev.Status != StatusPending || ev.Dur != 0 || ev.Fail != "" {
			t.Errorf("событие %d: декодер проставил исход: %v %v %q", i, ev.Status, ev.Dur, ev.Fail)
		}
	}
}

// Результат отдаётся линкеру КАЖДЫМ блоком tool_result, а событием становится
// только ошибка: транскрипт красные строки не теряет, но и успешный вызов
// обязан узнать свой исход, иначе он навсегда останется «выполняется».
func TestDecodeResults(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantEvents  int
		wantResults int
		wantID      string
		wantIsError bool
		wantDenial  string
		wantText    string
	}{
		{
			name:        "is_error отсутствует — результат есть, события нет",
			line:        `{"type":"user","timestamp":"2026-08-04T10:00:00.194Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ok","content":"готово"}]}}`,
			wantEvents:  0,
			wantResults: 1,
			wantID:      "toolu_ok",
			wantText:    "готово",
		},
		{
			name:        "is_error false — то же самое, но поле есть",
			line:        `{"type":"user","timestamp":"2026-08-04T10:00:00.194Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_false","is_error":false,"content":"80\tfunc x() {"}]}}`,
			wantEvents:  0,
			wantResults: 1,
			wantID:      "toolu_false",
			wantText:    "80 func x() {",
		},
		{
			name:        "is_error true — и результат, и красная строка лога",
			line:        `{"type":"user","timestamp":"2026-08-03T12:45:25.060Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_err","is_error":true,"content":"upstream: status 429"}]}}`,
			wantEvents:  1,
			wantResults: 1,
			wantID:      "toolu_err",
			wantIsError: true,
			wantText:    "upstream: status 429",
		},
		{
			name:        "отказ правила приезжает с верхнего уровня записи",
			line:        `{"type":"user","timestamp":"2026-08-04T11:00:00.120Z","toolDenialKind":"permission-rule","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_deny","is_error":true,"content":"[Policy Gate]"}]}}`,
			wantEvents:  1,
			wantResults: 1,
			wantID:      "toolu_deny",
			wantIsError: true,
			wantDenial:  "permission-rule",
			wantText:    "[Policy Gate]",
		},
		{
			name:        "content массивом блоков разворачивается в текст",
			line:        `{"type":"user","timestamp":"2026-08-04T10:00:00.194Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_arr","content":[{"type":"text","text":"первый"},{"type":"text","text":"второй"}]}]}}`,
			wantEvents:  0,
			wantResults: 1,
			wantID:      "toolu_arr",
			wantText:    "первый второй",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := Decode([]byte(tt.line))
			if !ok {
				t.Fatalf("Decode вернул ok=false на валидной строке")
			}
			if len(d.Events) != tt.wantEvents {
				t.Errorf("событий %d, ожидалось %d: %+v", len(d.Events), tt.wantEvents, d.Events)
			}
			if len(d.Results) != tt.wantResults {
				t.Fatalf("результатов %d, ожидалось %d: %+v", len(d.Results), tt.wantResults, d.Results)
			}

			r := d.Results[0]
			if r.ToolUseID != tt.wantID {
				t.Errorf("ToolUseID=%q, ожидался %q", r.ToolUseID, tt.wantID)
			}
			if r.IsError != tt.wantIsError {
				t.Errorf("IsError=%v, ожидалось %v", r.IsError, tt.wantIsError)
			}
			if r.Denial != tt.wantDenial {
				t.Errorf("Denial=%q, ожидался %q", r.Denial, tt.wantDenial)
			}
			if r.Text != tt.wantText {
				t.Errorf("Text=%q, ожидался %q", r.Text, tt.wantText)
			}
			if r.Time.IsZero() {
				t.Errorf("у результата нет отметки времени: без неё нечем считать длительность")
			}
		})
	}
}

// Содержимое результата обрезается ЗДЕСЬ, до попадания в структуру: в корпусе
// оно доходит до 147 КБ, а в памяти живёт по записи на каждую строку окна.
func TestDecodeResultTextIsBounded(t *testing.T) {
	huge, err := json.Marshal(strings.Repeat("тело метода ", 12_000))
	if err != nil {
		t.Fatalf("сборка входа: %v", err)
	}
	line := `{"type":"user","timestamp":"2026-08-04T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_big","content":` + string(huge) + `}]}}`

	d, ok := Decode([]byte(line))
	if !ok || len(d.Results) != 1 {
		t.Fatalf("ok=%v, результатов %d", ok, len(d.Results))
	}
	if n := len([]rune(d.Results[0].Text)); n > maxError+1 {
		t.Errorf("длина текста результата %d рун, потолок %d", n, maxError)
	}
}

// Путь приходит снаружи, поэтому у него свой потолок; управляющие символы до
// показа доезжать не должны.
func TestDecodePathLimits(t *testing.T) {
	long := "/" + strings.Repeat("сегмент/", 60) + "file.go"
	line := `{"type":"assistant","timestamp":"2026-08-04T10:00:00Z","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"` + long + `"}}]}}`

	d, ok := Decode([]byte(line))
	if !ok || len(d.Events) != 1 {
		t.Fatalf("ok=%v, событий %d", ok, len(d.Events))
	}
	if got := len([]rune(d.Events[0].Path)); got > maxPath+1 {
		t.Errorf("длина пути %d рун, потолок %d", got, maxPath)
	}

	// Управляющий символ в пути. Вход собирается маршалингом, а не руками:
	// сырым байтом строка не была бы валидным JSON, а в живых данных он
	// приходит экранированным.
	input, err := json.Marshal(map[string]string{"file_path": "/tmp/" + string(rune(0x1b)) + "[31mзлой.go"})
	if err != nil {
		t.Fatalf("сборка входа: %v", err)
	}
	evil := `{"type":"assistant","timestamp":"2026-08-04T10:00:00Z","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Write","input":` + string(input) + `}]}}`

	d, ok = Decode([]byte(evil))
	if !ok || len(d.Events) != 1 {
		t.Fatalf("ok=%v, событий %d", ok, len(d.Events))
	}
	if strings.ContainsRune(d.Events[0].Path, 0x1b) {
		t.Errorf("управляющий символ из пути прошёл наружу: %q", d.Events[0].Path)
	}
	if !strings.Contains(d.Events[0].Path, "злой.go") {
		t.Errorf("путь потерян при вычистке: %q", d.Events[0].Path)
	}
}

// Деталь файлового вызова — две последние компоненты пути. Виндовый путь
// («D:\Work\...») режется по «\», линуксовый — только по «/»: бэкслеш там
// легальный символ имени, и «weird\name.txt» обязан остаться одним именем.
func TestLastTwo(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"linux: две последние компоненты", "/home/user/proj/internal/app/app.go", "app/app.go"},
		{"linux: одна компонента", "/foo", "foo"},
		{"linux: относительный путь", "internal/app/app.go", "app/app.go"},
		{"linux: имя без пути", "app.go", "app.go"},
		{"linux: пустой путь", "", ""},
		{"linux: сдвоенные слеши не дают пустых компонент", "/home//user///app.go", "user/app.go"},
		{"linux: бэкслеш в имени файла — одно имя", `/home/u/weird\name.txt`, `u/weird\name.txt`},
		{"linux: несколько бэкслешей в имени", `/home/u/a\b\c.txt`, `u/a\b\c.txt`},
		{"windows: путь скрипта", `D:\Work\PowerShell\scripts\admin\Get-Inventory.ps1`, "admin/Get-Inventory.ps1"},
		{"windows: профиль пользователя", `C:\Users\user\AppData\Local\Temp\x.json`, "Temp/x.json"},
		{"windows: файл в корне диска", `D:\x.ps1`, "D:/x.ps1"},
		{"windows: смешанные разделители", `D:/Work/scripts\file.ps1`, "scripts/file.ps1"},
		{"windows: UNC", `\\server\share\dir\file.txt`, "dir/file.txt"},
		{"windows: только сервер и шара", `\\server\share`, "server/share"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastTwo(tt.path); got != tt.want {
				t.Errorf("lastTwo(%q) = %q, ожидалось %q", tt.path, got, tt.want)
			}
		})
	}
}

// Имя инструмента режется своим, более щедрым пределом: реальные имена
// MCP-ручек уже сегодня достигают 60 рун, а общий предел имён — ровно 60.
func TestDecodeKeepsLongToolName(t *testing.T) {
	name := "mcp__acme_office_suite__create_reply_all_draft_from_messages"
	if len([]rune(name)) != maxName {
		t.Fatalf("длина имени %d, ожидалось %d", len([]rune(name)), maxName)
	}

	line := `{"type":"assistant","timestamp":"2026-08-04T10:00:00Z","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"` + name + `","input":{}}]}}`
	d, ok := Decode([]byte(line))
	if !ok || len(d.Events) != 1 {
		t.Fatalf("ok=%v, событий %d", ok, len(d.Events))
	}
	if d.Events[0].Tool != name {
		t.Errorf("Tool=%q, ожидалось имя целиком", d.Events[0].Tool)
	}

	_, method, parsed := MCPParts(d.Events[0].Tool)
	if !parsed || method != "create_reply_all_draft_from_messages" {
		t.Errorf("метод разобран как %q (ok=%v)", method, parsed)
	}
}

// TestDecodeToolNameBeyondMaxName — сторож границы: имя инструмента
// ДОЛЖНО обрезаться пределом maxToolName (120), а не общим maxName (60).
// Имя ниже — 61 руна, то есть уже длиннее maxName, но короче maxToolName.
// Если decode.go по ошибке вернётся к Truncate(b.Name, maxName), метод
// придёт с обрезкой и хвостовым «…» — тест обязан это заметить, идя ЧЕРЕЗ
// Decode (а не через прямой вызов Truncate, как TestMCPPartsAtTruncationBoundary).
func TestDecodeToolNameBeyondMaxName(t *testing.T) {
	name := "mcp__acme_office_suite__create_reply_all_draft_from_messages_v2"
	if n := len([]rune(name)); n <= maxName {
		t.Fatalf("длина имени %d, для теста нужно строго больше maxName=%d", n, maxName)
	}

	line := `{"type":"assistant","timestamp":"2026-08-04T10:00:00Z","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"` + name + `","input":{}}]}}`
	d, ok := Decode([]byte(line))
	if !ok || len(d.Events) != 1 {
		t.Fatalf("ok=%v, событий %d", ok, len(d.Events))
	}
	if d.Events[0].Tool != name {
		t.Errorf("Tool=%q, ожидалось имя целиком нетронутым (в пределах maxToolName)", d.Events[0].Tool)
	}

	server, method, parsed := MCPParts(d.Events[0].Tool)
	if !parsed {
		t.Fatalf("имя не разобралось: server=%q method=%q", server, method)
	}
	if server != "acme_office_suite" {
		t.Errorf("server=%q, ожидался acme_office_suite", server)
	}
	if method != "create_reply_all_draft_from_messages_v2" {
		t.Errorf("method=%q, ожидался create_reply_all_draft_from_messages_v2 (без обрезки)", method)
	}
	if strings.HasSuffix(method, "…") {
		t.Errorf("method обрезан хвостовым многоточием: %q — значит decode.go режет по maxName, не maxToolName", method)
	}
}

func TestDecodeEventMetadata(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-03T10:00:01.500Z","effort":"high","sessionId":"s1","message":{"id":"m1","model":"claude-opus-5","content":[{"type":"text","text":"привет"}]}}`

	d, ok := Decode([]byte(line))
	if !ok || len(d.Events) != 1 {
		t.Fatalf("ok=%v, событий %d, ожидалось одно", ok, len(d.Events))
	}
	ev := d.Events[0]

	want := time.Date(2026, 8, 3, 10, 0, 1, 500_000_000, time.UTC)
	if !ev.Time.Equal(want) {
		t.Errorf("Time=%v, ожидалось %v", ev.Time, want)
	}
	if ev.Source != "main" {
		t.Errorf("Source=%q, ожидался main", ev.Source)
	}
	if ev.Model != "claude-opus-5" {
		t.Errorf("Model=%q", ev.Model)
	}
	// effort — плоское поле верхнего уровня записи, не assistant.effort.level.
	if ev.Effort != "high" {
		t.Errorf("Effort=%q, ожидался high", ev.Effort)
	}
}

func TestDecodeSourceFromAttributionAgent(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","attributionAgent":"go-code-adapter","agentId":"acc3faf0","message":{"id":"m1","content":[{"type":"text","text":"пишу код"}]}}`

	d, ok := Decode([]byte(line))
	if !ok || len(d.Events) != 1 {
		t.Fatalf("ok=%v, событий %d", ok, len(d.Events))
	}
	if d.Events[0].Source != "go-code-adapter" {
		t.Errorf("Source=%q, ожидался go-code-adapter", d.Events[0].Source)
	}
}

func TestDecodeUsage(t *testing.T) {
	tests := []struct {
		name string
		line string
		want *Usage
	}{
		{
			name: "вложенный cache_creation — основной путь",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","requestId":"req_1","message":{"id":"msg_1","model":"claude-opus-5","stop_reason":"tool_use","content":[],"usage":{"input_tokens":2,"output_tokens":238,"cache_read_input_tokens":91,"cache_creation_input_tokens":55410,"cache_creation":{"ephemeral_1h_input_tokens":55410,"ephemeral_5m_input_tokens":0}}}}`,
			want: &Usage{
				RequestID: "req_1", MessageID: "msg_1", Model: "claude-opus-5",
				StopReason: "tool_use", Input: 2, Output: 238, CacheRead: 91,
				Cache5m: 0, Cache1h: 55410,
				Time: time.Date(2026, 8, 3, 10, 0, 1, 0, time.UTC),
			},
		},
		{
			name: "плоский cache_creation_input_tokens — fallback, всё как 5m",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","requestId":"req_2","message":{"id":"msg_2","model":"claude-opus-5","stop_reason":null,"content":[],"usage":{"input_tokens":1,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":700}}}`,
			want: &Usage{
				RequestID: "req_2", MessageID: "msg_2", Model: "claude-opus-5",
				StopReason: "", Input: 1, Output: 5, Cache5m: 700, Cache1h: 0,
				Time: time.Date(2026, 8, 3, 10, 0, 1, 0, time.UTC),
			},
		},
		{
			name: "usage отсутствует",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","requestId":"req_3","message":{"id":"msg_3","content":[]}}`,
			want: nil,
		},
		{
			name: "usage равен null",
			line: `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","requestId":"req_4","message":{"id":"msg_4","content":[],"usage":null}}`,
			want: nil,
		},
		{
			name: "у записи пользователя расхода нет",
			line: `{"type":"user","timestamp":"2026-08-03T10:00:01Z","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := Decode([]byte(tt.line))
			if !ok {
				t.Fatalf("Decode вернул ok=false")
			}
			got := d.Usage
			if tt.want == nil {
				if got != nil {
					t.Fatalf("usage=%+v, ожидался nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("usage=nil, ожидался %+v", tt.want)
			}
			// Время сэмпла сравнивается наравне с остальными полями:
			// от него зависит выбор датового окна тарифа.
			if *got != *tt.want {
				t.Errorf("usage=%+v, ожидался %+v", *got, *tt.want)
			}
		})
	}
}

// Записи API-ошибок приходят с model "<synthetic>", нулевым usage и пустым
// requestId. Событие они порождать обязаны, расход — нет: иначе в сводке
// появляется ложная «неизвестная модель» и лишний запрос.
func TestDecodeSyntheticModelHasNoUsage(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","requestId":null,"message":{"id":"d1563355","model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":"API Error: 500"}],"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0}}}}`

	d, ok := Decode([]byte(line))
	if !ok {
		t.Fatalf("ok=false")
	}
	if d.Usage != nil {
		t.Errorf("usage=%+v, ожидался nil", d.Usage)
	}
	if len(d.Events) != 1 || d.Events[0].Detail != "API Error: 500" {
		t.Errorf("события=%+v, ожидалось одно текстовое", d.Events)
	}
}

// Пустой requestId вместе с нулевым расходом — тоже не сэмпл: все такие
// записи схлопнулись бы в один ключ дедупликации.
func TestDecodeZeroUsageWithoutRequestIDSkipped(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","message":{"id":"m","model":"claude-opus-5","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`

	d, ok := Decode([]byte(line))
	if !ok {
		t.Fatalf("ok=false")
	}
	if d.Usage != nil {
		t.Errorf("usage=%+v, ожидался nil", d.Usage)
	}
}

// Враждебные значения токенов не должны искажать ранг и сводку.
func TestDecodeClampsAbsurdTokens(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"отрицательные", `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","requestId":"r","message":{"id":"m","model":"claude-opus-5","content":[],"usage":{"input_tokens":-5,"output_tokens":-1,"cache_read_input_tokens":-2,"cache_creation_input_tokens":-3}}}`},
		{"максимум int64", `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","requestId":"r","message":{"id":"m","model":"claude-opus-5","content":[],"usage":{"input_tokens":9223372036854775807,"output_tokens":9223372036854775807,"cache_read_input_tokens":9223372036854775807,"cache_creation":{"ephemeral_1h_input_tokens":9223372036854775807,"ephemeral_5m_input_tokens":9223372036854775807}}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := Decode([]byte(tt.line))
			if !ok {
				t.Fatalf("ok=false")
			}
			u := d.Usage
			if u == nil {
				return // нулевой расход отбрасывать допустимо
			}
			for _, v := range []int64{u.Input, u.Output, u.CacheRead, u.Cache5m, u.Cache1h} {
				if v < 0 || v > MaxTokens {
					t.Errorf("значение %d вне допустимого диапазона [0, %d]", v, MaxTokens)
				}
			}
			if u.Total() < 0 {
				t.Errorf("Total()=%d переполнился", u.Total())
			}
		})
	}
}

// speed и server_tool_use влияют на тариф, а в живых данных speed всегда
// "standard", а web_search_requests — ноль: проверка синтетическая.
func TestDecodeUsageSpeedAndWebSearch(t *testing.T) {
	tests := []struct {
		name          string
		usage         string
		wantFast      bool
		wantWebSearch int64
	}{
		{
			name:  "обычный режим",
			usage: `{"input_tokens":1,"output_tokens":2,"speed":"standard","server_tool_use":{"web_search_requests":0,"web_fetch_requests":0}}`,
		},
		{
			name:     "ускоренный режим",
			usage:    `{"input_tokens":1,"output_tokens":2,"speed":"fast"}`,
			wantFast: true,
		},
		{
			name:          "веб-поиск считается",
			usage:         `{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_search_requests":3,"web_fetch_requests":1}}`,
			wantWebSearch: 3,
		},
		{
			name:  "speed равен null",
			usage: `{"input_tokens":1,"output_tokens":2,"speed":null,"server_tool_use":null}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","requestId":"r","message":{"id":"m","model":"claude-opus-5","content":[],"usage":` + tt.usage + `}}`

			d, ok := Decode([]byte(line))
			if !ok || d.Usage == nil {
				t.Fatalf("ok=%v, usage=%v", ok, d.Usage)
			}
			u := d.Usage
			if u.Fast != tt.wantFast {
				t.Errorf("Fast=%v, ожидалось %v", u.Fast, tt.wantFast)
			}
			if u.WebSearch != tt.wantWebSearch {
				t.Errorf("WebSearch=%d, ожидалось %d", u.WebSearch, tt.wantWebSearch)
			}
		})
	}
}

// stop_reason лежит в message.stop_reason; top-level в живых данных всегда null.
func TestDecodeStopReasonFromMessage(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-03T10:00:01Z","stop_reason":null,"requestId":"r","message":{"id":"m","stop_reason":"end_turn","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`

	d, ok := Decode([]byte(line))
	if !ok || d.Usage == nil {
		t.Fatalf("ok=%v, usage=%v", ok, d.Usage)
	}
	if d.Usage.StopReason != "end_turn" {
		t.Errorf("StopReason=%q, ожидался end_turn", d.Usage.StopReason)
	}
}

func TestUsageKeyAndTotal(t *testing.T) {
	a := Usage{RequestID: "r1", MessageID: "m1"}
	b := Usage{RequestID: "r1", MessageID: "m2"}
	c := Usage{RequestID: "r2", MessageID: "m1"}

	if a.Key() == b.Key() || a.Key() == c.Key() || b.Key() == c.Key() {
		t.Errorf("ключи должны различаться: %q %q %q", a.Key(), b.Key(), c.Key())
	}
	if a.Key() != (Usage{RequestID: "r1", MessageID: "m1", Output: 5}).Key() {
		t.Errorf("ключ не должен зависеть от токенов")
	}

	u := Usage{Input: 1, Output: 2, CacheRead: 4, Cache5m: 8, Cache1h: 16}
	if got := u.Total(); got != 31 {
		t.Errorf("Total()=%d, ожидалось 31", got)
	}
}

// Парсер терпимый: битая строка молча пропускается, кривые поля не роняют.
func TestDecodeTolerance(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		wantOK bool
	}{
		{"обрезанный JSON", `{"type":"assistant","message":{"content":[{"type":"text"`, false},
		{"вообще не JSON", `не json вовсе`, false},
		{"пустая строка", ``, false},
		{"массив вместо объекта", `[]`, false},
		{"число вместо объекта", `123`, false},
		{"json null", `null`, true},
		{"пустой объект", `{}`, true},
		{"message равен null", `{"type":"assistant","message":null}`, true},
		{"content равен null", `{"type":"assistant","message":{"content":null}}`, true},
		{"content строкой", `{"type":"assistant","message":{"content":"строка"}}`, true},
		{"блок равен null", `{"type":"assistant","message":{"content":[null]}}`, true},
		{"input не объект", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":"ls"}]}}`, true},
		{"битый timestamp", `{"type":"assistant","timestamp":"вчера","message":{"content":[{"type":"text","text":"а"}]}}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := Decode([]byte(tt.line))
			if ok != tt.wantOK {
				t.Errorf("ok=%v, ожидалось %v", ok, tt.wantOK)
			}
			if !ok && (len(d.Events) != 0 || d.Usage != nil) {
				t.Errorf("при ok=false должно быть пусто, получено %d событий и usage=%v", len(d.Events), d.Usage)
			}
		})
	}
}
