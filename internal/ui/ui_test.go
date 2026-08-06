package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/cost"
	"github.com/KratosUAE/ccon/internal/parse"
	"github.com/KratosUAE/ccon/internal/session"
)

func sampleTotals() cost.Totals {
	return cost.Totals{
		Input: 5200, Output: 817000, CacheRead: 71_200_000,
		Cache5m: 2_800_000, Cache1h: 300_000,
		Requests: 214, CostUSD: 24.18,
		Models: []cost.ModelCount{
			{Model: "claude-opus-5", Count: 214},
			{Model: "claude-opus-4-8", Count: 3},
		},
	}
}

func sampleEvents() []parse.Event {
	ts := time.Date(2026, 8, 4, 10, 31, 4, 0, time.Local)
	return []parse.Event{
		{Time: ts, Source: "main", Kind: parse.KindTool, Tool: "Read", Detail: "demo-repo/PROMPT.md"},
		{Time: ts, Source: "main", Kind: parse.KindDelegate, Tool: "Agent", Detail: "go-code-adapter фикс"},
		{Time: ts, Source: "go-code-adapter", Kind: parse.KindError, Detail: "No such tool available"},
	}
}

func sampleModel(w, h int) *Model {
	m := New(Options{
		Project: "claude_con_ecc",
		Model:   "claude-opus-5",
		Effort:  "high",
		Mode:    "archive",
		Events:  sampleEvents(),
		Totals:  sampleTotals(),
		Agents:  []Agent{{Name: "main", Count: 41}, {Name: "go-code-adapter", Count: 157}},
	})
	if w > 0 {
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	}
	return m
}

func TestHumanNumber(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1.0k"},
		{5200, "5.2k"},
		{99_900, "99.9k"},
		{817_000, "817k"},
		{71_200_000, "71.2M"},
		{1_500_000_000, "1.5G"},
		{-5, "0"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := humanNumber(tt.in); got != tt.want {
				t.Errorf("humanNumber(%d)=%q, ожидалось %q", tt.in, got, tt.want)
			}
		})
	}
}

// Цвет источника обязан быть стабилен в пределах сессии: иначе взгляд
// перестаёт цепляться за агента.
func TestThemeColorStability(t *testing.T) {
	th := NewTheme()

	// lipgloss.Style сравнивать нельзя (внутри срез цветов), поэтому
	// проверяем стабильное представление — назначенный цвет палитры.
	first := th.colorName("go-code-adapter")
	for range 10 {
		if th.colorName("go-code-adapter") != first {
			t.Fatalf("цвет источника скачет между вызовами")
		}
	}

	seen := map[string]string{}
	for _, name := range []string{"main", "go-code-adapter", "go-linter", "rust-fixer", "kotlin-verifier"} {
		color := th.colorName(name)
		if color == "" {
			t.Fatalf("источнику %q не назначен цвет", name)
		}
		if other, busy := seen[color]; busy {
			t.Errorf("цвет %s источника %q уже занят источником %q", color, name, other)
		}
		seen[color] = name
	}

	// Палитра не бесконечна: шестой источник цвет переиспользует, но
	// назначение обязано остаться стабильным.
	sixth := th.colorName("шестой")
	if th.colorName("шестой") != sixth {
		t.Errorf("цвет шестого источника не закрепился")
	}
}

func TestFooterEssentials(t *testing.T) {
	got := Footer(FooterInput{Totals: sampleTotals(), Agents: []Agent{{Name: "main", Count: 41}}}, NewTheme(), 100)

	for _, want := range []string{
		"MODELS", "claude-opus-5", "TOKENS", "5.2k", "817k", "71.2M",
		"COST", "$24.18", "at API rates", "Max subscription", "AGENTS", "main",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в футере нет %q:\n%s", want, got)
		}
	}
	// Оговорка про оценку обязательна по спеке.
	if !strings.Contains(got, "not actually billed") {
		t.Errorf("нет оговорки о том, что списания нет:\n%s", got)
	}
}

// Футер обязан держать ширину: перенос ломает раскладку, а обрыв ANSI
// посреди последовательности красит весь экран.
func TestFooterFitsWidth(t *testing.T) {
	for _, w := range []int{40, 60, 80, 120} {
		got := Footer(FooterInput{Totals: sampleTotals(), Agents: []Agent{{Name: "main", Count: 41}}}, NewTheme(), w)
		lines := strings.Split(got, "\n")

		if len(lines) != footerHeight {
			t.Errorf("ширина %d: строк футера %d, ожидалось %d", w, len(lines), footerHeight)
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("ширина %d: строка %d занимает %d колонок:\n%q", w, i, got, line)
			}
		}
	}
}

