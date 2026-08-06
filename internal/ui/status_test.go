package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/parse"
)

// statusModel собирает модель по фикстуре целиком: и вызовы, и их результаты.
func statusModel(t *testing.T, view View, w, h int) *Model {
	t.Helper()

	events, results := toolFixture(t)
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, Results: results, View: view})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// cellOf возвращает правый блок строки панели — то, что стоит на фиксированном
// месте у края окна: исход и, если таб его рисует, длительность.
func cellOf(line string, cellWidth int) []string {
	r := []rune(stripANSI(line))
	if len(r) < cellWidth {
		return nil
	}
	return strings.Fields(string(r[len(r)-cellWidth:]))
}

// lineWith находит строку панели по подстроке.
func lineWith(t *testing.T, lines []string, needle string) string {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(stripANSI(line), needle) {
			return line
		}
	}
	t.Fatalf("строки с %q нет:\n%s", needle, stripANSI(strings.Join(lines, "\n")))
	return ""
}

// Исход и длительность доезжают до строки таба mcp: успех, сбой инструмента и
// время ответа на своих местах.
func TestMCPPaneShowsOutcome(t *testing.T) {
	m := statusModel(t, ViewMCP, 100, 30)
	lines := m.content()

	tests := []struct {
		needle string
		want   []string
	}{
		{"find_symbol", []string{"ok", "0.0s"}},          // 32 мс
		{"netscan_client_roam", []string{"ERR", "0.6s"}}, // 644 мс
	}
	for _, tt := range tests {
		t.Run(tt.needle, func(t *testing.T) {
			got := cellOf(lineWith(t, lines, tt.needle), mcpCellWidth)
			if len(got) != len(tt.want) || got[0] != tt.want[0] || got[1] != tt.want[1] {
				t.Errorf("правый блок строки %v, ожидался %v", got, tt.want)
			}
		})
	}
}

// Таб файлов рисует исход, но не длительность: у чтения и правки медиана
// 94 мс, колонка была бы шумом, а место нужнее пути. Отказ показан отдельно от
// сбоя: 85 ошибок корпуса из 252 — это отказы, и слитый исход врал бы втрое.
func TestFilesPaneShowsOutcome(t *testing.T) {
	m := statusModel(t, ViewFiles, 100, 30)
	lines := m.content()

	tests := []struct {
		needle string
		want   string
	}{
		{"/home/user/.ssh/config", "ERR"},                       // сбой инструмента
		{"/home/user/Devs/proj/internal/parse/decode.go", "ok"}, // успех
		{"/home/user/Devs/proj/README.md", "DENY"},              // отказ правила
		{"R  ?", "·"}, // результата не было
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := cellOf(lineWith(t, lines, tt.needle), fileCellWidth)
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("правый блок строки %v, ожидался [%s]", got, tt.want)
			}
		})
	}

	// Длительности в строке файловой операции нет вовсе: цифры времени тут
	// взяться неоткуда, кроме колонки отметки.
	line := stripANSI(lineWith(t, lines, "decode.go"))
	if strings.Contains(line, "0.1s") {
		t.Errorf("в табе файлов появилась длительность: %q", line)
	}
}

// Текст ошибки замещает аргументы у неуспешной строки: причина отказа важнее
// аргументов, а таблица остаётся по строке на событие.
func TestFailTextReplacesArguments(t *testing.T) {
	m := statusModel(t, ViewMCP, 120, 30)
	line := stripANSI(lineWith(t, m.content(), "netscan_client_roam"))

	if !strings.Contains(line, "netscan: roaming path") {
		t.Errorf("текст ошибки не показан: %q", line)
	}
	if strings.Contains(line, "00:00:5e:00:53:00") {
		t.Errorf("аргументы вытеснили текст ошибки: %q", line)
	}

	// У успешного вызова показаны именно аргументы.
	ok := stripANSI(lineWith(t, m.content(), "find_symbol"))
	if !strings.Contains(ok, "name_path") {
		t.Errorf("у успешного вызова пропали аргументы: %q", ok)
	}
}

