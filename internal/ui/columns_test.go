package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/parse"
)

// Ступени ширины таба mcp. Числа выписаны явно, а не пересчитаны формулой
// реализации: тест обязан падать при смене раскладки, а не соглашаться с ней.
//
// Инвариант всей таблицы — голова строки вместе с блоком исхода укладывается в
// окно: исход и длительность не жертвуются никогда, ради них таб и заведён.
func TestMCPColumnsLadder(t *testing.T) {
	tests := []struct {
		width int
		want  mcpCols
	}{
		{40, mcpCols{source: 8, server: 0, method: 7, args: 0}},
		{55, mcpCols{source: 10, server: 0, method: 16, args: 0}},
		{70, mcpCols{source: 18, server: 9, method: 16, args: 0}},
		{80, mcpCols{source: 18, server: 12, method: 20, args: 0}},
		{100, mcpCols{source: 18, server: 14, method: 24, args: 15}},
		{120, mcpCols{source: 18, server: 18, method: 28, args: 27}},
		{140, mcpCols{source: 18, server: 24, method: 30, args: 39}},
	}

	prev := mcpCols{}
	for _, tt := range tests {
		got := mcpColumns(tt.width)
		if got != tt.want {
			t.Errorf("mcpColumns(%d) = %+v, ожидалось %+v", tt.width, got, tt.want)
		}
		// Голова строки обязана уместиться в окно вместе с блоком исхода.
		head := timeWidth + colGap + got.source + colGap + got.method
		if got.server > 0 {
			head += got.server + colGap
		}
		if got.args > 0 {
			head += colGap + got.args
		}
		if head+mcpCellWidth > tt.width {
			t.Errorf("ширина %d: колонки заняли %d вместе с блоком исхода", tt.width, head+mcpCellWidth)
		}
		// Ступени монотонны: окно шире — колонки не уже.
		if got.server < prev.server || got.method < prev.method || got.args < prev.args {
			t.Errorf("ширина %d: колонки сузились относительно предыдущей ступени: %+v после %+v",
				tt.width, got, prev)
		}
		prev = got
	}

	// Ширина ещё не известна (кадр до первого WindowSizeMsg): резать не по
	// чему, аргументы не ограничены.
	if got := mcpColumns(0); got.args != unbounded {
		t.Errorf("при неизвестной ширине аргументы ограничены: %+v", got)
	}
}

// То же для таба файлов: жертвовать там нечем, кроме пути, и он обязан
// оставаться положительным даже на узкой панели.
func TestFileColumnsLadder(t *testing.T) {
	tests := []struct {
		width int
		want  fileCols
	}{
		{40, fileCols{source: 8, path: 11}},
		{55, fileCols{source: 10, path: 24}},
		{70, fileCols{source: 18, path: 31}},
		{80, fileCols{source: 18, path: 41}},
		{100, fileCols{source: 18, path: 61}},
		{120, fileCols{source: 18, path: 81}},
	}

	prev := fileCols{}
	for _, tt := range tests {
		got := fileColumns(tt.width)
		if got != tt.want {
			t.Errorf("fileColumns(%d) = %+v, ожидалось %+v", tt.width, got, tt.want)
		}
		head := timeWidth + colGap + got.source + colGap + fileOpWidth + colGap + got.path
		if head+fileCellWidth > tt.width {
			t.Errorf("ширина %d: колонки заняли %d вместе с блоком исхода", tt.width, head+fileCellWidth)
		}
		if got.path < prev.path {
			t.Errorf("ширина %d: путь сузился относительно предыдущей ступени: %+v после %+v",
				tt.width, got, prev)
		}
		prev = got
	}

	if got := fileColumns(0); got.path != unbounded {
		t.Errorf("при неизвестной ширине путь ограничен: %+v", got)
	}
}

// Путь режется с ГОЛОВЫ и по границе компонента: хвост важнее — по имени файла
// строку и узнают, а начало у всех путей проекта одинаковое.
func TestClipPathHead(t *testing.T) {
	const path = "/home/user/Devs/proj/internal/parse/decode.go"

	tests := []struct {
		name  string
		width int
		want  string
	}{
		{"влезает целиком", 60, path},
		{"ровно по ширине", len([]rune(path)), path},
		{"по границе компонента", 20, "…/parse/decode.go"},
		{"самый длинный влезающий хвост", 30, "…/internal/parse/decode.go"},
		{"компонент не влезает — режем по рунам", 8, "…code.go"},
		{"одна колонка", 1, "…"},
		{"ширина не известна", unbounded, path},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clipPathHead(path, tt.width)
			if got != tt.want {
				t.Errorf("clipPathHead(%d) = %q, ожидалось %q", tt.width, got, tt.want)
			}
			if tt.width > 0 && len([]rune(got)) > tt.width {
				t.Errorf("результат %q шире отведённых %d колонок", got, tt.width)
			}
			// Смысл всей обрезки: имя файла обязано уцелеть, пока оно влезает.
			if tt.width >= len("…decode.go") && !strings.HasSuffix(got, "decode.go") {
				t.Errorf("обрезка съела имя файла: %q", got)
			}
		})
	}
}

