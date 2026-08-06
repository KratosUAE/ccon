package parse

import (
	"strings"
	"testing"
	"time"
)

func TestLine(t *testing.T) {
	ts := time.Date(2026, 8, 3, 10, 31, 4, 0, time.Local)

	tests := []struct {
		name string
		ev   Event
		want string
	}{
		{
			name: "вызов инструмента",
			ev:   Event{Time: ts, Source: "main", Kind: KindTool, Tool: "Read", Detail: "demo-repo/PROMPT.md"},
			want: "10:31:04  main                Read       demo-repo/PROMPT.md",
		},
		{
			name: "реплика ассистента помечена вертикальной чертой",
			ev:   Event{Time: ts, Source: "main", Kind: KindText, Detail: "Спека прочитана"},
			want: "10:31:04  main                │          Спека прочитана",
		},
		{
			name: "делегирование",
			ev:   Event{Time: ts, Source: "main", Kind: KindDelegate, Tool: "Agent", Detail: "go-code-adapter фикс"},
			want: "10:31:04  main                Agent      go-code-adapter фикс",
		},
		{
			name: "скилл",
			ev:   Event{Time: ts, Source: "main", Kind: KindSkill, Tool: "Skill", Detail: "superpowers:brainstorming"},
			want: "10:31:04  main                Skill      superpowers:brainstorming",
		},
		{
			name: "ошибка укладывается в колонку ярлыка",
			ev:   Event{Time: ts, Source: "go-code-adapter", Kind: KindError, Detail: "No such tool available: Task"},
			want: "10:31:04  go-code-adapter     ✗ ERROR    No such tool available: Task",
		},
		{
			name: "длинное имя источника обрезано до 18 с многоточием",
			ev:   Event{Time: ts, Source: "оченьдлинноеимяагентасверхмеры", Kind: KindTool, Tool: "Bash", Detail: "ls"},
			want: "10:31:04  оченьдлинноеимяаг…  Bash       ls",
		},
		{
			name: "длинное имя инструмента не двигает колонку деталей",
			ev:   Event{Time: ts, Source: "main", Kind: KindTool, Tool: "AskUserQuestion", Detail: "куда?"},
			want: "10:31:04  main                AskUserQ…  куда?",
		},
		{
			name: "системная запись",
			ev:   Event{Time: ts, Source: "main", Kind: KindSystem, Detail: "context compaction: 385077 → 24939"},
			want: "10:31:04  main                system     context compaction: 385077 → 24939",
		},
		{
			name: "подмена модели",
			ev:   Event{Time: ts, Source: "main", Kind: KindFallback, Detail: "claude-fable-5 → claude-opus-5 · cyber"},
			want: "10:31:04  main                ⚠ swap     claude-fable-5 → claude-opus-5 · cyber",
		},
		{
			name: "пустая деталь не оставляет хвостовых пробелов",
			ev:   Event{Time: ts, Source: "main", Kind: KindTool, Tool: "Bash"},
			want: "10:31:04  main                Bash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Line(tt.ev); got != tt.want {
				t.Errorf("Line()=%q\nожидалось %q", got, tt.want)
			}
		})
	}
}

// Колонка деталей обязана стоять на одном месте при любом ярлыке.
func TestLineDetailColumnIsStable(t *testing.T) {
	ts := time.Date(2026, 8, 3, 10, 31, 4, 0, time.Local)
	evs := []Event{
		{Time: ts, Source: "main", Kind: KindTool, Tool: "Read", Detail: "ДЕТАЛЬ"},
		{Time: ts, Source: "main", Kind: KindText, Detail: "ДЕТАЛЬ"},
		{Time: ts, Source: "go-code-adapter", Kind: KindError, Detail: "ДЕТАЛЬ"},
		{Time: ts, Source: "оченьдлинноеимяагентасверхмеры", Kind: KindTool, Tool: "AskUserQuestion", Detail: "ДЕТАЛЬ"},
		{Time: ts, Source: "a", Kind: KindDelegate, Tool: "Agent", Detail: "ДЕТАЛЬ"},
	}

	want := -1
	for _, ev := range evs {
		line := Line(ev)
		col := len([]rune(line)) - len([]rune("ДЕТАЛЬ"))
		if want == -1 {
			want = col
			continue
		}
		if col != want {
			t.Errorf("деталь начинается с колонки %d вместо %d в строке %q", col, want, line)
		}
	}
}