func TestHeader(t *testing.T) {
	got := Header("claude_con_ecc", "claude-opus-5", "high", "archive", NewTheme(), 100)

	for _, want := range []string{"claude_con", "claude_con_ecc", "opus-5", "effort:high", "archive"} {
		if !strings.Contains(got, want) {
			t.Errorf("в шапке нет %q:\n%s", want, got)
		}
	}

	for _, w := range []int{40, 60, 80, 120} {
		line := Header("claude_con_ecc", "claude-opus-5", "high", "archive", NewTheme(), w)
		if strings.Contains(line, "\n") {
			t.Errorf("ширина %d: шапка расползлась на несколько строк", w)
		}
		if got := lipgloss.Width(line); got > w {
			t.Errorf("ширина %d: шапка занимает %d колонок", w, got)
		}
	}
}

// Ресайз панели tmux обязан перестраивать раскладку без остатка и артефактов.
func TestLayoutHeights(t *testing.T) {
	// Инвариант «зоны укладываются без остатка» держится, пока логу хватает
	// хотя бы одной строки; теснота проверяется отдельно.
	for _, h := range []int{headerHeight + footerHeight + 1, 24, 40, 80} {
		m := sampleModel(80, h)

		total := headerHeight + m.viewportHeight() + footerHeight
		if total != h {
			t.Errorf("высота %d: сумма зон %d (лог %d)", h, total, m.viewportHeight())
		}
		if m.viewportHeight() < 1 {
			t.Errorf("высота %d: лог схлопнулся в %d строк", h, m.viewportHeight())
		}

		view := m.View().Content
		if lines := strings.Count(view, "\n") + 1; lines > h {
			t.Errorf("высота %d: кадр занял %d строк", h, lines)
		}
	}
}

// Спека просит ~70/30: на обычном окне лог обязан занимать большую часть.
func TestLogTakesMostOfTheWindow(t *testing.T) {
	m := sampleModel(100, 40)
	if share := float64(m.viewportHeight()) / 40; share < 0.6 {
		t.Errorf("лог занимает %.0f%% окна, ожидалось около 70%%", share*100)
	}
}

// До первого WindowSizeMsg View() обязан не паниковать.
func TestViewBeforeWindowSize(t *testing.T) {
	m := sampleModel(0, 0)

	view := m.View()
	if view.Content == "" {
		t.Errorf("пустой кадр до первого WindowSizeMsg")
	}
	if !strings.Contains(view.Content, "claude_con") {
		t.Errorf("заглушка не называет себя:\n%s", view.Content)
	}
}

// Alt-screen в v2 включается полем View, а не опцией NewProgram.
func TestViewUsesAltScreen(t *testing.T) {
	if !sampleModel(80, 24).View().AltScreen {
		t.Errorf("AltScreen выключен: TUI затопчет вывод терминала")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		t.Run(key, func(t *testing.T) {
			m := sampleModel(80, 24)
			_, cmd := m.Update(keyPress(key))
			if cmd == nil {
				t.Fatalf("клавиша %q не вернула команду", key)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("клавиша %q не завершает программу", key)
			}
		})
	}
}

// Докрутка до низа возвращает автоскролл сама, как в less +F.
func TestScrollBackToBottomRestoresFollow(t *testing.T) {
	m := manyEvents(t, 100, 80, 20)

	for range 3 {
		m.Update(keyPress("up"))
	}
	if m.pane().autoFollow {
		t.Fatalf("автоскролл не погас при прокрутке вверх")
	}

	for range 3 {
		m.Update(keyPress("down"))
	}
	if !m.pane().autoFollow {
		t.Errorf("докрутка вниз не вернула автоскролл")
	}
}

// Вытеснение амортизировано: сдвиг буфера случается не на каждое событие.
func TestRingEvictionIsAmortized(t *testing.T) {
	r := newRing(100)
	evictions := 0
	for i := range 400 {
		if _, evicted := r.push(parse.Event{Source: "main", Detail: fmt.Sprintf("%d", i)}); evicted {
			evictions++
		}
	}

	if r.len() > 100+100/4 {
		t.Errorf("буфер вырос до %d при ёмкости 100", r.len())
	}
	// Без амортизации сдвиг был бы на каждом событии после сотого — 300 раз.
	if evictions > 20 {
		t.Errorf("сдвигов буфера %d, ожидалась амортизация", evictions)
	}
}

// Прочие клавиши в этом слайсе ничего не делают: прокрутка — в S7.
func TestOtherKeysDoNotQuit(t *testing.T) {
	m := sampleModel(80, 24)
	// Пустая команда тоже проверяется: раньше при cmd == nil тест не
	// утверждал ничего и был бы зелёным при любом поведении.
	_, cmd := m.Update(keyPress("x"))
	if cmd == nil {
		return
	}
	if _, ok := cmd().(tea.QuitMsg); ok {
		t.Errorf("посторонняя клавиша завершила программу")
	}
}

// Пачка обязана давать тот же результат, что и вставка по одному.
func TestPushBatchMatchesSingle(t *testing.T) {
	events := []parse.Event{benchEvent(1), benchEvent(2), benchEvent(3)}

	one := benchModelT(t)
	for _, ev := range events {
		one.Push(ev)
	}
	batch := benchModelT(t)
	batch.PushBatch(events)

	if one.View().Content != batch.View().Content {
		t.Errorf("пачка и поштучная вставка дали разные кадры")
	}
}

