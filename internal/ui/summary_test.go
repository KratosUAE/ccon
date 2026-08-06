package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/KratosUAE/ccon/internal/parse"
)

// Счётчики исходов лежат массивом на statusCount ячеек. Если в parse появится
// пятый исход, он молча перестанет считаться — этот сторож ловит расхождение
// раньше, чем сводка начнёт врать.
func TestStatusCountCoversParse(t *testing.T) {
	if got := int(parse.StatusDenied) + 1; got != statusCount {
		t.Errorf("исходов в parse %d, ячеек счётчика %d", got, statusCount)
	}
	for _, s := range []parse.Status{parse.StatusPending, parse.StatusOK, parse.StatusError, parse.StatusDenied} {
		var stats paneStats
		stats.bump(s, 1)
		if stats.count(s) != 1 {
			t.Errorf("исход %v не учтён счётчиком", s)
		}
	}
}

// Сводка активного таба в футере. Отказы считаются ОТДЕЛЬНО от ошибок: в
// корпусе 85 из 252 неуспешных вызовов — отказы правила, и слитый счётчик врал
// бы про сбои втрое.
//
// Топ-сервер здесь ещё и проверка детерминизма: у serena и netscan по одному
// вызову, и при равенстве обязано побеждать меньшее имя, а не порядок обхода
// карты.
//
// У таба mcp в этой фикстуре отказов нет: строка «0 denied» здесь холостая —
// мутация, сливающая отказы с ошибками, эту проверку не краснит. Живую
// ветку «denied ≠ err» держит
// TestSummaryFollowsLiveResults. Фикстура toolFixture общая на четыре файла
// (parse/decode_test.go, parse/link_test.go, cmd/ccon/main_test.go,
// ui/pane_test.go) — заводить в неё MCP-отказ ради одной этой строки не стал:
// цена шире, чем сам сторож.
func TestTabSummaries(t *testing.T) {
	tests := []struct {
		view View
		want string
	}{
		{ViewTranscript, ""},
		{ViewMCP, "mcp: 2 calls · 1 err · 0 denied · netscan 1"},
		{ViewFiles, "files: 4 ops · R 2 · W 1 · E 1"},
	}

	for _, tt := range tests {
		t.Run(tt.view.String(), func(t *testing.T) {
			m := statusModel(t, tt.view, 120, 30)

			if got := m.pane().summary(); got != tt.want {
				t.Errorf("сводка %q, ожидалась %q", got, tt.want)
			}
			if tt.want == "" {
				return
			}
			if row := footerRow(m, "FILTER"); !strings.Contains(row, tt.want) {
				t.Errorf("сводки нет в строке футера: %q", row)
			}
		})
	}
}

// Фильтр меняет только видимое: счётчики сводки считаются по всему буферу —
// прямое требование спеки, и оно же делает сводку полезной при фильтрации.
func TestSummaryIgnoresFilter(t *testing.T) {
	m := statusModel(t, ViewFiles, 120, 30)
	before := m.pane().summary()

	m.Update(keyPress("/"))
	for _, r := range "decode" {
		m.Update(keyPress(string(r)))
	}
	if n := len(m.content()); n != 1 {
		t.Fatalf("фильтр оставил %d строк, ожидалась одна", n)
	}
	if got := m.pane().summary(); got != before {
		t.Errorf("фильтр изменил сводку: было %q, стало %q", before, got)
	}

	// Полная пересборка при включённом фильтре (ресайз панели tmux) обязана
	// пересчитать счётчики так же по всему буферу: иначе сводка молча
	// съёживается до видимого при первом же изменении ширины окна.
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.content()
	if got := m.pane().summary(); got != before {
		t.Errorf("пересборка при фильтре изменила сводку: было %q, стало %q", before, got)
	}

	// Тумблер ошибок — то же самое: он про показ, а не про счёт.
	m.Update(keyPress("esc"))
	m.Update(keyPress("e"))
	if got := m.pane().summary(); got != before {
		t.Errorf("тумблер ошибок изменил сводку: было %q, стало %q", before, got)
	}
}