// Незакрытый вызов остаётся без исхода и без длительности: «результата ещё
// нет» — это состояние живой сессии, и рисовать «0s» значило бы соврать про
// мгновенный ответ.
func TestPendingCallHasNoDuration(t *testing.T) {
	m := statusModel(t, ViewFiles, 100, 30)
	cell := cellOf(lineWith(t, m.content(), "R  ?"), fileCellWidth)

	if len(cell) != 1 || cell[0] != "·" {
		t.Errorf("незакрытый вызов помечен как %v", cell)
	}
	if got := durText(0); strings.TrimSpace(got) != "" {
		t.Errorf("неизвестная длительность нарисована как %q", got)
	}
	if got := len([]rune(durText(15*time.Hour + 26*time.Minute))); got != durWidth {
		t.Errorf("самая долгая длительность заняла %d колонок вместо %d", got, durWidth)
	}
}

// durText не должен ни соврать нулём про мгновенный ответ, ни разъехаться
// колонкой при длительности длиннее наблюдённого максимума корпуса: ветка
// часов формата (%dh%dm) ничем не ограничена, а durWidth выбрана по
// наблюдению, не по пределу формата.
func TestDurTextWidth(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string // ожидаемый текст после обрезки пробелов; "" — пусто (неизвестна)
	}{
		{"неизвестна", 0, ""},
		{"суб-миллисекундная округляется в 0", 500 * time.Microsecond, ""},
		{"наблюдённый максимум корпуса", 15*time.Hour + 26*time.Minute, "15h26m"},
		{"дольше предела формата часов", 999*time.Hour + 59*time.Minute, "999h5…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := durText(tt.d)
			if n := len([]rune(got)); n != durWidth {
				t.Fatalf("durText(%s) = %q, занял %d колонок вместо %d", tt.d, got, n, durWidth)
			}
			if trimmed := strings.TrimSpace(got); trimmed != tt.want {
				t.Errorf("durText(%s) = %q, ожидалось %q", tt.d, got, tt.want)
			}
		})
	}
}

// Результат, приехавший позже вызова, дописывает УЖЕ показанную строку.
// Это главный сценарий живого режима: строка появляется без исхода и через
// секунду дополняется им на месте.
func TestLiveResultUpdatesShownLine(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	call := parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
		Tool: "mcp__serena__find_symbol", ToolID: "toolu_live", Detail: `{"name_path":"X"}`}

	m.Update(batchMsg(Batch{Events: []parse.Event{call}}))
	m.Update(keyPress("2"))
	if cell := cellOf(lineWith(t, m.content(), "find_symbol"), mcpCellWidth); len(cell) != 1 || cell[0] != "·" {
		t.Fatalf("вызов появился с исходом %v, ожидалось ожидание", cell)
	}

	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_live", Time: call.Time.Add(2100 * time.Millisecond), Text: "готово"},
	}}))

	line := lineWith(t, m.content(), "find_symbol")
	if got := cellOf(line, mcpCellWidth); len(got) != 2 || got[0] != "ok" || got[1] != "2.1s" {
		t.Errorf("строка дополнена как %v, ожидалось [ok 2.1s]", got)
	}
	if n := len(m.content()); n != 1 {
		t.Errorf("строк на табе %d: результат породил вторую строку вместо дописывания", n)
	}
}

// Инвариант порядка: вся пачка сначала встаёт на учёт и только потом
// сшивается. Результат, приехавший ОДНОЙ порцией со своим вызовом, обязан
// сшиться независимо от того, как порция разложена внутри.
func TestBatchTracksBeforeResolving(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	ts := time.Now()

	m.Update(batchMsg(Batch{
		Results: []parse.Result{{ToolUseID: "toolu_same", Time: ts.Add(400 * time.Millisecond), Text: "готово"}},
		Events: []parse.Event{{Time: ts, Source: "main", Kind: parse.KindTool,
			Tool: "mcp__context7__query-docs", ToolID: "toolu_same", Detail: `{"q":"x"}`}},
	}))

	m.Update(keyPress("2"))
	if got := cellOf(lineWith(t, m.content(), "query-docs"), mcpCellWidth); len(got) != 2 || got[0] != "ok" {
		t.Errorf("вызов и результат из одной порции не сшились: %v", got)
	}
}

// То же для архивного пути: события приезжают отсортированными по времени, а
// результаты — в порядке чтения файлов, и сшивка не должна от этого зависеть.
func TestArchiveResultsDoNotDependOnOrder(t *testing.T) {
	events, results := toolFixture(t)
	// Разворачиваем оба среза: порядок подачи не должен значить ничего.
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, Results: results, View: ViewFiles})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	if got := cellOf(lineWith(t, m.content(), "README.md"), fileCellWidth); len(got) != 1 || got[0] != "DENY" {
		t.Errorf("при перевёрнутом порядке отказ приехал как %v", got)
	}
}