func benchModelT(t *testing.T) *Model {
	t.Helper()
	m := New(Options{Project: "p", Mode: ModeArchive})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

// Ctrl+C обязан работать и из режима ввода фильтра.
func TestCtrlCQuitsFromFilterInput(t *testing.T) {
	m := sampleModel(80, 24)
	m.Update(keyPress("/"))
	if !m.pane().filtering {
		t.Fatalf("режим ввода фильтра не открылся")
	}

	_, cmd := m.Update(keyPress("ctrl+c"))
	if cmd == nil {
		t.Fatalf("Ctrl+C из фильтра не вернул команду")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Ctrl+C из фильтра не завершает программу")
	}
}

// footerRow достаёт строку футера по её заголовку из настоящего кадра.
func footerRow(m *Model, label string) string {
	for _, line := range strings.Split(stripANSI(m.View().Content), "\n") {
		if strings.HasPrefix(line, label) {
			return line
		}
	}
	return ""
}

// priceRow достаёт строку ЦЕНА из настоящего кадра.
func priceRow(m *Model) string { return footerRow(m, "COST") }

// Кадр обязан содержать события лога и футер одновременно.
func TestViewShowsLogAndFooter(t *testing.T) {
	view := sampleModel(100, 24).View().Content

	for _, want := range []string{"demo-repo/PROMPT.md", "go-code-adapter", "TOKENS", "COST"} {
		if !strings.Contains(view, want) {
			t.Errorf("в кадре нет %q:\n%s", want, view)
		}
	}
}

// Строки собираются один раз и лежат в кэше панели: пересобирать их на каждом
// кадре — работа, линейная от длины сессии.
func TestStyledOnce(t *testing.T) {
	m := sampleModel(80, 24)
	if m.log.len() != len(sampleEvents()) {
		t.Fatalf("событий в буфере %d, ожидалось %d", m.log.len(), len(sampleEvents()))
	}

	before := m.pane().cachePln
	if len(before) != len(sampleEvents()) {
		t.Fatalf("строк в кэше %d, событий %d", len(before), len(sampleEvents()))
	}

	m.View()
	m.View()
	after := m.pane().cachePln
	if len(after) != len(before) || &after[0] != &before[0] {
		t.Errorf("кэш строк пересобран при рендере")
	}
}

// Управляющие символы из данных не должны доезжать до терминала.
func TestStyledSanitizesControlChars(t *testing.T) {
	ev := parse.Event{
		Time: time.Now(), Source: "\x1b[31mЗЛОЙ", Kind: parse.KindTool,
		Tool: "Bash", Detail: parse.Truncate("\x1b[31mEVIL", 50),
	}
	got := NewTheme().Styled(ev)

	if strings.Contains(got, "\x1b[31mEVIL") {
		t.Errorf("escape из данных прошёл в вывод: %q", got)
	}
	if !strings.Contains(got, "[31mEVIL") {
		t.Errorf("текст детали потерян: %q", got)
	}
	// Имя источника управляется извне так же, как деталь.
	if strings.Contains(got, "\x1b[31mЗЛОЙ") {
		t.Errorf("escape из имени источника прошёл в вывод: %q", got)
	}
}

// keyPress собирает сообщение о нажатии клавиши, как его шлёт bubbletea v2.
func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
}

// В тесноте ценнее расход, чем лог: футер обязан уцелеть целиком.
func TestTinyWindowKeepsFooter(t *testing.T) {
	for _, h := range []int{6, 7, 8} {
		m := sampleModel(80, h)
		view := m.View().Content
		lines := strings.Split(view, "\n")

		if len(lines) > h {
			t.Errorf("высота %d: кадр занял %d строк", h, len(lines))
		}
		for _, want := range []string{"COST", "[q] quit"} {
			if !strings.Contains(view, want) {
				t.Errorf("высота %d: футер обрезан, нет %q:\n%s", h, want, view)
			}
		}
	}
}

// Ресайз панели не должен выбрасывать пользователя из середины лога вниз.
func TestResizeKeepsScrollPosition(t *testing.T) {
	events := make([]parse.Event, 60)
	for i := range events {
		events[i] = parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
			Tool: "Bash", Detail: "строка"}
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	// Прокрутка идёт клавишами: только так модель узнаёт, что пользователь
	// ушёл от хвоста и автоскролл надо погасить.
	for range 5 {
		m.Update(keyPress("up"))
	}
	offset := m.pane().vp.YOffset()

	m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	if got := m.pane().vp.YOffset(); got != offset {
		t.Errorf("после ресайза смещение %d, ожидалось %d", got, offset)
	}

	// А кто стоял внизу — внизу и остаётся.
	m.Update(keyPress("f"))
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if !m.pane().vp.AtBottom() {
		t.Errorf("стоявший внизу уехал вверх после ресайза")
	}
}