// Пришедший результат переносит вызов из ожидания в свой исход, и сводка это
// показывает. Без переучёта счётчик ошибок остался бы нулевым на всю живую
// сессию: строки-то приезжают незакрытыми.
func TestSummaryFollowsLiveResults(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(keyPress("2"))

	ts := time.Now()
	m.Update(batchMsg(Batch{Events: []parse.Event{
		{Time: ts, Source: "main", Kind: parse.KindTool,
			Tool: "mcp__serena__find_symbol", ToolID: "toolu_a", Detail: `{"name_path":"X"}`},
		{Time: ts, Source: "main", Kind: parse.KindTool,
			Tool: "mcp__serena__replace_symbol_body", ToolID: "toolu_b", Detail: `{"name_path":"Y"}`},
	}}))

	if got, want := m.pane().summary(), "mcp: 2 calls · 0 err · 0 denied · serena 2"; got != want {
		t.Fatalf("сводка до результатов %q, ожидалась %q", got, want)
	}

	m.Update(batchMsg(Batch{Results: []parse.Result{
		{ToolUseID: "toolu_a", Time: ts.Add(time.Second), IsError: true, Text: "боом"},
		{ToolUseID: "toolu_b", Time: ts.Add(time.Second), IsError: true, Denial: "permission-rule", Text: "нельзя"},
	}}))

	if got, want := m.pane().summary(), "mcp: 2 calls · 1 err · 1 denied · serena 2"; got != want {
		t.Errorf("сводка после результатов %q, ожидалась %q", got, want)
	}
}

// Тумблер системного шума. Системных записей в живом корпусе 14.9 % строк
// (в основном turn_duration), и фильтром их не спрятать: фильтр оставляет
// совпавшее, а тут нужно ровно обратное.
func TestSystemToggleHidesNoise(t *testing.T) {
	ts := time.Now()
	events := []parse.Event{
		{Time: ts, Source: "main", Kind: parse.KindSystem, Detail: "turn_duration 4.2s"},
		{Time: ts, Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: "go build ./..."},
		{Time: ts, Source: "main", Kind: parse.KindSystem, Detail: "turn_duration 1.1s"},
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	if n := len(m.content()); n != 3 {
		t.Fatalf("строк до тумблера %d, ожидалось 3", n)
	}
	if !strings.Contains(stripANSI(m.View().Content), "[s] sys: on") {
		t.Errorf("состояние тумблера не показано в подсказках")
	}

	m.Update(keyPress("s"))
	shown := logText(m)
	if strings.Contains(shown, "turn_duration") {
		t.Errorf("тумблер не спрятал системные записи:\n%s", shown)
	}
	if !strings.Contains(shown, "go build") {
		t.Errorf("тумблер спрятал обычные строки:\n%s", shown)
	}
	if !strings.Contains(stripANSI(m.View().Content), "[s] sys: off") {
		t.Errorf("выключенный тумблер не показан в подсказках")
	}

	// Тумблер свой у каждого таба и возвращается обратно.
	if m.panes[ViewMCP].hideSystem {
		t.Errorf("тумблер перетёк на соседний таб")
	}
	m.Update(keyPress("s"))
	if n := len(m.content()); n != 3 {
		t.Errorf("после выключения тумблера строк %d, ожидалось 3", n)
	}

	// Живое событие приходит по тем же правилам: дописывание в кэш не должно
	// расходиться с полной пересборкой.
	m.Update(keyPress("s"))
	m.Push(parse.Event{Time: ts, Source: "main", Kind: parse.KindSystem, Detail: "turn_duration 9.9s"})
	if shown := logText(m); strings.Contains(shown, "9.9s") {
		t.Errorf("живая системная запись прошла мимо тумблера:\n%s", shown)
	}
}

// Подсказки клавиш стоят отдельной строкой именно затем, чтобы сообщение о
// потерянных строках и сводка таба не выдавливали из кадра подсказку выхода.
// Прежняя раскладка теряла её на 80 и на 100 колонках (журнал, поправка к
// отклонениям №6/№7).
func TestFooterKeepsQuitHintWithStatus(t *testing.T) {
	events, results := toolFixture(t)
	for _, w := range []int{80, 100, 120} {
		m := New(Options{Project: "p", Mode: ModeLive, Events: events, Results: results,
			View: ViewMCP, Skipped: 12})
		m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		m.status = "watch: permission denied"

		view := stripANSI(m.View().Content)
		for _, want := range []string{"[q] quit", "[s] sys: on", "unparsed lines: 12", "permission denied"} {
			if !strings.Contains(view, want) {
				t.Errorf("ширина %d: в кадре нет %q:\n%s", w, want, view)
			}
		}
	}
}
