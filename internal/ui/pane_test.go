package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/parse"
)

// toolFixture — фикстура с настоящими вызовами и их результатами: два MCP,
// четыре файловые операции, отказ, незакрытый вызов и дубль результата.
// Одна фикстура на parse-, ui- и cmd-тесты.
func toolFixture(t *testing.T) ([]parse.Event, []parse.Result) {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "testdata", "tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var events []parse.Event
	var results []parse.Result
	if _, err := parse.Scan(f, func(line []byte) error {
		d, ok := parse.Decode(line)
		if !ok {
			t.Errorf("строка фикстуры не разобралась: %s", line)
			return nil
		}
		events = append(events, d.Events...)
		results = append(results, d.Results...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return events, results
}

// toolEvents — только события фикстуры: часть тестов проверяет показ до того,
// как исход вызова стал известен.
func toolEvents(t *testing.T) []parse.Event {
	t.Helper()
	events, _ := toolFixture(t)
	return events
}

// dropCells склеивает строки панели, сняв у каждой правый блок исхода.
// Хвост строки заведомо не длиннее width-cellWidth (перенос считается по
// той же ширине), поэтому срезаются только колонки блока и добивка.
func dropCells(lines []string, cellWidth int) string {
	var b strings.Builder
	for _, line := range lines {
		r := []rune(stripANSI(line))
		if len(r) > cellWidth {
			r = r[:len(r)-cellWidth]
		}
		b.WriteString(string(r))
	}
	return b.String()
}

// logText — только строки лога активного таба.
//
// Отрицательные проверки фильтра смотрят сюда, а не в кадр целиком: сводка
// футера считается по ВСЕМУ буферу (прямое требование спеки), и имя
// отфильтрованного сервера честно остаётся в ней.
func logText(m *Model) string {
	return stripANSI(strings.Join(m.content(), "\n"))
}

func toolModel(t *testing.T, view View, w, h int) *Model {
	t.Helper()

	m := New(Options{Project: "p", Mode: ModeArchive, Events: toolEvents(t), View: view})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// Каждый таб берёт своё: транскрипт — всё подряд, mcp — только вызовы ручек,
// files — только чтение, правку и запись.
func TestPanesPickTheirEvents(t *testing.T) {
	m := toolModel(t, ViewTranscript, 100, 30)

	tests := []struct {
		view  View
		want  int
		lines []string
	}{
		// Транскрипт не меняется: имя инструмента там по-прежнему ярлык в
		// девять рун, а не разнесённые сервер и метод.
		{ViewTranscript, 10, []string{"mcp__ser…", "AskUserQ…", "Policy Gate"}},
		{ViewMCP, 2, []string{"serena", "find_symbol", "netscan", "netscan_client_roam"}},
		{ViewFiles, 4, []string{"/home/user/.ssh/config", "/home/user/Devs/proj/internal/parse/decode.go",
			"/home/user/Devs/proj/README.md"}},
	}

	for _, tt := range tests {
		t.Run(tt.view.String(), func(t *testing.T) {
			p := m.panes[tt.view]
			got := p.content(m.log, m.theme, m.width)
			if len(got) != tt.want {
				t.Fatalf("строк %d, ожидалось %d:\n%s", len(got), tt.want, strings.Join(got, "\n"))
			}
			joined := stripANSI(strings.Join(got, "\n"))
			for _, want := range tt.lines {
				if !strings.Contains(joined, want) {
					t.Errorf("в табе нет %q:\n%s", want, joined)
				}
			}
		})
	}

	// Ошибка результата — событие транскрипта, а не файловой операции: в
	// окнах табов ей места нет, иначе строка задвоится.
	files := stripANSI(strings.Join(m.panes[ViewFiles].content(m.log, m.theme, m.width), "\n"))
	if strings.Contains(files, "Policy Gate") {
		t.Errorf("в таб файлов просочилась ошибка результата:\n%s", files)
	}
}

// Клавиши 1/2/3 и tab переключают таб, и в кадре видно содержимое активного.
func TestTabKeysSwitchPanes(t *testing.T) {
	m := toolModel(t, ViewTranscript, 100, 30)

	m.Update(keyPress("2"))
	if m.active != ViewMCP {
		t.Fatalf("активен таб %v, ожидался mcp", m.active)
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, "find_symbol") {
		t.Errorf("кадр не показывает вызовы MCP:\n%s", view)
	}

	m.Update(keyPress("3"))
	if m.active != ViewFiles {
		t.Fatalf("активен таб %v, ожидался files", m.active)
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, ".ssh/config") {
		t.Errorf("кадр не показывает файловые операции:\n%s", view)
	}

	// tab листает по кругу: с последнего таба возвращает на первый.
	m.Update(keyPress("tab"))
	if m.active != ViewTranscript {
		t.Errorf("tab с последнего таба привёл на %v", m.active)
	}

	m.Update(keyPress("1"))
	if m.active != ViewTranscript {
		t.Errorf("клавиша 1 привела на %v", m.active)
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, "AskUserQ…") {
		t.Errorf("транскрипт не вернулся:\n%s", view)
	}
}

// Ряд табов виден в шапке, активный отличается от прочих.
func TestTabsRowShowsActive(t *testing.T) {
	m := toolModel(t, ViewMCP, 100, 30)

	row := strings.Split(m.View().Content, "\n")[1]
	plain := stripANSI(row)
	for _, want := range []string{"[1]transcript", "[2]mcp", "[3]files"} {
		if !strings.Contains(plain, want) {
			t.Errorf("в ряду табов нет %q: %q", want, plain)
		}
	}
	if row == plain {
		t.Errorf("ряд табов нарисован без стилей: активный не отличить")
	}
	if got := lipgloss.Width(plain); got != 100 {
		t.Errorf("ряд табов занимает %d колонок вместо 100", got)
	}
}

// Фильтр у каждого таба свой: переключение туда и обратно его не теряет и не
// переносит на соседний таб.
func TestPaneFilterIsIndependent(t *testing.T) {
	m := toolModel(t, ViewMCP, 100, 30)

	m.Update(keyPress("/"))
	for _, r := range "serena" {
		m.Update(keyPress(string(r)))
	}
	m.Update(keyPress("enter"))

	if log := logText(m); strings.Contains(log, "netscan") {
		t.Errorf("фильтр не применился на табе mcp:\n%s", log)
	}

	// На файлах фильтра нет — там свои строки и свой пустой ввод.
	m.Update(keyPress("3"))
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "README.md") {
		t.Errorf("фильтр соседнего таба применился к файлам:\n%s", view)
	}
	if m.panes[ViewFiles].filter != "" {
		t.Errorf("фильтр перетёк на таб files: %q", m.panes[ViewFiles].filter)
	}

	m.Update(keyPress("2"))
	if m.panes[ViewMCP].filter != "serena" {
		t.Errorf("фильтр таба mcp потерян: %q", m.panes[ViewMCP].filter)
	}
	if log := logText(m); strings.Contains(log, "netscan") {
		t.Errorf("возврат на таб сбросил фильтр:\n%s", log)
	}
}

// Поиск широкий: он видит ровно то, что показано в строке, — путь, имя ручки
// и источник, а не только имя агента.
func TestFilterSearchesWholeLine(t *testing.T) {
	tests := []struct {
		name    string
		view    View
		needle  string
		want    string
		notWant string
	}{
		{"путь на табе файлов", ViewFiles, "decode.go", "decode.go", "README.md"},
		{"метод на табе mcp", ViewMCP, "find_symbol", "find_symbol", "netscan"},
		// Ярлык в строке обрезан, но ищется по полному имени инструмента:
		// стог фильтра — это событие, а не то, что влезло в колонку.
		{"имя инструмента в транскрипте", ViewTranscript, "askuserquestion", "AskUserQ…", "find_symbol"},
		{"источник в транскрипте", ViewTranscript, "kotlin-adapter", "kotlin-adapter", "AskUserQ…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := toolModel(t, tt.view, 100, 30)
			m.Update(keyPress("/"))
			for _, r := range tt.needle {
				m.Update(keyPress(string(r)))
			}

			if view := stripANSI(m.View().Content); !strings.Contains(view, tt.want) {
				t.Errorf("совпавшая строка пропала:\n%s", view)
			}
			// Отсутствие проверяется по логу: сводка футера считает по всему
			// буферу и имя отфильтрованного сервера из неё не пропадает.
			if log := logText(m); strings.Contains(log, tt.notWant) {
				t.Errorf("строка %q не отфильтрована:\n%s", tt.notWant, log)
			}
		})
	}
}

// Место чтения у каждого таба своё: возврат не должен выбрасывать вниз.
func TestPaneKeepsScrollPosition(t *testing.T) {
	events := make([]parse.Event, 80)
	for i := range events {
		events[i] = parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
			Tool: "Bash", Detail: fmt.Sprintf("команда-%d", i)}
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	for range 5 {
		m.Update(keyPress("up"))
	}
	offset := m.pane().vp.YOffset()

	m.Update(keyPress("2"))
	m.Update(keyPress("1"))

	if got := m.pane().vp.YOffset(); got != offset {
		t.Errorf("после возврата смещение %d, ожидалось %d", got, offset)
	}
	if m.pane().autoFollow {
		t.Errorf("возврат на таб оживил автоскролл, которого не было")
	}
}

// Непосещённый таб не стоит ничего: ни строк, ни кэшей до первого показа.
func TestUnvisitedPaneCostsNothing(t *testing.T) {
	m := toolModel(t, ViewTranscript, 100, 30)

	for _, v := range []View{ViewMCP, ViewFiles} {
		p := m.panes[v]
		if p.rows != nil || p.cachePln != nil || p.cacheWrap != nil {
			t.Errorf("таб %v собрал строки, хотя его не показывали", v)
		}
	}
	if m.panes[ViewTranscript].cachePln == nil {
		t.Errorf("показанный таб строки не собрал")
	}
	// Перенос не считается, пока его не включили: он стоит столько же, сколько
	// обычная строка, а включают его редко.
	for _, row := range m.panes[ViewTranscript].rows {
		if row.wrap != nil {
			t.Errorf("перенос посчитан заранее, хотя он выключен")
			break
		}
	}

	// Показали — собрал, и только обычное представление: перенос выключен.
	m.Update(keyPress("2"))
	if m.panes[ViewMCP].cachePln == nil {
		t.Errorf("показанный таб mcp строки не собрал")
	}
	if m.panes[ViewMCP].cacheWrap != nil {
		t.Errorf("собран кэш переноса, хотя перенос выключен")
	}
}

// Пустой таб обязан говорить об этом словами: молчаливая пустота
// неотличима от поломки, а сессий без единого MCP-вызова больше половины.
func TestEmptyPaneExplainsItself(t *testing.T) {
	events := []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: "ls"},
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, View: ViewMCP})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "no MCP calls in this session") {
		t.Errorf("пустой таб mcp промолчал:\n%s", view)
	}

	m.Update(keyPress("3"))
	if view := stripANSI(m.View().Content); !strings.Contains(view, "no file operations in this session") {
		t.Errorf("пустой таб файлов промолчал:\n%s", view)
	}

	// Отфильтрованный в ноль список — другое состояние, и путать их нельзя.
	m.Update(keyPress("1"))
	m.Update(keyPress("/"))
	for _, r := range "неттакого" {
		m.Update(keyPress(string(r)))
	}
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "nothing matches") {
		t.Errorf("пустой результат фильтра промолчал:\n%s", view)
	}
}