// Дубль результата (в корпусе такой есть) не должен переписывать уже сшитую
// строку: второй раз сшивать нечего.
func TestDuplicateResultChangesNothing(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	ts := time.Now()
	m.Update(batchMsg(Batch{Events: []parse.Event{{Time: ts, Source: "main", Kind: parse.KindTool,
		Tool: "mcp__notes__create_note", ToolID: "toolu_dup", Detail: `{"path":"/tmp/n.md"}`}}}))
	m.Update(keyPress("2"))

	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_dup", Time: ts.Add(300 * time.Millisecond), Text: "готово"},
	}}))
	first := stripANSI(lineWith(t, m.content(), "create_note"))

	// Тот же вызов, но с другой отметкой времени: будь сшивка неидемпотентной,
	// длительность строки переписалась бы.
	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_dup", Time: ts.Add(9 * time.Second), IsError: true, Text: "ой"},
	}}))
	second := stripANSI(lineWith(t, m.content(), "create_note"))

	if first != second {
		t.Errorf("дубль результата переписал строку:\nбыло:  %q\nстало: %q", first, second)
	}
	if got := cellOf(second, mcpCellWidth); len(got) != 2 || got[0] != "ok" || got[1] != "0.3s" {
		t.Errorf("строка после дубля: %v, ожидалось [ok 0.3s]", got)
	}
}

// Пришедший результат не сбрасывает кэш транскрипта (в живой сессии это десять
// тысяч строк): вид транскрипта от исхода не зависит. Оговорка — панель без
// фильтра и без тумблера ошибок; когда исход влияет на отбор, кэш обязан
// сброситься, это проверяют TestErrOnlyOnTranscriptSeesLiveResult и
// TestTranscriptFilterSeesLiveFailText.
func TestResultSparesTranscriptCache(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	ts := time.Now()
	m.Update(batchMsg(Batch{Events: []parse.Event{{Time: ts, Source: "main", Kind: parse.KindTool,
		Tool: "mcp__serena__find_symbol", ToolID: "toolu_1", Detail: `{"name_path":"X"}`}}}))

	// Прогреваем кэши всех табов и возвращаемся на транскрипт.
	for _, k := range []string{"2", "3", "1"} {
		m.Update(keyPress(k))
	}
	before := m.panes[ViewTranscript].cachePln
	if len(before) == 0 {
		t.Fatalf("кэш транскрипта пуст")
	}
	if m.panes[ViewMCP].cachePln == nil {
		t.Fatalf("кэш таба mcp не прогрелся")
	}

	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_1", Time: ts.Add(time.Second), Text: "готово"},
	}}))

	after := m.panes[ViewTranscript].cachePln
	if len(after) != len(before) || &after[0] != &before[0] {
		t.Errorf("кэш транскрипта пересобран из-за результата вызова")
	}
	if m.panes[ViewMCP].cachePln != nil {
		t.Errorf("кэш таба mcp не сброшен: строка исхода осталась бы прежней")
	}
	// Таб файлов этого события не берёт — пачкать его нечем.
	if m.panes[ViewFiles].cachePln == nil {
		t.Errorf("кэш таба files сброшен чужим событием")
	}
}

// Исход и текст ошибки дописываются в событие
// буфера ЗАДНИМ ЧИСЛОМ (applyUpdate), но StatusAware() транскрипта — всегда
// false: он рисует строку одинаково при любом исходе. От исхода на
// транскрипте зависит не РЕНДЕР, а ОТБОР — тумблер e (через failed) и фильтр
// (через parse.Haystack → ev.Fail). Без учёта отбора кэш держит устаревший
// список строк до первого постороннего пересбора (ресайз, вытеснение…).
func TestErrOnlyOnTranscriptSeesLiveResult(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(keyPress("e")) // тумблер включён ДО прихода вызова

	ts := time.Now()
	call := parse.Event{Time: ts, Source: "main", Kind: parse.KindTool,
		Tool: "Bash", ToolID: "toolu_fail", Detail: "rm -rf /tmp/x"}
	m.Update(batchMsg(Batch{Events: []parse.Event{call}}))

	// Вызов ещё pending — тумблер его прячет, и кэш строится (запоминается) пустым.
	if shown := strings.Join(m.content(), "\n"); strings.Contains(shown, "rm -rf /tmp/x") {
		t.Fatalf("незакрытый вызов показан при включённом тумблере: %q", shown)
	}

	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_fail", Time: ts.Add(time.Second), IsError: true, Text: "боом"},
	}}))

	shown := stripANSI(strings.Join(m.content(), "\n"))
	if !strings.Contains(shown, "rm -rf /tmp/x") {
		t.Errorf("строка транскрипта, ставшая неуспешной, не появилась при включённом тумблере: %q", shown)
	}
}

