package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	tea "charm.land/bubbletea/v2"

	"github.com/KratosUAE/ccon/internal/cost"
	"github.com/KratosUAE/ccon/internal/parse"
	"github.com/KratosUAE/ccon/internal/session"
)

// ModeArchive — индикатор режима для завершённого транскрипта.
const ModeArchive = "archive"

// Options — всё, что нужно показать. Слайс S6 работает по завершённой
// сессии: события уже прочитаны, расход уже посчитан.
type Options struct {
	Project string
	Model   string
	Effort  string
	Mode    string
	Events  []parse.Event
	// Results — результаты вызовов той же сессии. Порядок значения не имеет:
	// модель сшивает их после того, как поставила на учёт ВСЕ события.
	Results []parse.Result
	Totals  cost.Totals
	Agents  []Agent
	// Skipped — сколько строк транскрипта потеряно: не разобрано или
	// отброшено по длине. Архивный режим обязан говорить о них так же,
	// как живой, иначе пустой лог выглядит как отсутствие событий.
	Skipped int
	// Capacity — размер кольцевого буфера; 0 означает DefaultCapacity.
	Capacity int
	// Feed — живой источник событий; nil для архивного режима.
	Feed *Feed
	// View — с какого таба открыться: значение флага --view.
	View View
}

// Model — состояние TUI: шапка, табы, лог и футер.
type Model struct {
	opts  Options
	theme *Theme
	log   *ring

	// panes — по панели на таб над одним буфером событий; active — показанная.
	// Панель держит своё место чтения, свой фильтр и свой режим переноса:
	// поле ввода фильтра написано вручную намеренно (bubbles/textinput тянет
	// github.com/atotto/clipboard, а нужны от него две операции — добавить
	// руну и стереть руну).
	panes  [viewCount]*pane
	active View

	// linker сшивает вызовы с результатами. Он один на сессию и живёт именно
	// здесь: архивный и живой пути отдают модели одинаковый корм, и механизм
	// сшивки остаётся один, а не по одному на путь.
	linker *parse.Linker

	// feed — живой источник; nil для завершённой сессии.
	feed     *Feed
	status   string // последняя ошибка наблюдения, показывается в футере
	skipped  int    // неразобранных строк транскрипта
	progress string // ход догона накопленного
	stopped  bool   // источник иссяк: живого потока больше не будет

	ready bool // пришёл ли хоть один WindowSizeMsg
	width int
	// styleWidth — ширина, под которую собраны строки панелей.
	styleWidth int
	height     int
}

// New собирает модель. Строки панелей строятся лениво: непосещённый таб не
// стоит ничего, а показанный собирается один раз и дальше дописывается.
func New(o Options) *Model {
	if o.Mode == "" {
		o.Mode = ModeArchive
	}
	// Значение таба приходит от вызывающего (флаг --view). Оно проверено там,
	// но за пределами диапазона всё равно не должно ронять первый же кадр
	// обращением к несуществующей панели.
	if o.View < 0 || int(o.View) >= viewCount {
		o.View = ViewTranscript
	}

	m := &Model{
		opts:  o,
		theme: NewTheme(),
		log:   newRing(o.Capacity),
		panes: [viewCount]*pane{
			ViewTranscript: newPane(transcriptView{}),
			ViewMCP:        newPane(mcpView{}),
			ViewFiles:      newPane(filesView{}),
		},
		active:  o.View,
		linker:  parse.NewLinker(),
		feed:    o.Feed,
		skipped: o.Skipped,
	}
	// Порядок обязателен: сначала на учёт встают ВСЕ вызовы пачки и только
	// потом сшиваются её результаты. Так сшивка перестаёт зависеть от порядка
	// внутри пачки — а он у архивного пути свой (пересортировка по времени) и
	// у живого свой (фаза догона).
	for _, ev := range o.Events {
		m.pushOne(ev)
	}
	m.applyResults(o.Results)
	m.applyContent()
	return m
}

// pane — активная панель. Все клавиши показа адресуются ей одной.
func (m *Model) pane() *pane { return m.panes[m.active] }

// Push добавляет событие на ходу. Живой режим — слайс S8, но кольцевой буфер
// и перерисовка обязаны быть готовы уже сейчас.
func (m *Model) Push(ev parse.Event) { m.PushBatch([]parse.Event{ev}) }

