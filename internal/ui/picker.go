package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/session"
)

// HumanTime — время сессии так, как его читает человек: у сегодняшней важны
// часы и минуты, у позавчерашней — дата. Год не показывается никогда: место
// он занимает, а различает транскрипты редко.
func HumanTime(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}

	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	switch days := int(today.Sub(day).Hours() / 24); {
	case days <= 0:
		return t.Format("15:04")
	case days == 1:
		return "yesterday " + t.Format("15:04")
	default:
		return t.Format("02.01")
	}
}

// HumanSize — размер транскрипта в человеческом виде. Он косвенно говорит о
// длине сессии, а длина — единственный признак «это была большая работа»,
// когда заголовка в файле нет.
func HumanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// noTitle — то, что показывается вместо заголовка, которого в транскрипте
// нет. Подставлять сюда обрывок реплики нельзя: строка заголовка обязана
// означать заголовок, иначе список врёт.
const noTitle = "— untitled —"

// timeCol — ширина колонки времени. Самое длинное значение — "yesterday 00:00"
// (15 рун), плюс пробел до следующей колонки. Занизить нельзя: маркер
// свежести слипнется со временем, и строка станет нечитаемой.
const timeCol = 16

// Picker — выбор сессии из списка. Экспортируется ради тестов раскладки:
// проверять отрисовку через живую программу bubbletea нечем.
type Picker struct {
	groups  []session.Group
	entries []session.Entry // плоский список выбираемого, в порядке показа
	heads   map[int]string  // индекс записи → шапка проекта перед ней

	theme  *Theme
	now    time.Time
	cursor int
	top    int // первая видимая запись: окно ползёт за курсором

	width, height int
	ready         bool
	chosen        bool
}

// NewPicker собирает модель выбора. now передаётся снаружи, чтобы «сегодня»
// в тестах не зависело от того, когда их запустили.
func NewPicker(groups []session.Group, now time.Time) *Picker {
	p := &Picker{groups: groups, theme: NewTheme(), now: now, heads: map[int]string{}}
	for _, g := range groups {
		if len(g.Entries) == 0 {
			continue
		}
		p.heads[len(p.entries)] = g.Project
		p.entries = append(p.entries, g.Entries...)
	}
	return p
}

// Chosen — выбранная сессия. ok == false, если пользователь вышел без выбора.
func (p *Picker) Chosen() (session.Entry, bool) {
	if !p.chosen || p.cursor >= len(p.entries) {
		return session.Entry{}, false
	}
	return p.entries[p.cursor], true
}

func (p *Picker) Init() tea.Cmd { return nil }

func (p *Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
		p.ready = true

	case tea.KeyPressMsg:
		return p, p.onKey(msg)
	}
	return p, nil
}

func (p *Picker) onKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return tea.Quit
	case "enter":
		p.chosen = true
		return tea.Quit
	case "up", "k":
		p.move(-1)
	case "down", "j":
		p.move(1)
	case "pgup":
		p.move(-max(p.visible(), 1))
	case "pgdown":
		p.move(max(p.visible(), 1))
	case "home", "g":
		p.move(-len(p.entries))
	case "end", "G":
		p.move(len(p.entries))
	}
	return nil
}

func (p *Picker) move(delta int) {
	if len(p.entries) == 0 {
		return
	}
	p.cursor = min(max(p.cursor+delta, 0), len(p.entries)-1)

	// Окно ползёт за курсором ровно настолько, чтобы он остался виден:
	// прыжок к середине сбивает ощущение места в списке.
	if h := p.visible(); h > 0 {
		p.top = min(p.top, p.cursor)
		p.top = max(p.top, p.cursor-h+1)
		p.top = max(min(p.top, len(p.entries)-h), 0)
	}
}

// visible — сколько записей помещается в окно. Запись занимает две строки,
// шапки проектов забирают ещё по две; считаем по худшему случаю, иначе
// последняя строка списка налезет на подсказку.
func (p *Picker) visible() int {
	rows := p.height - 3 // заголовок списка, пустая строка, подсказка клавиш
	if rows < 2 {
		return 1
	}
	return max(rows/3, 1)
}

func (p *Picker) View() tea.View {
	if !p.ready {
		return altView("ccon — waiting for window size…")
	}
	if len(p.entries) == 0 {
		return altView(p.theme.dim.Render("no sessions found"))
	}

	var b strings.Builder
	b.WriteString(p.theme.accent.Render("ccon ─ pick a session"))
	b.WriteString("\n")

	h := p.visible()
	end := min(p.top+h, len(p.entries))
	for i := p.top; i < end; i++ {
		if head, ok := p.heads[i]; ok {
			b.WriteString("\n" + p.theme.label.Render(head) + "\n")
		}
		b.WriteString(p.line(i) + "\n")
		b.WriteString(p.sub(i) + "\n")
	}

	b.WriteString(p.theme.dim.Render(
		"[↑↓] move  [Enter] open  [Esc] quit  " +
			fmt.Sprintf("%d/%d", p.cursor+1, len(p.entries))))

	return altView(clipHeight(b.String(), p.height))
}

// line — основная строка записи: время, признак свежести и заголовок.
func (p *Picker) line(i int) string {
	e := p.entries[i]

	mark := "  "
	if i == p.cursor {
		mark = "▸ "
	}

	when := HumanTime(e.Modified, p.now)
	when += strings.Repeat(" ", max(timeCol-len([]rune(when)), 0))

	fresh := p.theme.dim.Render("○ ")
	if e.Newest {
		fresh = p.theme.delegate.Render("● ")
	}

	title := e.Title
	style := p.theme.models
	if title == "" {
		title, style = noTitle, p.theme.dim
	}
	if i == p.cursor {
		style = p.theme.tokOut
	}

	left := mark + p.theme.dim.Render(when) + fresh
	budget := max(p.width-lipgloss.Width(left)-1, 0)
	return clip(left+style.Render(clip(title, budget)), p.width)
}

// sub — вторая строка записи: последняя реплика. Она отвечает на вопрос
// «чем сессия занята сейчас», на который заголовок, снятый в начале, не
// отвечает вовсе.
func (p *Picker) sub(i int) string {
	e := p.entries[i]

	text := e.Prompt
	if text == "" {
		text = HumanSize(e.Size) + " · " + e.SessionID
	}

	indent := strings.Repeat(" ", timeCol+2)
	budget := max(p.width-len([]rune(indent))-2, 0)
	return clip(indent+p.theme.dim.Render("└ "+clip(text, budget)), p.width)
}

// Pick показывает список и возвращает выбранную сессию.
// ok == false означает, что пользователь вышел, ничего не выбрав.
func Pick(groups []session.Group, now time.Time) (session.Entry, bool, error) {
	p := NewPicker(groups, now)
	if len(p.entries) == 0 {
		return session.Entry{}, false, nil
	}

	res, err := tea.NewProgram(p).Run()
	if err != nil {
		return session.Entry{}, false, err
	}
	if fin, ok := res.(*Picker); ok {
		e, chosen := fin.Chosen()
		return e, chosen, nil
	}
	return session.Entry{}, false, nil
}