// Граница компонента у виндового пути — «\» (и «/» в смешанной записи), иначе
// от «D:\Work\...» не отрезалось бы ничего и путь резался бы по рунам посреди
// имени. На linux-пути «\» границей НЕ считается: там это символ имени файла.
func TestClipPathHeadSeparators(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		width int
		want  string
	}{
		{"windows: по границе компонента", `D:\Work\PowerShell\scripts\admin\Get-Inventory.ps1`, 25, `…\admin\Get-Inventory.ps1`},
		{"windows: самый длинный влезающий хвост", `D:\Work\PowerShell\scripts\admin\Get-Inventory.ps1`, 20, `…\Get-Inventory.ps1`},
		{"windows: имя не влезает — режем по рунам", `D:\Work\PowerShell\scripts\admin\Get-Inventory.ps1`, 10, "…ntory.ps1"},
		{"windows: влезает целиком", `C:\Users\user\x.json`, 40, `C:\Users\user\x.json`},
		{"windows: UNC", `\\srv\share\dir\file.txt`, 15, `…\dir\file.txt`},
		{"windows: смешанные разделители", `D:/Work/scripts\file.ps1`, 12, `…\file.ps1`},
		// Ширина 12 выбрана нарочно: граница по «/» на ней уже не влезает, и
		// принятый за разделитель «\» дал бы «…\name.txt», потеряв «weird» из
		// имени файла. Правильный ответ — рез по рунам.
		{"linux: бэкслеш в имени границей не служит", `/home/u/weird\name.txt`, 12, `…rd\name.txt`},
		{"linux: имя с бэкслешем целиком, когда влезает", `/home/u/weird\name.txt`, 16, `…/weird\name.txt`},
		{"linux: обычный путь", "/home/user/proj/internal/parse/decode.go", 20, "…/parse/decode.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clipPathHead(tt.path, tt.width)
			if got != tt.want {
				t.Errorf("clipPathHead(%q, %d) = %q, ожидалось %q", tt.path, tt.width, got, tt.want)
			}
			if len([]rune(got)) > tt.width {
				t.Errorf("результат %q шире отведённых %d колонок", got, tt.width)
			}
		})
	}
}

// На узкой панели строка файла показывает хвост пути, а не его начало:
// «/home/user/Devs/proj» одинаково у всех строк и не отвечает ни на один
// вопрос.
func TestFilePathClippedFromHead(t *testing.T) {
	m := statusModel(t, ViewFiles, 40, 16)
	line := stripANSI(lineWith(t, m.content(), "decode.go"))

	if !strings.Contains(line, "…/decode.go") {
		t.Errorf("путь обрезан не с головы: %q", line)
	}
	if strings.Contains(line, "/home/user") {
		t.Errorf("на 40 колонках показано начало пути вместо хвоста: %q", line)
	}
}

// В режиме переноса причина сбоя идёт отдельной строкой «└ …» — подстрока из
// макета спеки. Аргументы при этом не теряются: место для них есть.
func TestWrapShowsFailUnderLine(t *testing.T) {
	tests := []struct {
		view View
		want []string // тексты ошибок, которые обязаны появиться под строками
	}{
		{ViewMCP, []string{"└ netscan: roaming path"}},
		{ViewFiles, []string{"└ File has not been read yet", "└ [Policy Gate]"}},
	}

	for _, tt := range tests {
		t.Run(tt.view.String(), func(t *testing.T) {
			m := statusModel(t, tt.view, 100, 30)
			m.Update(keyPress("w"))

			lines := m.content()
			joined := stripANSI(strings.Join(lines, "\n"))
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Errorf("нет подстроки с причиной сбоя %q:\n%s", want, joined)
				}
			}
			for _, line := range lines {
				// Уголок стоит под колонкой деталей, а не у левого края.
				if strings.HasPrefix(stripANSI(line), "└") {
					t.Errorf("подстрока прижата к краю вместо отступа: %q", stripANSI(line))
				}
			}
			// Успешная строка подстроки не получает: сочинять причину там,
			// где сбоя не было, нельзя.
			if strings.Count(joined, "└") != len(tt.want) {
				t.Errorf("подстрок %d, неуспешных строк %d:\n%s",
					strings.Count(joined, "└"), len(tt.want), joined)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got != 100 {
					t.Errorf("строка %d занимает %d колонок вместо 100: %q", i, got, stripANSI(line))
				}
			}
		})
	}

	// На табе mcp в режиме переноса аргументы возвращаются: их замещал текст
	// ошибки только потому, что в одну строку не помещалось и то и другое.
	m := statusModel(t, ViewMCP, 100, 30)
	m.Update(keyPress("w"))
	shown := stripANSI(strings.Join(m.content(), "\n"))
	if !strings.Contains(shown, "00:00:5e:00:53:00") {
		t.Errorf("в режиме переноса аргументы неуспешного вызова не показаны:\n%s", shown)
	}
}