// PushBatch добавляет пачку событий и обновляет viewport один раз.
// Обновление viewport стоит дороже самой вставки, поэтому всплеск из тысячи
// строк при старте живой сессии обязан идти пачкой, а не по одной.
func (m *Model) PushBatch(events []parse.Event) {
	evicted := false
	for _, ev := range events {
		if m.pushOne(ev) {
			evicted = true
		}
	}

	if evicted {
		m.refresh()
		return
	}
	m.applyContent()
}

// pushOne кладёт событие в буфер и предъявляет его каждой панели: та сама
// решает, берёт ли она такие события и дописывать ли строку в свои кэши.
func (m *Model) pushOne(ev parse.Event) bool {
	// Вызов встаёт на учёт до всякого показа: результат к нему может приехать
	// уже следующей строкой того же батча.
	m.linker.Track(ev)

	seq, evicted := m.log.push(ev)
	if evicted {
		// Голова вытеснена — строки панелей больше не совпадают с буфером.
		return true
	}

	for _, p := range m.panes {
		p.observe(ev, seq, m.theme, m.width)
	}
	return false
}

// applyResults дописывает исходы вызовов в уже показанные строки.
//
// Перерисовывается ОДНА строка на результат (pane.update), а видимое
// пересобирается один раз на всю пачку: при старте живой сессии результаты
// приезжают сотнями, и полная пересборка на каждый стоила бы десятков
// миллисекунд на ровном месте.
func (m *Model) applyResults(rs []parse.Result) {
	if len(rs) == 0 {
		return
	}

	var dirty [viewCount]bool
	touched := false
	for _, r := range rs {
		u, known := m.linker.Resolve(r)
		if !known {
			// Сирота или дубль уже закрытого вызова — штатный исход.
			continue
		}
		ev, seq, found := m.log.applyUpdate(u)
		if !found {
			// Строка вызова вытеснена из буфера: дописывать нечего.
			continue
		}
		for i, p := range m.panes {
			if !p.v.Pick(ev) {
				continue
			}
			// Панель сама решает, изменился ли у неё показ: строку она
			// правит на месте, а видимое просит пересобрать, только если
			// исход влияет на её РЕНДЕР либо на её ОТБОР.
			if p.update(seq, ev, m.theme, m.width) {
				dirty[i], touched = true, true
			}
		}
	}
	if !touched {
		return
	}

	for i, p := range m.panes {
		if dirty[i] {
			p.refilter()
		}
	}
	m.applyContent()
}

// Rename меняет подпись субагента, приехавшую с опозданием, и перерисовывает
// уже показанные строки: watcher отдаёт такие сигналы через Renames().
func (m *Model) Rename(from, to string) {
	if !m.log.rename(from, to) {
		return
	}
	m.refresh()
}

// Init подписывается на живой источник; в архиве ждать нечего.
func (m *Model) Init() tea.Cmd { return m.listen() }

// Update обрабатывает ресайз панели и клавиши выхода.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.resize()

	case tea.KeyPressMsg:
		return m, m.onKey(msg)

	case batchMsg:
		m.applyBatch(Batch(msg))
		return m, m.waitBatch()

	case renameMsg:
		m.applyRename(session.Rename(msg))
		return m, m.waitRename()

	case errMsg:
		// Сбой наблюдения — строка статуса, а не смерть интерфейса.
		m.status = msg.err.Error()
		return m, m.waitErr()

	case doneMsg:
		// Источник иссяк: обещать живой поток больше нельзя, иначе
		// пользователь будет ждать событий, которых не будет.
		if msg.source == "batches" {
			m.stopped = true
		}
		return m, nil
	}
	return m, nil
}