// Обрезка лога должна выглядеть так же, как обрезка шапки и футера.
func TestLogLinesTruncatedWithEllipsis(t *testing.T) {
	long := parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
		Tool: "Bash", Detail: strings.Repeat("длинная-команда ", 20)}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: []parse.Event{long}})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})

	// Смотрим именно строку лога, а не футер: многоточие там тоже бывает.
	// Лог начинается сразу за шапкой — заголовком и рядом табов.
	logLine := strings.Split(stripANSI(m.View().Content), "\n")[headerHeight]
	if !strings.HasSuffix(strings.TrimRight(logLine, " "), "…") {
		t.Errorf("строка лога обрезана без многоточия: %q", logLine)
	}
}

// Тема пишет в карты: в S8 события придут из другой горутины.
func TestThemeConcurrentUse(t *testing.T) {
	th := NewTheme()
	done := make(chan struct{})

	for i := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := range 50 {
				th.ColorFor(fmt.Sprintf("агент-%d", (i+j)%5))
			}
		}()
	}
	for range 8 {
		<-done
	}
}

// Прокрутка вверх гасит автоскролл, f и G возвращают в хвост.
func TestFollowEdgeDetection(t *testing.T) {
	m := manyEvents(t, 100, 80, 20)
	m.opts.Mode = ModeLive // индикатор paused осмыслен только у живой сессии
	if !m.pane().autoFollow {
		t.Fatalf("автоскролл выключен на старте")
	}

	m.Update(keyPress("up"))
	if m.pane().autoFollow {
		t.Errorf("прокрутка вверх не погасила автоскролл")
	}
	if !strings.Contains(stripANSI(m.View().Content), "paused") {
		t.Errorf("в шапке нет индикатора paused")
	}

	m.Update(keyPress("f"))
	if !m.pane().autoFollow || !m.pane().vp.AtBottom() {
		t.Errorf("f не вернул в хвост")
	}

	m.Update(keyPress("up"))
	m.Update(keyPress("G"))
	if !m.pane().autoFollow {
		t.Errorf("G не вернул автоскролл")
	}
}

// Новое событие при погашенном автоскролле не должно дёргать viewport.
func TestPushDoesNotJumpWhenPaused(t *testing.T) {
	m := manyEvents(t, 100, 80, 20)
	m.Update(keyPress("up"))
	offset := m.pane().vp.YOffset()

	m.Push(parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
		Tool: "Bash", Detail: "новое"})

	if got := m.pane().vp.YOffset(); got != offset {
		t.Errorf("viewport уехал: было %d, стало %d", offset, got)
	}
}

// Фильтр меняет видимое и НЕ трогает счётчики футера — прямое требование спеки.
func TestFilterAffectsOnlyLog(t *testing.T) {
	events := []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: "главный"},
		{Time: time.Now(), Source: "go-code-adapter", Kind: parse.KindTool, Tool: "Edit", Detail: "агентский"},
		{Time: time.Now(), Source: "go-linter", Kind: parse.KindTool, Tool: "Bash", Detail: "линтерский"},
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, Totals: sampleTotals(),
		Agents: []Agent{{Name: "main", Count: 1}}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	priceBefore := priceRow(m)

	m.Update(keyPress("/"))
	for _, r := range "go-code" {
		m.Update(keyPress(string(r)))
	}
	view := stripANSI(m.View().Content)

	if !strings.Contains(view, "агентский") {
		t.Errorf("совпавшая строка пропала:\n%s", view)
	}
	for _, gone := range []string{"главный", "линтерский"} {
		if strings.Contains(view, gone) {
			t.Errorf("строка %q не отфильтрована:\n%s", gone, view)
		}
	}

	// Сравниваем строку ЦЕНА из НАСТОЯЩЕГО кадра: два независимых вызова
	// Footer с одинаковыми Totals сошлись бы и при сломанной модели.
	if after := priceRow(m); after != priceBefore {
		t.Errorf("строка ЦЕНА изменилась при фильтрации:\n%q\n%q", priceBefore, after)
	}

	m.Update(keyPress("esc"))
	if !strings.Contains(stripANSI(m.View().Content), "главный") {
		t.Errorf("Esc не сбросил фильтр")
	}
}

// Кольцевой буфер вытесняет голову и держит потолок.
func TestRingBufferEviction(t *testing.T) {
	r := newRing(10)
	for i := range 25 {
		r.push(parse.Event{Source: "main", Detail: fmt.Sprintf("строка-%d", i)})
	}

	if r.len() != 10 {
		t.Fatalf("в буфере %d событий, ожидалось 10", r.len())
	}
	if r.events[0].Detail != "строка-15" {
		t.Errorf("первое событие %q, ожидалась строка-15", r.events[0].Detail)
	}
	if r.events[9].Detail != "строка-24" {
		t.Errorf("последнее событие %q", r.events[9].Detail)
	}
}