// Санитайз обязан покрывать все колонки, а не только деталь: имя инструмента
// приходит от MCP-сервера, имя источника — из конфига агента.
func TestLineStripsControlCharsInAllColumns(t *testing.T) {
	ts := time.Date(2026, 8, 3, 10, 31, 4, 0, time.Local)
	ev := Event{
		Time:   ts,
		Source: "\x1b[31mЗЛОЙ\x07АГЕНТ",
		Kind:   KindTool,
		Tool:   "\x1b[31mEVIL",
		Detail: "нормальная деталь",
	}

	got := Line(ev)
	for _, r := range got {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("в строке %q остался управляющий символ %U", got, r)
		}
	}
	if !strings.Contains(got, "нормальная деталь") {
		t.Errorf("деталь потеряна: %q", got)
	}
}

// Санитайз колонок не должен разъезжать раскладку.
func TestLineColumnStableWithControlChars(t *testing.T) {
	ts := time.Date(2026, 8, 3, 10, 31, 4, 0, time.Local)
	clean := Line(Event{Time: ts, Source: "агент", Kind: KindTool, Tool: "Bash", Detail: "X"})
	dirty := Line(Event{Time: ts, Source: "аг\x1bент", Kind: KindTool, Tool: "Ba\x07sh", Detail: "X"})

	if len([]rune(clean)) != len([]rune(dirty)) {
		t.Errorf("длины строк разошлись: %q против %q", clean, dirty)
	}
}

// Управляющие символы из tool_result не должны доезжать до терминала.
func TestTruncateStripsControlCharacters(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"escape-последовательность", "красный \x1b[31mтекст\x1b[0m"},
		{"звонок и возврат каретки", "бип\x07\rзатёрто"},
		{"C1-управляющие", "текст\u0085\u009bхвост"},
		{"нулевой байт", "до\x00после"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.in, 100)
			for _, r := range got {
				if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
					t.Errorf("в результате %q остался управляющий символ %U", got, r)
				}
			}
			if got == "" {
				t.Errorf("текст вычищен целиком: %q", tt.in)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"короткая строка не меняется", "go build", 20, "go build"},
		{"ровно по границе", "abcde", 5, "abcde"},
		{"на руну длиннее", "abcdef", 5, "abcde…"},
		{"пробелы схлопнуты", "go build   ./...\n\t&& echo OK", 60, "go build ./... && echo OK"},
		{"края обрезаны", "  привет  ", 60, "привет"},
		{"пустая строка", "   \n  ", 10, ""},
		{"кириллица режется по рунам", "абвгдежзик", 5, "абвгд…"},
		{"эмодзи режется по рунам", "🔥🔥🔥🔥", 2, "🔥🔥…"},
		{"нулевая длина", "что угодно", 0, "…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.in, tt.n)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d)=%q, ожидалось %q", tt.in, tt.n, got, tt.want)
			}
			if strings.ContainsAny(got, "\n\t") {
				t.Errorf("в результате остались управляющие пробелы: %q", got)
			}
		})
	}
}

// Виндовый путь распознаётся по самому пути, а не по системе: транскрипт мог
// быть записан на другой машине. Цена ошибок разная — линуксовое имя с
// бэкслешем («weird\name.txt» — ОДИН файл) принять за виндовый путь нельзя.
func TestWindowsPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"буква диска с бэкслешем", `D:\Work\PowerShell`, true},
		{"буква диска со слешем", "D:/Work/PowerShell", true},
		{"строчная буква диска", `c:\Users\user`, true},
		{"корень диска", `D:\`, true},
		{"UNC", `\\server\share\file.txt`, true},
		{"абсолютный linux-путь", "/home/user/proj/app.go", false},
		{"бэкслеш в имени файла на linux", `/home/u/weird\name.txt`, false},
		{"относительный путь с бэкслешем", `scripts\admin.ps1`, false},
		{"каталог с двоеточием на linux", "/home/u/D:/x", false},
		{"один бэкслеш в начале", `\share\file.txt`, false},
		{"двоеточие без разделителя", "D:Work", false},
		{"голая буква диска", "D:", false},
		{"цифра вместо буквы диска", `1:\x`, false},
		{"пустой путь", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WindowsPath(tt.path); got != tt.want {
				t.Errorf("WindowsPath(%q) = %v, ожидалось %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range []Kind{KindText, KindTool, KindDelegate, KindSkill, KindError} {
		s := k.String()
		if s == "" {
			t.Errorf("Kind(%d).String() пуст", int(k))
		}
		if seen[s] {
			t.Errorf("имя рода события %q не уникально", s)
		}
		seen[s] = true
	}
}
