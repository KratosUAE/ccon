package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/cost"
)

const (
	// headerHeight и footerHeight — фиксированные зоны раскладки; остальное
	// достаётся логу. В шапке две строки: заголовок и ряд табов.
	headerHeight = 2
	// footerHeight включает разделительную черту: она принадлежит футеру,
	// поэтому в тесноте исчезает первой, а не вместо строки ЦЕНА.
	// Подсказки клавиш стоят отдельной строкой: вместе со сводкой таба и
	// сообщением о потерянных строках они в одну строку не укладываются и
	// первой из кадра выпадала бы подсказка выхода.
	footerHeight = 10

	// labelColumn — ширина колонки заголовков футера.
	labelColumn = 10
)

// Agent — сколько событий пришло от источника.
type Agent struct {
	Name  string
	Count int
}

// Header — верхняя строка: проект, модель, эффорт и индикатор режима справа.
func Header(project, model, effort, mode string, th *Theme, width int) string {
	left := "claude_con ─ " + project
	if model != "" {
		left += " ─ " + shortModel(model)
	}
	if effort != "" {
		left += " ─ effort:" + effort
	}

	right := "● " + mode

	// Узкое окно: индикатор режима важнее хвоста заголовка, поэтому режем
	// левую часть. Бюджет учитывает два пробела и минимум одну черту.
	budget := max(width-lipgloss.Width(right)-3, 0)
	if lipgloss.Width(left) > budget {
		left = clip(left, budget)
	}
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right)-2, 1)

	line := th.accent.Render(left) + " " + th.dim.Render(strings.Repeat("─", gap)) + " " +
		th.label.Render(right)
	return padLine(clip(line, width), width)
}

// Tabs — ряд табов под заголовком: активный ярче прочих. Номер стоит рядом с
// именем, потому что переключают их именно цифрами.
func Tabs(active View, th *Theme, width int) string {
	parts := make([]string, 0, viewCount)
	for i := range viewCount {
		text := fmt.Sprintf("[%d]%s", i+1, View(i))
		style := th.dim
		if View(i) == active {
			style = th.label
		}
		parts = append(parts, style.Render(text))
	}
	return padLine(clip(strings.Join(parts, "  "), width), width)
}

// FooterInput — данные тела футера, именованными полями вместо позиционных
// аргументов: у Footer их было уже семь, из них две строки — перестановка
// при копипасте компилировалась бы молча. Th и width остаются
// отдельными параметрами Footer — тот же приём, что у Header и Tabs.
type FooterInput struct {
	Totals cost.Totals
	Agents []Agent
	// Summary — сводка активного таба: счётчики считаются по всему буферу, а
	// не по видимому, и у каждого таба они свои.
	Summary string
	Filter  string
	Wrap    bool
	// ErrOnly — включён ли тумблер «только неуспешные». Состояние показа
	// обязано быть видно в подсказках: пустой лог с включённым тумблером
	// иначе неотличим от сессии без событий.
	ErrOnly bool
	// ShowSystem — видны ли системные записи. Та же причина: спрятанная
	// седьмая часть строк обязана быть объяснена в кадре.
	ShowSystem bool
	Status     string
}

// Footer — нижняя зона: модели, токены, цена, агенты и подсказки клавиш.
// Токены разнесены по строкам, чтобы цвет каждой говорил о её цене.
func Footer(in FooterInput, th *Theme, width int) string {
	lines := []string{
		th.dim.Render(strings.Repeat("─", max(width, 0))),
		row(th, "MODELS", th.models.Render(modelsLine(in.Totals))),
		row(th, "TOKENS", th.tokIn.Render("in "+humanNumber(in.Totals.Input))),
		row(th, "", th.tokOut.Render("out "+humanNumber(in.Totals.Output))),
		row(th, "", th.tokRead.Render("cache read "+humanNumber(in.Totals.CacheRead))),
		row(th, "", th.tokWrite.Render(fmt.Sprintf("write %s (5m %s · 1h %s)",
			humanNumber(in.Totals.CacheCreate()), humanNumber(in.Totals.Cache5m), humanNumber(in.Totals.Cache1h)))),
		// Оговорка обязательна дословно: подписка Max, списания нет.
		row(th, "COST", th.price.Render(fmt.Sprintf("$%.2f", in.Totals.CostUSD))+
			th.dim.Render(PriceNote(in.Totals))),
		row(th, "AGENTS", agentsLine(in.Agents, th)),
		row(th, "FILTER", filterLine(in, th)),
		keysLine(in),
	}

	// Строки добиваются до ширины здесь: при сжатии панели иначе остаются
	// обрывки прежнего, более широкого кадра.
	for i, line := range lines {
		lines[i] = padLine(clip(line, width), width)
	}
	return strings.Join(lines, "\n")
}

func row(th *Theme, label, body string) string {
	pad := labelColumn - len([]rune(label))
	if pad < 1 {
		pad = 1
	}
	if label == "" {
		return strings.Repeat(" ", labelColumn) + body
	}
	return th.label.Render(label) + strings.Repeat(" ", pad) + body
}