// Стартовый таб приходит из Options: это значение флага --view.
func TestStartViewOpensRequestedTab(t *testing.T) {
	for _, v := range []View{ViewTranscript, ViewMCP, ViewFiles} {
		t.Run(v.String(), func(t *testing.T) {
			m := toolModel(t, v, 100, 30)
			if m.active != v {
				t.Fatalf("открылся таб %v, ожидался %v", m.active, v)
			}
		})
	}
}

// Таб вне диапазона не должен ронять первый кадр обращением к
// несуществующей панели.
func TestStartViewOutOfRange(t *testing.T) {
	m := New(Options{Project: "p", Mode: ModeArchive, Events: toolEvents(t), View: View(7)})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	if m.active != ViewTranscript {
		t.Errorf("активен таб %v, ожидался транскрипт", m.active)
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, "AskUserQ…") {
		t.Errorf("транскрипт не показан:\n%s", view)
	}
}

func TestParseView(t *testing.T) {
	tests := []struct {
		in     string
		want   View
		wantOK bool
	}{
		{"transcript", ViewTranscript, true},
		{"mcp", ViewMCP, true},
		{"files", ViewFiles, true},
		{"bogus", ViewTranscript, false},
		{"", ViewTranscript, false},
		{"MCP", ViewTranscript, false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ParseView(tt.in)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("ParseView(%q)=%v,%v; ожидалось %v,%v", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}

	// Имена видов — один список на флаг, подписи табов и сообщения об ошибке.
	if len(ViewNames()) != viewCount {
		t.Errorf("имён видов %d, табов %d", len(ViewNames()), viewCount)
	}
	// Вид вне диапазона не должен притворяться существующим табом.
	for _, bad := range []View{View(-1), View(viewCount)} {
		if got := bad.String(); got != "?" {
			t.Errorf("View(%d).String()=%q, ожидалось \"?\"", bad, got)
		}
	}
	for i, name := range ViewNames() {
		if View(i).String() != name {
			t.Errorf("подпись таба %d (%q) разошлась с именем флага %q", i, View(i).String(), name)
		}
	}
}

// Перенос у нового таба свой и разворачивает длинный путь целиком.
func TestPaneWrapIsPerTab(t *testing.T) {
	long := "/home/user/" + strings.Repeat("очень-длинный-каталог/", 6) + "file.go"
	events := []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Read",
			Detail: "каталог/file.go", Path: long},
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, View: ViewFiles})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	plain := len(m.content())
	m.Update(keyPress("w"))
	wrapped := m.content()

	if len(wrapped) <= plain {
		t.Fatalf("перенос не добавил строк: было %d, стало %d", plain, len(wrapped))
	}
	// Продолжение идёт с отступом под колонкой пути: склеиваем строки и
	// убираем пробелы — в самом пути их нет. Правый блок исхода снимается:
	// он стоит у края окна и в склейке оказался бы посреди пути.
	glued := strings.ReplaceAll(dropCells(wrapped, fileCellWidth), " ", "")
	if !strings.Contains(glued, long) {
		t.Errorf("путь не показан целиком:\n%s", stripANSI(strings.Join(wrapped, "\n")))
	}
	// Перенос — свойство таба: транскрипт остаётся как был.
	if m.panes[ViewTranscript].wrap {
		t.Errorf("перенос включился и на соседнем табе")
	}

	// На узкой панели отступ съел бы строку целиком, поэтому продолжение
	// прижимается к левому краю — но за край окна не вылезает.
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	narrow := m.content()
	if len(narrow) <= plain {
		t.Errorf("на узкой панели перенос пропал: строк %d", len(narrow))
	}
	for i, line := range narrow {
		if got := lipgloss.Width(line); got != 40 {
			t.Errorf("строка %d занимает %d колонок вместо 40: %q", i, got, stripANSI(line))
		}
	}
}

