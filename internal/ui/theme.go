// Package ui рисует TUI: шапку, лог действий и футер с расходом.
package ui

import (
	"strings"
	"sync"

	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/parse"
)

// palette — цвета источников, унаследованы от bash-прототипа.
var palette = []string{"110", "144", "180", "151", "175"}

// Theme держит стили и раздаёт цвета источникам.
//
// Цвет назначается по порядку первого появления и больше не меняется:
// стабильность внутри сессии — всё, что требует спека, и её достаточно,
// чтобы взгляд цеплялся за нужного агента.
// Потокобезопасна: в S8 события придут из горутины watcher, и раздача
// цветов не должна краснеть под -race.
type Theme struct {
	mu      sync.Mutex
	sources map[string]lipgloss.Style
	colors  map[string]string
	order   []string

	dim      lipgloss.Style
	tool     lipgloss.Style
	text     lipgloss.Style
	delegate lipgloss.Style
	failure  lipgloss.Style
	system   lipgloss.Style
	fallback lipgloss.Style
	detail   lipgloss.Style
	label    lipgloss.Style
	accent   lipgloss.Style

	// Палитра футера кодирует ЦЕНУ токенов, а не их тип: кэш-чтение стоит
	// 0.1x от входа, а выход 5x. Если они выглядят одинаково, глаз не
	// подсказывает, где утекают деньги: 149M кэша обязаны выглядеть тише,
	// чем 652k выхода.
	tokIn    lipgloss.Style
	tokOut   lipgloss.Style
	tokRead  lipgloss.Style
	tokWrite lipgloss.Style
	price    lipgloss.Style
	models   lipgloss.Style
}

// NewTheme собирает тему со стилями по умолчанию.
func NewTheme() *Theme {
	return &Theme{
		sources:  make(map[string]lipgloss.Style),
		colors:   make(map[string]string),
		dim:      lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		tool:     lipgloss.NewStyle().Foreground(lipgloss.Color("41")),
		text:     lipgloss.NewStyle().Foreground(lipgloss.Color("44")),
		delegate: lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true),
		failure:  lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		system:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		fallback: lipgloss.NewStyle().Foreground(lipgloss.Color("176")),
		detail:   lipgloss.NewStyle(),
		label:    lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Bold(true),
		accent:   lipgloss.NewStyle().Foreground(lipgloss.Color("110")),

		tokIn:    lipgloss.NewStyle().Foreground(lipgloss.Color("252")),            // 1x — обычный
		tokOut:   lipgloss.NewStyle().Foreground(lipgloss.Color("210")).Bold(true), // 5x — яркий коралл
		tokRead:  lipgloss.NewStyle().Foreground(lipgloss.Color("240")),            // 0.1x — тусклый
		tokWrite: lipgloss.NewStyle().Foreground(lipgloss.Color("179")),            // 1.25–2x — тёплый
		price:    lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true), // итог — акцент
		models:   lipgloss.NewStyle().Foreground(lipgloss.Color("255")),
	}
}

// ColorFor отдаёт стабильный стиль источника.
func (t *Theme) ColorFor(name string) lipgloss.Style {
	t.colorName(name)

	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sources[name]
}

// colorName закрепляет за источником цвет палитры и отдаёт его код.
// Отдельный метод нужен и раскраске, и тестам: lipgloss.Style сравнивать
// нельзя — внутри срез цветов, поэтому стабильность проверяется по коду.
func (t *Theme) colorName(name string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if color, ok := t.colors[name]; ok {
		return color
	}

	color := palette[len(t.order)%len(palette)]
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	if len(t.order) >= len(palette) {
		// Палитра кончилась — различаем начертанием, а не путаницей цветов.
		st = st.Bold(true)
	}

	t.colors[name] = color
	t.sources[name] = st
	t.order = append(t.order, name)
	return color
}

// kindStyle — стиль колонки-ярлыка по роду события.
func (t *Theme) kindStyle(kind parse.Kind) lipgloss.Style {
	switch kind {
	case parse.KindDelegate:
		return t.delegate
	case parse.KindError:
		return t.failure
	case parse.KindText:
		return t.text
	case parse.KindSystem:
		return t.system
	case parse.KindFallback:
		return t.fallback
	default:
		return t.tool
	}
}

// statusStyle — цвет колонки исхода. Отказ красится не как ошибка: правило
// разрешений сработало штатно, инструмент не падал, и красный тут кричал бы
// о поломке, которой нет. Успех и ожидание тусклые: в глаза должны бросаться
// именно неудачи.
func (t *Theme) statusStyle(s parse.Status) lipgloss.Style {
	switch s {
	case parse.StatusError:
		return t.failure
	case parse.StatusDenied:
		return t.fallback
	default:
		return t.dim
	}
}

// Styled собирает раскрашенную строку лога. Зовётся при поступлении события,
// а не при отрисовке кадра: перекрашивать весь буфер на каждый кадр — это
// работа, линейная от длины сессии.
func (t *Theme) Styled(ev parse.Event) string { return t.StyledFor(ev, 0) }

// StyledFor собирает строку под ширину окна: на узкой панели колонка
// источника сжимается, иначе деталь события не видна вовсе.
// StyledWrapped собирает строку с переносом: деталь не режется, а
// продолжается на следующих строках под колонкой деталей.
func (t *Theme) StyledWrapped(ev parse.Event, width int) []string {
	ts, source, label, detail := parse.PartsFor(ev, width)

	prefix := t.dim.Render(ts) + "  " + t.ColorFor(ev.Source).Render(source) + "  " +
		t.kindStyle(ev.Kind).Render(label) + "  "
	indent := len("15:04:05") + 2 + parse.SourceWidth(width) + 2 + parse.LabelWidth() + 2

	avail := width - indent
	if avail < 20 {
		// На узкой панели отступ съедает всю строку: продолжение честнее
		// прижать к левому краю, чем показывать по девять знаков.
		indent, avail = 2, max(width-2, 10)
	}

	style := t.detail
	if ev.Kind == parse.KindDelegate {
		style = t.delegate
	}

	chunks := wrapRunes(detail, avail)
	if len(chunks) == 0 {
		return []string{strings.TrimRight(prefix, " ")}
	}

	out := []string{prefix + style.Render(chunks[0])}
	for _, chunk := range chunks[1:] {
		out = append(out, strings.Repeat(" ", indent)+style.Render(chunk))
	}
	return out
}

// wrapRunes делит текст на куски по n рун, не разрывая руну.
func wrapRunes(s string, n int) []string {
	if s == "" {
		return nil
	}
	if n < 1 {
		n = 1
	}

	r := []rune(s)
	out := make([]string, 0, len(r)/n+1)
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
}

func (t *Theme) StyledFor(ev parse.Event, width int) string {
	ts, source, label, detail := parse.PartsFor(ev, width)

	line := t.dim.Render(ts) + "  " +
		t.ColorFor(ev.Source).Render(source) + "  " +
		t.kindStyle(ev.Kind).Render(label)

	if detail != "" {
		style := t.detail
		if ev.Kind == parse.KindDelegate {
			style = t.delegate
		}
		line += "  " + style.Render(detail)
	}
	return line
}