// Тот же дефект для фильтра: поиск обязан видеть ровно
// то, что показано, включая текст ошибки — а он приезжает позже самого
// вызова. Без учёта p.filter в отборе кэш транскрипта не замечает совпадение,
// появившееся задним числом.
func TestTranscriptFilterSeesLiveFailText(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(keyPress("/"))
	for _, r := range "upstream" {
		m.Update(keyPress(string(r)))
	}

	ts := time.Now()
	call := parse.Event{Time: ts, Source: "main", Kind: parse.KindTool,
		Tool: "Bash", ToolID: "toolu_upstream", Detail: "curl example.com"}
	m.Update(batchMsg(Batch{Events: []parse.Event{call}}))

	if n := len(m.content()); n != 0 {
		t.Fatalf("до результата фильтру откликаться не на что, строк %d", n)
	}

	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_upstream", Time: ts.Add(time.Second), IsError: true, Text: "upstream: status 429"},
	}}))

	// Строка транскрипта текст ошибки не рисует (это дело mcp/files), но
	// Haystack его видит — фильтр обязан найти вызов по слову из ev.Fail,
	// а не по тому, что нарисовано в колонке.
	shown := stripANSI(strings.Join(m.content(), "\n"))
	if !strings.Contains(shown, "curl example.com") {
		t.Errorf("фильтр не увидел текст ошибки, приехавший позже вызова: %q", shown)
	}
}

// Тумблер e оставляет неуспешные исходы — и сбои, и отказы. Прятать отказ
// нельзя: искать «почему не сработало» приходится там же.
func TestErrOnlyKeepsFailuresAndDenials(t *testing.T) {
	m := statusModel(t, ViewFiles, 100, 30)
	if len(m.content()) != 4 {
		t.Fatalf("строк до тумблера %d, ожидалось 4", len(m.content()))
	}

	m.Update(keyPress("e"))
	shown := stripANSI(strings.Join(m.content(), "\n"))

	for _, want := range []string{"/home/user/.ssh/config", "/home/user/Devs/proj/README.md"} {
		if !strings.Contains(shown, want) {
			t.Errorf("тумблер спрятал неуспешный вызов %q:\n%s", want, shown)
		}
	}
	for _, gone := range []string{"decode.go", "R  ?"} {
		if strings.Contains(shown, gone) {
			t.Errorf("тумблер оставил %q:\n%s", gone, shown)
		}
	}
	if !strings.Contains(stripANSI(m.View().Content), "[e] err: on") {
		t.Errorf("состояние тумблера не показано в подсказках")
	}

	// Тумблер свой у каждого таба и возвращается обратно.
	if m.panes[ViewMCP].errOnly {
		t.Errorf("тумблер перетёк на соседний таб")
	}
	m.Update(keyPress("e"))
	if len(m.content()) != 4 {
		t.Errorf("после выключения тумблера строк %d, ожидалось 4", len(m.content()))
	}
}

// На транскрипте тумблер оставляет красные строки ошибок: исхода вызова у них
// нет, но именно они там и означают неудачу.
func TestErrOnlyOnTranscriptKeepsErrorRows(t *testing.T) {
	m := statusModel(t, ViewTranscript, 100, 30)
	m.Update(keyPress("e"))

	shown := stripANSI(strings.Join(m.content(), "\n"))
	if !strings.Contains(shown, "ERROR") {
		t.Errorf("тумблер спрятал строки ошибок транскрипта:\n%s", shown)
	}
	if strings.Contains(shown, "AskUserQ") {
		t.Errorf("тумблер оставил обычный вызов:\n%s", shown)
	}
}

// Пустой экран объясняет себя и при включённом тумблере: «ошибок нет» — это
// хорошая новость, а не поломка показа.
func TestErrOnlyEmptyExplainsItself(t *testing.T) {
	events := []parse.Event{{Time: time.Now(), Source: "main", Kind: parse.KindTool,
		Tool: "Read", ToolID: "t1", Detail: "a.go", Path: "/p/a.go"}}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, View: ViewFiles})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.Update(keyPress("e"))
	if view := stripANSI(m.View().Content); !strings.Contains(view, "no failed calls in this session") {
		t.Errorf("пустой экран тумблера промолчал:\n%s", view)
	}

	m.Update(keyPress("/"))
	for _, r := range "zzz" {
		m.Update(keyPress(string(r)))
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, `no failures match "zzz"`) {
		t.Errorf("пустой экран тумблера с фильтром промолчал:\n%s", view)
	}
}