// Вытеснение головы буфера обязано сбрасывать кэши ВСЕХ панелей, не только
// той, что активна сейчас: иначе непрогретый таб потом отдаст строки уже
// удалённых событий (и потеряет последнее событие пачки — pushOne возвращает
// признак вытеснения ДО observe). Диф расширил инвариант с одного кэша
// (styled) до шести (три панели × два представления) — проверяем на два таба.
func TestEvictionInvalidatesPaneCaches(t *testing.T) {
	m := New(Options{Project: "p", Mode: ModeLive, Capacity: 20})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	for _, k := range []string{"2", "3", "1"} { // прогреть кэши всех трёх табов
		m.Update(keyPress(k))
	}

	batch := make([]parse.Event, 0, 60)
	for i := range 60 {
		batch = append(batch, parse.Event{Time: time.Now(), Source: "main",
			Kind: parse.KindTool, Tool: "Read",
			Detail: fmt.Sprintf("f%d.go", i), Path: fmt.Sprintf("/p/f%d.go", i)})
	}
	m.PushBatch(batch)

	want := m.log.len()
	for _, v := range []View{ViewTranscript, ViewFiles} {
		lines := m.panes[v].content(m.log, m.theme, m.width)
		if len(lines) != want {
			t.Errorf("таб %v: строк %d, событий в буфере %d", v, len(lines), want)
		}
		joined := stripANSI(strings.Join(lines, "\n"))
		if strings.Contains(joined, "f0.go") {
			t.Errorf("таб %v показывает вытесненное событие", v)
		}
		if !strings.Contains(joined, "f59.go") {
			t.Errorf("таб %v потерял последнее событие пачки", v)
		}
	}
}

// Опоздавшее имя субагента перерисовывает уже показанные строки.
func TestRenameRedrawsShownLines(t *testing.T) {
	events := []parse.Event{
		{Time: time.Now(), Source: "agent-9f8e7d6c", Kind: parse.KindTool, Tool: "Bash", Detail: "работа"},
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	if !strings.Contains(stripANSI(m.View().Content), "agent-9f8e7d6c") {
		t.Fatalf("исходная подпись не показана")
	}

	m.Rename("agent-9f8e7d6c", "go-code-fixer")
	view := stripANSI(m.View().Content)

	if !strings.Contains(view, "go-code-fixer") {
		t.Errorf("строка не перерисована:\n%s", view)
	}
	if strings.Contains(view, "agent-9f8e7d6c") {
		t.Errorf("осталась старая подпись:\n%s", view)
	}
}

// Перенос показывает деталь целиком и возвращается обратно.
func TestWrapMode(t *testing.T) {
	long := strings.Repeat("длинная-команда ", 12)
	m := New(Options{Project: "p", Mode: ModeArchive, Events: []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: long},
	}})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})

	plain := len(m.content())
	m.Update(keyPress("w"))
	wrapped := len(m.content())

	if wrapped <= plain {
		t.Errorf("перенос не добавил строк: было %d, стало %d", plain, wrapped)
	}
	if !strings.Contains(stripANSI(m.View().Content), "[w] wrap: on") {
		t.Errorf("режим переноса не показан в подсказках")
	}

	m.Update(keyPress("w"))
	if len(m.content()) != plain {
		t.Errorf("возврат в обрезку не сработал")
	}
}

// Перенос и фильтр работают вместе.
func TestWrapWithFilter(t *testing.T) {
	m := New(Options{Project: "p", Mode: ModeArchive, Events: []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: strings.Repeat("а", 200)},
		{Time: time.Now(), Source: "go-linter", Kind: parse.KindTool, Tool: "Bash", Detail: strings.Repeat("б", 200)},
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(keyPress("w"))

	m.Update(keyPress("/"))
	for _, r := range "linter" {
		m.Update(keyPress(string(r)))
	}

	view := stripANSI(m.View().Content)
	if strings.Contains(view, "аааа") {
		t.Errorf("фильтр не применился в режиме переноса")
	}
	if !strings.Contains(view, "бббб") {
		t.Errorf("совпавшее событие не показано:\n%s", view)
	}
}

// Между логом и футером обязана быть черта — она есть в макете спеки.
func TestSeparatorPresent(t *testing.T) {
	m := sampleModel(80, 24)
	lines := strings.Split(stripANSI(m.View().Content), "\n")

	sep := lines[len(lines)-footerHeight]
	if strings.Count(sep, "─") < 70 {
		t.Errorf("разделителя нет: %q", sep)
	}
	if len(lines) != 24 {
		t.Errorf("кадр занял %d строк вместо 24", len(lines))
	}
}

// На узкой панели колонка источника сжимается, иначе детали не видно.
func TestNarrowShowsDetail(t *testing.T) {
	m := New(Options{Project: "p", Mode: ModeArchive, Events: []parse.Event{
		{Time: time.Now(), Source: "go-code-adapter", Kind: parse.KindTool,
			Tool: "Bash", Detail: "go build ./..."},
	}})
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})

	logLine := strings.Split(stripANSI(m.View().Content), "\n")[headerHeight]
	if !strings.Contains(logLine, "go build") {
		t.Errorf("на 40 колонках деталь не видна: %q", logLine)
	}
}