// Перенос на табе mcp разворачивает длинные аргументы, а вызов без аргументов
// не оставляет за собой хвоста пробелов.
func TestMCPPaneWrapsArguments(t *testing.T) {
	args := `{"query":"` + strings.Repeat("длинный-запрос ", 12) + `"}`
	events := []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindTool,
			Tool: "mcp__context7__query-docs", Detail: args},
		{Time: time.Now(), Source: "main", Kind: parse.KindTool,
			Tool: "mcp__serena__list_memories", Detail: ""},
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, View: ViewMCP})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	plain := m.content()
	if len(plain) != 2 {
		t.Fatalf("строк без переноса %d, ожидалось 2:\n%s", len(plain), strings.Join(plain, "\n"))
	}
	// Вызов без аргументов: за колонкой метода идёт только правый блок исхода,
	// который у незакрытого вызова помечен точкой. Ничего иного между ними
	// быть не должно.
	bare := strings.TrimRight(stripANSI(plain[1]), " ")
	if !strings.HasSuffix(bare, "·") {
		t.Errorf("строка вызова без аргументов не кончается меткой исхода: %q", bare)
	}
	if between := strings.TrimSpace(strings.TrimSuffix(bare, "·")); !strings.HasSuffix(between, "list_memories") {
		t.Errorf("между методом и исходом появился мусор: %q", bare)
	}

	m.Update(keyPress("w"))
	wrapped := m.content()
	if len(wrapped) <= len(plain) {
		t.Fatalf("перенос не добавил строк: было %d, стало %d", len(plain), len(wrapped))
	}

	// Живое событие в режиме переноса дописывается в кэш, а не теряется.
	m.Push(parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
		Tool: "mcp__notes__create_note", Detail: `{"path":"/tmp/n.md"}`})
	if view := stripANSI(m.View().Content); !strings.Contains(view, "create_note") {
		t.Errorf("событие, пришедшее при включённом переносе, не показано:\n%s", view)
	}
}