// Пустой экран после тумблера s объясняет себя иначе, чем «событий нет вовсе»:
// события есть, их спрятал тумблер, а не отсутствие таких записей — путать
// эти состояния запрещает комментарий emptyText.
func TestHideSystemEmptyExplainsItself(t *testing.T) {
	events := []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindSystem, Detail: "turn_duration 4.2s"},
		{Time: time.Now(), Source: "main", Kind: parse.KindSystem, Detail: "turn_duration 1.1s"},
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.Update(keyPress("s"))
	view := stripANSI(m.View().Content)
	if strings.Contains(view, "no events in this session") {
		t.Errorf("пустой экран тумблера s соврал про отсутствие событий:\n%s", view)
	}
	if !strings.Contains(view, "only system records here") {
		t.Errorf("пустой экран тумблера s промолчал про причину:\n%s", view)
	}
}

// Живое событие, пришедшее при включённом тумблере, показывается только если
// оно неуспешно: дописывание в кэш обязано жить по тем же правилам, что и
// полная пересборка.
func TestErrOnlyAppliesToLiveEvents(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(keyPress("3"))
	m.Update(keyPress("e"))

	ts := time.Now()
	m.Update(batchMsg(Batch{Events: []parse.Event{
		{Time: ts, Source: "main", Kind: parse.KindTool, Tool: "Read", ToolID: "t1",
			Detail: "a.go", Path: "/p/a.go"},
		{Time: ts, Source: "main", Kind: parse.KindTool, Tool: "Write", ToolID: "t2",
			Detail: "b.go", Path: "/p/b.go", Status: parse.StatusError, Fail: "не пишется"},
	}}))

	shown := stripANSI(strings.Join(m.content(), "\n"))
	if !strings.Contains(shown, "/p/b.go") {
		t.Errorf("неуспешное живое событие не показано:\n%s", shown)
	}
	if strings.Contains(shown, "/p/a.go") {
		t.Errorf("тумблер пропустил успешное живое событие:\n%s", shown)
	}
}

// mcpOutcomes/filesOutcomes — правые блоки строк фикстуры toolFixture в
// порядке появления. Позиционная проверка, а не поиск по подстроке: на 40
// колонках сервер и метод обрезаются вплоть до полного исчезновения
// а блок исхода — никогда, ради этого он и прижат к краю.
var mcpOutcomes = [][2]string{{"ok", "0.0s"}, {"ERR", "0.6s"}}
var filesOutcomes = []string{"ERR", "ok", "·", "DENY"}

// Правый блок исхода не должен разносить кадр по ширине: он отбирает колонки
// у аргументов, а не приписывается сверх окна. И не должен пропадать сам —
// это тот же инвариант, только с другой стороны: узкое окно жертвует
// аргументами, а не исходом (мутация withCell, теряющая блок на width<=50,
// раньше не роняла ни один тест).
func TestStatusFramesFitWindow(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 20}, {80, 20}, {40, 16}} {
		for _, v := range []View{ViewMCP, ViewFiles} {
			m := statusModel(t, v, size.w, size.h)

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

			content := m.content()
			cellWidth, want := mcpCellWidth, len(mcpOutcomes)
			if v == ViewFiles {
				cellWidth, want = fileCellWidth, len(filesOutcomes)
			}
			if len(content) != want {
				t.Fatalf("%s %dx%d: строк %d, ожидалось %d", v, size.w, size.h, len(content), want)
			}
			for i, line := range content {
				got := cellOf(line, cellWidth)
				switch v {
				case ViewMCP:
					want := mcpOutcomes[i]
					if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
						t.Errorf("%s %dx%d: строка %d правый блок %v, ожидался %v", v, size.w, size.h, i, got, want)
					}
				case ViewFiles:
					want := filesOutcomes[i]
					if len(got) != 1 || got[0] != want {
						t.Errorf("%s %dx%d: строка %d правый блок %v, ожидался [%s]", v, size.w, size.h, i, got, want)
					}
				}
			}

			t.Logf("\n=== ТАБ %s %dx%d (с исходами) ===\n%s\n=== конец кадра ===", v, size.w, size.h, stripANSI(frame))
		}
	}
}