// manyEvents собирает модель с заданным числом событий.
func manyEvents(t *testing.T, n, w, h int) *Model {
	t.Helper()

	events := make([]parse.Event, n)
	for i := range events {
		events[i] = parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
			Tool: "Bash", Detail: fmt.Sprintf("команда-%d", i)}
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, Totals: sampleTotals()})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// Футер обязан стоять на месте: короткий отфильтрованный лог не должен
// поднимать его вверх.
func TestFooterStaysAnchored(t *testing.T) {
	events := []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: "раз"},
		{Time: time.Now(), Source: "go-linter", Kind: parse.KindTool, Tool: "Bash", Detail: "два"},
	}
	m := New(Options{Project: "p", Mode: ModeArchive, Events: events, Totals: sampleTotals()})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	before := strings.Split(stripANSI(m.View().Content), "\n")

	m.Update(keyPress("/"))
	for _, r := range "linter" {
		m.Update(keyPress(string(r)))
	}
	after := strings.Split(stripANSI(m.View().Content), "\n")

	if len(before) != len(after) {
		t.Fatalf("высота кадра изменилась: %d против %d", len(before), len(after))
	}
	// От фильтрации меняется РОВНО одна строка футера — его собственная.
	// Прежде тут сравнивались заголовки «ЦЕНА», которых в футере нет с тех
	// пор, как вывод перевели на английский: проверка была пустой.
	for i := len(after) - footerHeight; i < len(after); i++ {
		if strings.HasPrefix(after[i], "FILTER") {
			continue
		}
		if before[i] != after[i] {
			t.Errorf("строка %d футера съехала при фильтрации:\n%q\n%q", i, before[i], after[i])
		}
	}
}

// BenchmarkPushIncremental и BenchmarkPushFullRefresh меряют цену живого
// потока: watcher при старте отдаёт тысячи строк подряд.
func BenchmarkPushIncremental(b *testing.B) {
	for b.Loop() {
		m := benchModel()
		for i := range 1000 {
			m.Push(benchEvent(i))
		}
	}
}

func BenchmarkPushBatch(b *testing.B) {
	events := make([]parse.Event, 1000)
	for i := range events {
		events[i] = benchEvent(i)
	}
	for b.Loop() {
		benchModel().PushBatch(events)
	}
}

func BenchmarkPushFullRefresh(b *testing.B) {
	for b.Loop() {
		m := benchModel()
		for i := range 1000 {
			m.log.push(benchEvent(i))
			m.refresh() // поведение до фикса: полный сброс кэша на каждое событие
		}
	}
}

// BenchmarkFilterKeystroke — цена ОДНОГО нажатия клавиши в поле фильтра на
// полном буфере. Это самая частая интерактивная работа интерфейса: пока
// набирают слово, панель пересобирается на каждую букву, и пользователь ждёт
// ровно столько, сколько стоит эта пересборка.
//
// Фильтр подобран так, что под него подходят ВСЕ события: это худший случай —
// в видимое попадает весь буфер.
func BenchmarkFilterKeystroke(b *testing.B) {
	m := benchModelSized(DefaultCapacity)
	m.Update(keyPress("/"))
	for _, r := range "go bui" {
		m.Update(keyPress(string(r)))
	}

	b.ResetTimer()
	// Одна итерация — ровно одно нажатие: буква и возврат стирающей клавишей
	// чередуются, чтобы длина фильтра не росла, а работа оставалась той же.
	for i := 0; b.Loop(); i++ {
		if i%2 == 0 {
			m.Update(keyPress("l"))
		} else {
			m.Update(keyPress("backspace"))
		}
	}
}