// onKey разводит клавиши: во время ввода фильтра они принадлежат полю ввода.
// Всё, кроме выхода и смены таба, адресуется активной панели.
func (m *Model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.pane()

	if p.filtering {
		switch msg.String() {
		case "ctrl+c":
			// Рефлекс, который обязан работать всегда, в том числе из ввода.
			m.stop()
			return tea.Quit
		case "esc":
			p.filtering, p.filter, p.input = false, "", ""
			m.refreshPane()
		case "enter":
			p.filtering = false
		case "backspace":
			if r := []rune(p.input); len(r) > 0 {
				p.input = string(r[:len(r)-1])
				p.filter = p.input
				m.refreshPane()
			}
		default:
			if text := msg.Text; text != "" {
				p.input += text
				p.filter = p.input
				m.refreshPane()
			}
		}
		return nil
	}

	switch key := msg.String(); key {
	case "q", "ctrl+c":
		m.stop()
		return tea.Quit

	case "1", "2", "3":
		m.switchTo(View(key[0] - '1'))
		return nil

	case "tab":
		m.switchTo((m.active + 1) % viewCount)
		return nil

	case "/":
		p.filtering = true
		return nil

	case "esc":
		p.filter, p.input = "", ""
		m.refreshPane()
		return nil

	case "w":
		p.wrap = !p.wrap
		m.applyContent()
		return nil

	case "e":
		// Показ теперь зависит от исхода, а не только от события: видимое
		// приходится отобрать заново, но сами строки остаются готовыми.
		p.errOnly = !p.errOnly
		m.refreshPane()
		return nil

	case "s":
		// Системные записи — 14.9 % строк живого корпуса, в основном
		// turn_duration. Прятать их фильтром нельзя: фильтр оставляет
		// совпавшее, а тут нужно ровно обратное.
		p.hideSystem = !p.hideSystem
		m.refreshPane()
		return nil

	case "f", "G":
		// Возврат в хвост: снова следим за концом лога.
		p.autoFollow = true
		p.vp.GotoBottom()
		return nil
	}

	if !isScrollKey(msg.String()) {
		return nil
	}

	var cmd tea.Cmd
	p.vp, cmd = p.vp.Update(msg)
	// Edge-detection: ушёл от хвоста — автоскролл гаснет; докрутил обратно
	// вниз — оживает сам, как в less +F. f и G делают то же одним нажатием.
	p.autoFollow = p.vp.AtBottom()
	return cmd
}

// switchTo меняет активный таб. Позиция прокрутки и фильтр остаются у каждой
// панели своими: возврат обратно не должен терять место чтения.
func (m *Model) switchTo(v View) {
	if v == m.active || v < 0 || int(v) >= viewCount {
		return
	}
	m.active = v
	m.applyContent()
}

// isScrollKey — клавиши, которые двигают лог.
func isScrollKey(key string) bool {
	switch key {
	case "up", "down", "pgup", "pgdown", "home", "end", "k", "j":
		return true
	}
	return false
}

// refresh пересобирает ВСЕ панели со строками включительно: изменилось то, из
// чего строки собраны — состав буфера (вытеснение головы) или подпись
// источника.
func (m *Model) refresh() {
	for _, p := range m.panes {
		p.restyle()
	}
	m.applyContent()
}

// refreshPane пересобирает видимое активной панели, оставляя её строки:
// фильтр и тумблеры меняют отбор, а не сами строки. Прочих табов это не
// касается — фильтр у каждого свой.
func (m *Model) refreshPane() {
	m.pane().refilter()
	m.applyContent()
}

// applyContent кладёт в viewport представление активной панели, строя его при
// нужде. Прочие панели получат своё при переключении на них.
func (m *Model) applyContent() {
	m.pane().apply(m.log, m.theme, m.width)
}

// content отдаёт строки активной панели. Нужен тестам: в кадре они уже лежат
// внутри viewport, и вытащить их оттуда для проверки нечем.
func (m *Model) content() []string {
	return m.pane().content(m.log, m.theme, m.width)
}

// resize пересобирает раскладку от размеров окна: высота лога — всё, что
// осталось от шапки и футера. Приём канонический (bubbletea/examples/pager).
//
// Позиция прокрутки сохраняется: ресайз панели tmux не должен выбрасывать
// читающего из середины лога вниз. Кто стоял внизу — внизу и остаётся.
func (m *Model) resize() {
	p := m.pane()
	atBottom := p.vp.AtBottom()
	offset := p.vp.YOffset()

	for _, q := range m.panes {
		q.vp.SetWidth(m.width)
		q.vp.SetHeight(m.viewportHeight())
	}
	// От ширины зависят и колонка источника транскрипта, и колонки табов mcp
	// и files, и обрезка по краю окна — то есть строки всех панелей. Но
	// перетаскивание границы панели tmux даёт поток сообщений о размере, а
	// высота на строки не влияет: пересобираем только при смене ширины.
	if m.styleWidth != m.width {
		for _, q := range m.panes {
			q.restyle()
		}
	}
	m.styleWidth = m.width
	m.applyContent()

	if atBottom || p.autoFollow {
		p.vp.GotoBottom()
	} else {
		p.vp.SetYOffset(offset)
	}
}

// viewportHeight — сколько строк достаётся логу. В тесноте лог схлопывается
// первым: расход и подсказки клавиш ценнее, чем одна строка действий.
func (m *Model) viewportHeight() int {
	return max(m.height-headerHeight-footerHeight, 1)
}