// Путь, которого нет (битые аргументы Read), рисуется знаком вопроса, а не
// молчаливой дырой в колонке.
func TestMissingPathShownAsQuestionMark(t *testing.T) {
	m := toolModel(t, ViewFiles, 100, 30)

	lines := stripANSI(strings.Join(m.content(), "\n"))
	if !strings.Contains(lines, "R  ?") {
		t.Errorf("пустой путь не помечен:\n%s", lines)
	}
}

// Кадр каждого таба обязан держать размеры окна: строки не шире, чем окно,
// и кадр не выше него.
func TestTabFramesFitWindow(t *testing.T) {
	// 80×6 — новая ветка этого дифа: altView(lastLines(...)) обрезает кадр в
	// низком окне (model.go), прежде altView отдавал шапку+футер без обрезки.
	for _, size := range []struct{ w, h int }{{80, 20}, {40, 16}, {80, 6}} {
		for _, v := range []View{ViewTranscript, ViewMCP, ViewFiles} {
			m := toolModel(t, v, size.w, size.h)

			frame := m.View().Content
			lines := strings.Split(frame, "\n")
			if len(lines) > size.h {
				t.Errorf("%s %dx%d: кадр занял %d строк", v, size.w, size.h, len(lines))
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > size.w {
					t.Errorf("%s %dx%d: строка %d шире окна: %d колонок", v, size.w, size.h, i, got)
				}
			}
			t.Logf("\n=== ТАБ %s %dx%d ===\n%s\n=== конец кадра ===", v, size.w, size.h, stripANSI(frame))
		}
	}
}