// В режиме переноса деталь показывается ЦЕЛИКОМ, даже когда голова строки
// съела почти всё окно.
//
// Ступени сделали голову таба mcp широкой (18+14+24 на сотне колонок), и на
// хвост остаётся меньше двадцати колонок. Продолжение в этом случае
// прижимается к левому краю — но первый кусок считался по широкому отступу и
// оставался на строке головы, где его срезал край окна. Срезанное терялось
// совсем: продолжение шло со ВТОРОГО куска.
func TestWrapKeepsWholeDetail(t *testing.T) {
	args := `{"query":"` + strings.Repeat("абвгде", 20) + `"}`
	m := New(Options{Project: "p", Mode: ModeArchive, View: ViewMCP, Events: []parse.Event{
		{Time: time.Now(), Source: "kotlin-adapter", Kind: parse.KindTool,
			Tool: "mcp__acme_office_suite__create_reply_all_draft_from_messages", Detail: args},
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(keyPress("w"))

	glued := strings.ReplaceAll(dropCells(m.content(), mcpCellWidth), " ", "")
	if !strings.Contains(glued, args) {
		t.Errorf("деталь показана не целиком:\n%s", stripANSI(strings.Join(m.content(), "\n")))
	}
}

// Медленный вызов подсвечивается тёплым: колонка цифр иначе не бросается в
// глаза, а ради «какая ручка тормозит» таб и открывают.
func TestSlowCallIsWarm(t *testing.T) {
	th := NewTheme()
	tests := []struct {
		name string
		dur  time.Duration
		warm bool
	}{
		{"быстрый", 400 * time.Millisecond, false},
		{"на пороге", slowMCP, true},
		{"чуть быстрее порога", slowMCP - time.Millisecond, false},
		{"медленный", 30 * time.Second, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := parse.Event{Status: parse.StatusOK, Dur: tt.dur}
			cell := mcpCell(th, ev)

			text := durText(tt.dur)
			warm := strings.Contains(cell, th.tokWrite.Render(text))
			calm := strings.Contains(cell, th.dim.Render(text))
			if warm != tt.warm || calm == tt.warm {
				t.Errorf("длительность %s нарисована %q: тёплая=%v, ожидалось %v",
					tt.dur, cell, warm, tt.warm)
			}
		})
	}
}

// Кадры новых ступеней целиком: строка не шире окна, кадр не выше него, а
// правый блок исхода на месте при любой ширине.
func TestLadderFramesFitWindow(t *testing.T) {
	for _, size := range []struct{ w, h int }{{140, 20}, {100, 20}, {70, 18}, {55, 16}} {
		for _, v := range []View{ViewMCP, ViewFiles} {
			m := statusModel(t, v, size.w, size.h)

			cellWidth, want := mcpCellWidth, mcpOutcomes[0][0]
			if v == ViewFiles {
				cellWidth, want = fileCellWidth, filesOutcomes[0]
			}
			content := m.content()
			if got := cellOf(content[0], cellWidth); len(got) == 0 || got[0] != want {
				t.Errorf("%s %dx%d: правый блок первой строки %v, ожидался [%s]",
					v, size.w, size.h, got, want)
			}

			frame := strings.Split(m.View().Content, "\n")
			if len(frame) > size.h {
				t.Errorf("%s %dx%d: кадр занял %d строк", v, size.w, size.h, len(frame))
			}
			for i, line := range frame {
				if got := lipgloss.Width(line); got > size.w {
					t.Errorf("%s %dx%d: строка %d шире окна: %d колонок", v, size.w, size.h, i, got)
				}
			}
			t.Logf("\n=== ТАБ %s %dx%d (ступени) ===\n%s\n=== конец кадра ===",
				v, size.w, size.h, stripANSI(m.View().Content))
		}
	}
}

// Ширина ещё не известна: до первого WindowSizeMsg строка собирается без
// обрезки и не должна ни падать, ни терять исход.
func TestTabLineBeforeWindowSize(t *testing.T) {
	events, results := toolFixture(t)
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, Results: results, View: ViewFiles})

	line := stripANSI(lineWith(t, m.content(), "decode.go"))
	if !strings.Contains(line, "/home/user/Devs/proj/internal/parse/decode.go") {
		t.Errorf("при неизвестной ширине путь обрезан: %q", line)
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), "ok") {
		t.Errorf("при неизвестной ширине потерян исход: %q", line)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// То же для таба mcp: аргументы — первая жертва тесноты, но тесноты ещё
	// никто не измерил, и жертвовать нечему.
	mcp := New(Options{Project: "p", Mode: ModeArchive, Events: events, Results: results, View: ViewMCP})
	call := stripANSI(lineWith(t, mcp.content(), "find_symbol"))
	if !strings.Contains(call, "name_path") {
		t.Errorf("при неизвестной ширине пропали аргументы: %q", call)
	}
	mcp.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
}