// View собирает кадр. Alt-screen в v2 включается полем View, а не опцией
// NewProgram: иначе TUI затопчет вывод терминала.
func (m *Model) View() tea.View {
	if !m.ready {
		// Размер окна ещё не пришёл — рисовать нечего, но и падать нельзя.
		return altView("claude_con — waiting for window size…")
	}

	p := m.pane()
	footer := Footer(FooterInput{
		Totals:  m.opts.Totals,
		Agents:  m.opts.Agents,
		Summary: p.summary(),
		Filter:  p.filterLabel(),
		Wrap:    p.wrap,
		ErrOnly: p.errOnly,
		// Тумблер называется по видимому состоянию, а не по имени поля:
		// «sys: on» читается как «системные записи показаны».
		ShowSystem: !p.hideSystem,
		Status:     m.statusLine(),
	}, m.theme, m.width)

	// В тесноте футер важнее лога, но и сам он может не влезть: сверху у него
	// модели и токены, снизу — цена, агенты и подсказки клавиш. Режем сверху.
	if m.height < footerHeight {
		footer = lastLines(footer, m.height)
	}

	// В тесную панель первым не влезает лог, а не футер: цена, агенты и
	// подсказки клавиш нужнее одной строки действий. Обрезка сверху вниз
	// выбросила бы именно их.
	if m.height <= footerHeight {
		return altView(footer)
	}

	header := Header(m.opts.Project, m.opts.Model, m.opts.Effort, m.indicator(), m.theme, m.width) +
		"\n" + Tabs(m.active, m.theme, m.width)
	if m.height <= headerHeight+footerHeight {
		// Логу места не осталось. Режем сверху: подсказки клавиш и цена
		// нужнее строки табов, а она нужнее заголовка.
		return altView(lastLines(header+"\n"+footer, m.height))
	}

	// Лог добивается до своей высоты: иначе короткий отфильтрованный список
	// поднимает футер вверх, и раскладка прыгает при каждом вводе символа.
	// Добивка по ширине сделана при сборке контента, здесь остаётся склейка.
	frame := header + "\n" + padLines(p.vp.View(), m.viewportHeight(), m.width) + "\n" + footer
	return altView(clipHeight(frame, m.height))
}

// indicator — что показать в шапке справа. Погашенный автоскролл виден
// сразу: иначе непонятно, почему лог не едет.
func (m *Model) indicator() string {
	if m.stopped {
		return "stopped"
	}
	if m.opts.Mode == "" || m.opts.Mode == ModeArchive {
		// У завершённого транскрипта следить не за чем: он не растёт, и
		// «paused» вводило бы в заблуждение.
		return ModeArchive
	}
	if !m.pane().autoFollow {
		return "paused"
	}
	return m.opts.Mode
}

// statusLine — что показать рядом с подсказками: сбой наблюдения и счётчик
// неразобранных строк.
func (m *Model) statusLine() string {
	parts := make([]string, 0, 3)
	if m.progress != "" {
		parts = append(parts, m.progress)
	}
	if m.status != "" {
		parts = append(parts, m.status)
	}
	if m.skipped > 0 {
		parts = append(parts, fmt.Sprintf("unparsed lines: %d", m.skipped))
	}
	return strings.Join(parts, " · ")
}

// padWidth добивает каждую строку кадра пробелами до полной ширины.
// Без этого при сжатии панели в ячейках справа остаются обрывки прежнего,
// более широкого кадра: терминал перерисовывает только то, что ему прислали.
func padTo(line string, width int) string {
	if gap := width - lipgloss.Width(line); gap > 0 {
		return line + strings.Repeat(" ", gap)
	}
	return line
}

// padLines добивает блок пустыми строками до нужной высоты.
func padLines(s string, height, width int) string {
	lines := strings.Split(s, "\n")
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", max(width, 0)))
	}
	return strings.Join(lines[:height], "\n")
}

// lastLines оставляет последние n строк текста.
func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if n <= 0 || len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func altView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// clipHeight не даёт кадру вылезти за высоту окна: лишние строки прокручивают
// терминал и оставляют артефакты после ресайза.
func clipHeight(frame string, height int) string {
	if height <= 0 {
		return frame
	}
	lines := strings.Split(frame, "\n")
	if len(lines) <= height {
		return frame
	}
	return strings.Join(lines[:height], "\n")
}

// Run показывает TUI и возвращает управление после выхода пользователя.
func Run(o Options) error {
	_, err := tea.NewProgram(New(o)).Run()
	return err
}