// Событие живого потока доезжает до всех табов сразу, а не только до
// показанного: переключение не должно обнаруживать пустой таб.
func TestLivePushReachesEveryPane(t *testing.T) {
	m := New(Options{Project: "p", Mode: ModeLive, Events: toolEvents(t)})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Показываем оба таба, чтобы их кэши стали непустыми.
	m.Update(keyPress("2"))
	m.Update(keyPress("3"))
	m.Update(keyPress("1"))

	m.Push(parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
		Tool: "mcp__notes__create_note", ToolID: "toolu_live1", Detail: `{"path":"/tmp/n.md"}`})
	m.Push(parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
		Tool: "Write", ToolID: "toolu_live2", Detail: "tmp/live.go", Path: "/tmp/live.go"})

	m.Update(keyPress("2"))
	if view := stripANSI(m.View().Content); !strings.Contains(view, "create_note") {
		t.Errorf("живой вызов MCP не доехал до таба:\n%s", view)
	}

	m.Update(keyPress("3"))
	if view := stripANSI(m.View().Content); !strings.Contains(view, "/tmp/live.go") {
		t.Errorf("живая файловая операция не доехала до таба:\n%s", view)
	}
}

// countingView считает, сколько раз панель перерисовывала строки. Это и есть
// та работа, которая стоит дорого: тема, колонки и обрезка на каждое событие.
type countingView struct {
	view
	lines *int
}