// benchModelSized собирает модель с готовым буфером в n событий.
func benchModelSized(n int) *Model {
	events := make([]parse.Event, n)
	for i := range events {
		events[i] = benchEvent(i)
	}
	m := New(Options{Project: "p", Mode: ModeLive, Events: events, Totals: sampleTotals()})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

func benchModel() *Model {
	m := New(Options{Project: "p", Mode: ModeArchive})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

func benchEvent(i int) parse.Event {
	return parse.Event{Time: time.Now(), Source: "go-code-adapter", Kind: parse.KindTool,
		Tool: "Bash", Detail: fmt.Sprintf("go build ./... && go test ./... # %d", i)}
}

// fakeFeed — источник живых данных под контролем теста.
type fakeFeed struct {
	batches   chan Batch
	renames   chan session.Rename
	errs      chan error
	names     map[string]string
	cancelled bool
}

func newFakeFeed() *fakeFeed {
	return &fakeFeed{
		batches: make(chan Batch, 4),
		renames: make(chan session.Rename, 4),
		errs:    make(chan error, 4),
		names:   map[string]string{},
	}
}

func (f *fakeFeed) feed() *Feed {
	return &Feed{
		Batches: f.batches,
		Renames: f.renames,
		Errs:    f.errs,
		Names:   func() map[string]string { return f.names },
		Cancel:  func() { f.cancelled = true },
	}
}

func liveModel(t *testing.T, f *fakeFeed) *Model {
	t.Helper()
	m := New(Options{Project: "p", Mode: ModeLive, Feed: f.feed()})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

// Живой батч пополняет лог и футер разом.
func TestLiveBatchUpdatesLogAndFooter(t *testing.T) {
	m := liveModel(t, newFakeFeed())

	m.Update(batchMsg(Batch{
		Events: []parse.Event{{Time: time.Now(), Source: "main", Kind: parse.KindTool,
			Tool: "Bash", Detail: "живое событие", Model: "claude-opus-5"}},
		Totals: sampleTotals(),
		Agents: []Agent{{Name: "main", Count: 1}},
	}))

	view := stripANSI(m.View().Content)
	for _, want := range []string{"живое событие", "$24.18", "opus-5"} {
		if !strings.Contains(view, want) {
			t.Errorf("в кадре нет %q:\n%s", want, view)
		}
	}
}

// Индикатор: live при follow, paused при прокрутке, archive у завершённой.
func TestLiveIndicator(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	events := make([]parse.Event, 100)
	for i := range events {
		events[i] = parse.Event{Time: time.Now(), Source: "main", Kind: parse.KindTool,
			Tool: "Bash", Detail: fmt.Sprintf("%d", i)}
	}
	m.Update(batchMsg(Batch{Events: events}))

	if got := m.indicator(); got != "live" {
		t.Errorf("индикатор %q, ожидался live", got)
	}
	m.Update(keyPress("up"))
	if got := m.indicator(); got != "paused" {
		t.Errorf("после прокрутки индикатор %q, ожидался paused", got)
	}
	m.Update(keyPress("f"))
	if got := m.indicator(); got != "live" {
		t.Errorf("после f индикатор %q, ожидался live", got)
	}

	// У архива слежение бессмысленно: он не растёт.
	a := sampleModel(80, 24)
	a.Update(keyPress("up"))
	if got := a.indicator(); got != ModeArchive {
		t.Errorf("архивный индикатор %q", got)
	}
}

// Сбой наблюдения уходит в строку статуса, интерфейс живёт дальше.
func TestTailerErrorGoesToStatus(t *testing.T) {
	m := liveModel(t, newFakeFeed())

	_, cmd := m.Update(errMsg{errors.New("файл исчез")})
	if cmd == nil {
		t.Errorf("после ошибки наблюдение не переподписалось")
	}

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "файл исчез") {
		t.Errorf("ошибка не показана:\n%s", view)
	}
	if !strings.Contains(view, "COST") {
		t.Errorf("футер развалился из-за статуса:\n%s", view)
	}
}

// Выход из интерфейса гасит наблюдение: тейлеры не должны пережить TUI.
func TestQuitCancelsFeed(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		t.Run(key, func(t *testing.T) {
			f := newFakeFeed()
			m := liveModel(t, f)

			_, cmd := m.Update(keyPress(key))
			if cmd == nil {
				t.Fatalf("%s не завершил программу", key)
			}
			if !f.cancelled {
				t.Errorf("%s не остановил наблюдение", key)
			}
		})
	}
}

// Ctrl+C из режима фильтра тоже обязан гасить наблюдение.
func TestQuitFromFilterCancelsFeed(t *testing.T) {
	f := newFakeFeed()
	m := liveModel(t, f)

	m.Update(keyPress("/"))
	m.Update(keyPress("ctrl+c"))
	if !f.cancelled {
		t.Errorf("наблюдение осталось работать после выхода из фильтра")
	}
}

// Опоздавшее имя субагента приходит сигналом и сверяется со снимком имён.
func TestLiveRenameRedraws(t *testing.T) {
	f := newFakeFeed()
	m := liveModel(t, f)

	m.Update(batchMsg(Batch{Events: []parse.Event{
		{Time: time.Now(), Source: "agent-9f8e7d6c", Kind: parse.KindTool, Tool: "Bash", Detail: "работа"},
		{Time: time.Now(), Source: "agent-1a2b3c4d", Kind: parse.KindTool, Tool: "Bash", Detail: "вторая"},
	}}))

	// Сигнал пришёл только про первого; второго чиним по снимку Names().
	f.names["agent-1a2b3c4d"] = "go-linter"
	m.Update(renameMsg(session.Rename{ID: "agent-9f8e7d6c", Name: "go-code-fixer"}))

	view := stripANSI(m.View().Content)
	for _, want := range []string{"go-code-fixer", "go-linter"} {
		if !strings.Contains(view, want) {
			t.Errorf("подпись %q не применена:\n%s", want, view)
		}
	}
	if strings.Contains(view, "agent-9f8e7d6c") || strings.Contains(view, "agent-1a2b3c4d") {
		t.Errorf("остались откатные подписи:\n%s", view)
	}
}

// Фильтр не должен трогать счётчики футера и в живом режиме.
func TestLiveFilterKeepsFooter(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(batchMsg(Batch{
		Events: []parse.Event{
			{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: "главный"},
			{Time: time.Now(), Source: "go-linter", Kind: parse.KindTool, Tool: "Bash", Detail: "линтер"},
		},
		Totals: sampleTotals(),
		Agents: []Agent{{Name: "main", Count: 1}, {Name: "go-linter", Count: 1}},
	}))

	before := priceRow(m)
	m.Update(keyPress("/"))
	for _, r := range "linter" {
		m.Update(keyPress(string(r)))
	}

	view := stripANSI(m.View().Content)
	if strings.Contains(view, "главный") {
		t.Errorf("фильтр не применился:\n%s", view)
	}
	if after := priceRow(m); after != before {
		t.Errorf("футер изменился при фильтрации живого потока")
	}
	// Набранный фильтр обязан стоять В НАЧАЛЕ строки FILTER, на своём месте.
	// Проверка позиционная, а не «есть где-то в кадре»: поля FooterInput
	// именованы, но перестановка Filter и Status в литерале компилируется
	// молча — и тогда фильтр уезжает в конец строки как сообщение о сбое,
	// оставаясь при этом видимым.
	body := strings.TrimSpace(strings.TrimPrefix(footerRow(m, "FILTER"), "FILTER"))
	if !strings.HasPrefix(body, "/linter") {
		t.Errorf("строка FILTER начинается не с набранного фильтра: %q", body)
	}
}

// Источник иссяк — интерфейс остаётся живым и показывает накопленное.
func TestFeedDoneKeepsUI(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(batchMsg(Batch{Events: []parse.Event{
		{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: "последнее"},
	}}))

	if _, cmd := m.Update(doneMsg{}); cmd != nil {
		t.Errorf("после закрытия источника интерфейс что-то ждёт")
	}
	if !strings.Contains(stripANSI(m.View().Content), "последнее") {
		t.Errorf("накопленное потерялось")
	}
}

// Каждая строка кадра обязана занимать всю ширину: иначе при сжатии панели
// справа остаются обрывки прежнего, более широкого кадра.
func TestFrameLinesFillWidth(t *testing.T) {
	m := sampleModel(120, 24)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 24}) // сжатие панели

	for i, line := range strings.Split(m.View().Content, "\n") {
		if got := lipgloss.Width(line); got != 70 {
			t.Errorf("строка %d занимает %d колонок вместо 70: %q", i, got, stripANSI(line))
		}
	}
}