func modelsLine(t cost.Totals) string {
	if len(t.Models) == 0 {
		return "(none)"
	}

	parts := make([]string, 0, len(t.Models))
	for _, m := range t.Models {
		parts = append(parts, fmt.Sprintf("%s ×%d", m.Model, m.Count))
	}
	line := strings.Join(parts, "   ")
	if t.Unknown {
		line += "   (unknown model — opus rate)"
	}
	return line
}

// agentsLine красит имена теми же цветами, что и лог: взгляд должен связывать
// строку лога с её агентом без чтения.
func agentsLine(agents []Agent, th *Theme) string {
	if len(agents) == 0 {
		return "(none)"
	}

	parts := make([]string, 0, len(agents))
	for _, a := range agents {
		parts = append(parts, th.ColorFor(a.Name).Render(fmt.Sprintf("%s %d", a.Name, a.Count)))
	}
	return strings.Join(parts, " · ")
}

// filterLine — фильтр, состояние наблюдения и сводка активного таба.
//
// Порядок — по цене потери: значение фильтра объясняет, почему лог короткий;
// сообщение о потерянных строках и сбое наблюдения нельзя срезать краем окна
// вовсе; сводка же полезна, но переживёт обрезку — потому она и последняя.
func filterLine(in FooterInput, th *Theme) string {
	shown := "(none)"
	if in.Filter != "" {
		shown = in.Filter
	}
	line := shown
	if in.Status != "" {
		line += "   " + th.failure.Render("⚠ "+in.Status)
	}
	if in.Summary != "" {
		line += "   " + th.dim.Render(in.Summary)
	}
	return line
}

// keysLine — подсказки клавиш во всю ширину, без колонки заголовка: с ней они
// не умещаются в 80 колонок. Тумблеры названы коротко (err, sys) по той же
// причине, а их состояние показано: пустой лог с включённым тумблером иначе
// неотличим от сессии без событий.
func keysLine(in FooterInput) string {
	return "[/] filter  [f] follow  [w] wrap: " + onOff(in.Wrap) +
		"  [e] err: " + onOff(in.ErrOnly) +
		"  [s] sys: " + onOff(in.ShowSystem) + "  [q] quit"
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// PriceNote — пояснение к сумме. Сумма идёт первой, доля кэша следом:
// на узкой панели обрезка съест долю, а не деньги. Оговорка про подписку —
// требование спеки, она остаётся дословно.
func PriceNote(t cost.Totals) string {
	note := " at API rates"
	if share := t.CacheShare(); share > 0 {
		note += fmt.Sprintf(" (cache %d%%)", share)
	}
	return note + " · Max subscription, not actually billed"
}

// shortModel убирает приставку вендора: в шапке важна версия, а не бренд.
func shortModel(model string) string {
	return strings.TrimPrefix(model, "claude-")
}

// humanNumber сокращает счётчики: в футере важен порядок, а не единицы.
func humanNumber(n int64) string {
	if n <= 0 {
		return "0"
	}

	units := []struct {
		limit int64
		suf   string
	}{
		{1_000_000_000, "G"},
		{1_000_000, "M"},
		{1_000, "k"},
	}
	for _, u := range units {
		if n >= u.limit {
			v := float64(n) / float64(u.limit)
			if v >= 100 {
				return fmt.Sprintf("%.0f%s", v, u.suf)
			}
			return fmt.Sprintf("%.1f%s", v, u.suf)
		}
	}
	return fmt.Sprintf("%d", n)
}

// padLine добивает строку пробелами до ширины.
func padLine(s string, width int) string {
	if gap := width - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// clip укорачивает строку до ширины с учётом уже наложенных стилей.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansiTruncate(s, width)
}

// ansiTruncate режет строку по видимым колонкам, не разрывая
// escape-последовательности. Различаются два вида: CSI (\x1b[…буква) и OSC
// (\x1b]…, завершается BEL или ST) — у второго буква концом не является,
// и наивная проверка порвала бы гиперссылку посреди.
func ansiTruncate(s string, width int) string {
	var b strings.Builder
	shown := 0
	const (
		plain = iota
		escStart
		csi
		osc
	)
	state := plain

	for _, r := range s {
		switch {
		case state == plain && r == 0x1b:
			state = escStart
			b.WriteRune(r)
		case state == escStart:
			b.WriteRune(r)
			switch r {
			case ']':
				state = osc
			case '[':
				state = csi
			default:
				state = plain
			}
		case state == csi:
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				state = plain
			}
		case state == osc:
			b.WriteRune(r)
			if r == 0x07 || r == 0x5c {
				state = plain
			}
		default:
			w := lipgloss.Width(string(r))
			if shown+w > width-1 {
				b.WriteString("…")
				b.WriteString("\x1b[0m")
				return b.String()
			}
			shown += w
			b.WriteRune(r)
		}
	}
	return b.String()
}