// Живой результат файловой операции дополняет УЖЕ ПОКАЗАННУЮ строку таба files:
// строка приезжает с «·», исход приходит следующей порцией. Без этого сторожа
// filesView.StatusAware() не проверен ничем (покрытие 0 %) —
// вся семья statusModel строит модель через New(Events, Results) сразу, где
// applyResults застаёт p.rows == nil и выходит раньше проверки StatusAware.
func TestLiveFileResultUpdatesShownLine(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(keyPress("3")) // таб files открыт ЗАРАНЕЕ — мемо построено
	ts := time.Now()

	m.Update(batchMsg(Batch{Events: []parse.Event{
		{Time: ts, Source: "main", Kind: parse.KindTool, Tool: "Write",
			ToolID: "toolu_w", Path: "/home/user/.ssh/config"},
	}}))
	if got := cellOf(lineWith(t, m.content(), "config"), fileCellWidth); len(got) != 1 || got[0] != "·" {
		t.Fatalf("незакрытая файловая операция помечена %v, ожидалось [·]", got)
	}

	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_w", Time: ts.Add(time.Second), IsError: true,
			Denial: "permission-rule", Text: "нельзя"},
	}}))
	if got := cellOf(lineWith(t, m.content(), "config"), fileCellWidth); len(got) != 1 || got[0] != "DENY" {
		t.Errorf("строка files не получила своего исхода: %v", got)
	}
}

// Буфер адресует обновление по ключу сшивки, а не по индексу: пока результат
// летел, голова могла вытесниться.
func TestRingApplyUpdate(t *testing.T) {
	r := newRing(10)
	r.push(parse.Event{Source: "main", Tool: "Read", ToolID: "t1", Detail: "первое"})
	r.push(parse.Event{Source: "main", Tool: "Read", Detail: "без ключа"})

	ev, seq, ok := r.applyUpdate(parse.Update{ToolID: "t1", Status: parse.StatusOK, Dur: time.Second})
	if !ok {
		t.Fatalf("обновление не нашло своё событие")
	}
	if ev.Detail != "первое" || ev.Status != parse.StatusOK || ev.Dur != time.Second {
		t.Errorf("вернулось %+v", ev)
	}
	// Сквозной номер нужен панели, чтобы найти свою строку: адресация по нему,
	// а не по индексу, переживает вытеснение головы.
	if got, live := r.at(seq); !live || got.Detail != "первое" {
		t.Errorf("номер %d указывает не на то событие: %+v (живо: %v)", seq, got, live)
	}
	if got := r.events[0]; got.Status != parse.StatusOK || got.Dur != time.Second {
		t.Errorf("событие в буфере не обновлено: %+v", got)
	}

	// Неизвестный ключ — тихий отказ, а не фантомная строка.
	if _, _, ok := r.applyUpdate(parse.Update{ToolID: "нет такого", Status: parse.StatusOK}); ok {
		t.Errorf("обновление сшилось с несуществующим событием")
	}
	if r.len() != 2 {
		t.Errorf("в буфере %d событий: обновление завело новое", r.len())
	}
}

// Вытесненное событие обновить нечем, и карта ключей за собой не тянет
// вытесненных: иначе она росла бы вместе с длиной сессии.
func TestRingApplyUpdateAfterEviction(t *testing.T) {
	r := newRing(10)
	for i := range 40 {
		r.push(parse.Event{Source: "main", Tool: "Read",
			ToolID: fmt.Sprintf("t%d", i), Detail: fmt.Sprintf("строка-%d", i)})
	}

	if _, _, ok := r.applyUpdate(parse.Update{ToolID: "t0", Status: parse.StatusOK}); ok {
		t.Errorf("обновление сшилось с вытесненным событием")
	}
	if len(r.byTool) > r.len() {
		t.Errorf("карта ключей держит %d записей при %d событиях", len(r.byTool), r.len())
	}

	last := fmt.Sprintf("t%d", 39)
	ev, _, ok := r.applyUpdate(parse.Update{ToolID: last, Status: parse.StatusError, Fail: "упало"})
	if !ok {
		t.Fatalf("последнее событие потеряло свой ключ после вытеснения")
	}
	if ev.Detail != "строка-39" || ev.Fail != "упало" {
		t.Errorf("обновилось не то событие: %+v", ev)
	}
}