func (c countingView) Line(th *Theme, ev parse.Event, width int) string {
	*c.lines++
	return c.view.Line(th, ev, width)
}

// watchLines подменяет вид активной панели считающим и пересобирает её строки,
// чтобы дальше счётчик мерил только новую работу.
func watchLines(m *Model) *int {
	n := 0
	p := m.pane()
	p.v = countingView{view: p.v, lines: &n}
	p.restyle()
	m.applyContent()
	n = 0
	return &n
}

// Набор фильтра НЕ пересчитывает строки: он перебирает уже готовые.
//
// Это главный долг слайса 2: полная пересборка стоила 207 мс на буфере в
// 10 000 событий, то есть каждое нажатие клавиши в фильтре подвешивало
// интерфейс на пятую долю секунды.
func TestFilterKeepsStyledRows(t *testing.T) {
	m := manyEvents(t, 300, 100, 24)
	rows := m.pane().rows
	if len(rows) != 300 {
		t.Fatalf("строк в мемо %d, событий 300", len(rows))
	}

	drawn := watchLines(m)
	m.Update(keyPress("/"))
	for _, r := range "команда-1" {
		m.Update(keyPress(string(r)))
	}

	if *drawn != 0 {
		t.Errorf("набор фильтра перерисовал %d строк — мемо не работает", *drawn)
	}
	if len(m.content()) == 0 || len(m.content()) == 300 {
		t.Errorf("фильтр не применился: строк %d", len(m.content()))
	}

	// Зато смена ширины окна строки пересобирает: колонки считаются от неё.
	rows = m.pane().rows
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m.content()
	if *drawn == 0 {
		t.Errorf("ресайз не пересобрал строки")
	}
	if len(m.pane().rows) > 0 && &m.pane().rows[0] == &rows[0] {
		t.Errorf("после ресайза мемо осталось прежним")
	}
}

// Пришедший исход перерисовывает ОДНУ свою строку, а не всю панель: в живой
// сессии результаты приезжают сотнями.
func TestResultRestylesOneRow(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(keyPress("2"))

	ts := time.Now()
	events := make([]parse.Event, 50)
	for i := range events {
		// Метод у каждого вызова свой: по нему тест и находит нужную строку.
		events[i] = parse.Event{Time: ts, Source: "main", Kind: parse.KindTool,
			Tool: fmt.Sprintf("mcp__serena__find_symbol_%d", i), ToolID: fmt.Sprintf("toolu_%d", i),
			Detail: `{"name_path":"X"}`}
	}
	m.Update(batchMsg(Batch{Events: events}))

	drawn := watchLines(m)
	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_7", Time: ts.Add(2 * time.Second), Text: "готово"},
	}}))

	if *drawn != 1 {
		t.Errorf("на один результат перерисовано %d строк", *drawn)
	}
	line := lineWith(t, m.content(), "find_symbol_7 ")
	if got := cellOf(line, mcpCellWidth); len(got) != 2 || got[0] != "ok" || got[1] != "2.0s" {
		t.Errorf("строка своего результата не дополнена: %v", got)
	}
}

// Пустая панель обязана считаться ПОСТРОЕННОЙ: иначе живое событие некуда
// дописывать, и каждая порция watcher'а пересобирала бы панель с нуля — та
// самая квадратичная растопка, от которой заведены кэши.
func TestEmptyPaneMemoIsBuilt(t *testing.T) {
	m := benchModelT(t)
	p := m.pane()
	if p.rows == nil {
		t.Fatalf("мемо пустой панели осталось nil — дописывание не заработает")
	}

	m.Push(benchEvent(1))
	if len(p.rows) != 1 {
		t.Errorf("живое событие не дописалось в мемо: строк %d", len(p.rows))
	}
}