// Пачка имён обязана перестраивать буфер один раз, а не по разу на имя.
func TestRenameAllIsSinglePass(t *testing.T) {
	r := newRing(100)
	for i := range 10 {
		r.push(parse.Event{Source: fmt.Sprintf("agent-%d", i)})
	}

	names := map[string]string{"agent-1": "go-linter", "agent-2": "go-fixer"}
	if !r.renameAll(names) {
		t.Fatalf("переименование не применилось")
	}
	if r.events[1].Source != "go-linter" || r.events[2].Source != "go-fixer" {
		t.Errorf("подписи не заменены: %+v", r.events[:3])
	}
	if r.renameAll(names) {
		t.Errorf("повторный проход сообщил об изменениях, хотя менять нечего")
	}
	if r.renameAll(nil) {
		t.Errorf("пустая карта не должна ничего менять")
	}
}

// Источник иссяк — шапка обязана перестать обещать живой поток.
func TestDoneStopsLiveIndicator(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	if m.indicator() != "live" {
		t.Fatalf("индикатор %q на старте", m.indicator())
	}

	m.Update(doneMsg{"renames"})
	if m.indicator() != "live" {
		t.Errorf("иссяк канал переименований, а поток объявлен мёртвым")
	}

	m.Update(doneMsg{"batches"})
	if got := m.indicator(); got != "stopped" {
		t.Errorf("индикатор %q, ожидался stopped", got)
	}
}

// Неразобранные строки не должны исчезать молча.
func TestSkippedLinesShownInFooter(t *testing.T) {
	m := liveModel(t, newFakeFeed())
	m.Update(batchMsg(Batch{
		Events:  []parse.Event{{Time: time.Now(), Source: "main", Kind: parse.KindTool, Tool: "Bash", Detail: "ок"}},
		Skipped: 7,
	}))

	if !strings.Contains(stripANSI(m.View().Content), "unparsed lines: 7") {
		t.Errorf("счётчик неразобранных строк не показан:\n%s", stripANSI(m.View().Content))
	}
}
