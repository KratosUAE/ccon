package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/parse"
)

// View — какой таб показан. Порядок фиксирован и совпадает с клавишами 1/2/3.
type View int

// Табы над одним потоком событий.
const (
	ViewTranscript View = iota
	ViewMCP
	ViewFiles
)

// viewCount — сколько всего табов; по этому же кругу листает tab.
const viewCount = 3

// viewNames — единственное место, где виды названы текстом. Флаг --view,
// подписи табов и сообщение об ошибке вызова обязаны меняться вместе:
// разъехавшись, они дадут флаг, открывающий не тот таб.
var viewNames = [viewCount]string{"transcript", "mcp", "files"}

// String — имя вида, оно же подпись таба.
func (v View) String() string {
	if v < 0 || int(v) >= len(viewNames) {
		return "?"
	}
	return viewNames[v]
}

// ParseView разбирает значение флага --view. Неизвестное имя — ошибка вызова,
// а не повод молча открыть транскрипт.
func ParseView(s string) (View, bool) {
	for i, name := range viewNames {
		if s == name {
			return View(i), true
		}
	}
	return ViewTranscript, false
}

// ViewNames — перечень имён видов для сообщений об ошибке вызова.
func ViewNames() []string { return viewNames[:] }

// view — как таб отбирает события и как рисует свою строку. Состояние показа
// (фильтр, место чтения, кэши) живёт в pane, здесь только правила.
//
// Подписи таба здесь нет намеренно: имя вида уже названо один раз в
// viewNames, и второй источник того же текста рано или поздно разъедется с
// флагом --view.
type view interface {
	// Pick — берёт ли таб это событие.
	Pick(ev parse.Event) bool
	// Key — по чему таб разбивает свою сводку: сервер у mcp, буква операции у
	// files. Пустая строка означает «в сводке не считать».
	Key(ev parse.Event) string
	// Line — строка события в один ряд.
	Line(th *Theme, ev parse.Event, width int) string
	// Wrapped — та же строка, длинный хвост которой продолжен ниже.
	Wrapped(th *Theme, ev parse.Event, width int) []string
	// StatusAware — зависит ли строка от исхода вызова. У транскрипта нет:
	// он статус не рисует, и результат не должен стоить ему пересборки
	// десяти тысяч строк.
	StatusAware() bool
	// Summary — сводка таба для футера; считается по всему буферу, а не по
	// видимому. Пустая строка — сводки у таба нет.
	Summary(s paneStats) string
	// Empty — что написать вместо лога, когда таб пуст.
	Empty() string
}

// transcriptView — прежний вид лога. Он не меняется вовсе: те же колонки,
// те же цвета, тот же рендерер темы.
type transcriptView struct{}

func (transcriptView) Pick(parse.Event) bool { return true }
func (transcriptView) Empty() string         { return "no events in this session" }

// Key и Summary — у транскрипта сводки нет: он показывает поток целиком, и
// счётчик «сколько всего строк» не отвечает ни на один вопрос, ради которого
// в футер смотрят. Место в строке футера достаётся фильтру и статусу.
func (transcriptView) Key(parse.Event) string { return "" }
func (transcriptView) Summary(paneStats) string {
	return ""
}

// StatusAware — нет: вид транскрипта не меняется, исход в нём не показан.
// Отсюда экономия сшивки — результат не сбрасывает кэш самой длинной панели.
// Но только пока панель не фильтруют и не включён тумблер ошибок: отбор читает
// поля, которые дописывает applyUpdate, и там StatusAware уже не решает.
func (transcriptView) StatusAware() bool { return false }

func (transcriptView) Line(th *Theme, ev parse.Event, width int) string {
	return th.StyledFor(ev, width)
}

func (transcriptView) Wrapped(th *Theme, ev parse.Event, width int) []string {
	return th.StyledWrapped(ev, width)
}

// Общие размеры раскладки табов.
const (
	// timeWidth — колонка отметки времени, "15:04:05".
	timeWidth = 8
	// colGap — отбивка между колонками, общая для всех табов.
	colGap = 2
	// minServer и minMethod — ниже этих ширин колонка не сжимается: от
	// шестирунного имени ещё есть польза, от двухрунного — только многоточие.
	minServer = 6
	minMethod = 6
	// minArgs — ниже этого аргументы не показываются вовсе: в две-три колонки
	// от них остаётся одно многоточие, а место нужнее методу.
	minArgs = 8
	// unbounded — значение колонки, когда ширина окна ещё не известна: резать
	// не по чему, показывается всё.
	unbounded = -1
)

// mcpCols — ширины колонок таба mcp под конкретное окно.
//
// Порядок жертв задан дизайном: первыми уходят аргументы, потом сжимается и
// исчезает сервер, последним жмётся метод. Исход и длительность не жертвуются
// никогда — ради них таб и заведён, поэтому их блок вычтен из ширины ещё до
// того, как посчитана голова строки.
type mcpCols struct {
	source int
	server int // 0 — колонки сервера нет
	method int
	args   int // 0 — колонки аргументов нет; unbounded — ширина не известна
}

// mcpColumns раскладывает колонки таба mcp по ширине окна.
//
// Ступени, а не пропорции: на каждой ширине имена показываются одинаково, и
// строки не пляшут по колонкам от одного лишнего символа в окне. Наблюдаемые
// максимумы корпуса — 28 рун у сервера и 32 у метода, до них ступени и растут.
func mcpColumns(width int) mcpCols {
	c := mcpCols{source: parse.SourceWidth(width)}
	switch {
	case width <= 0 || width >= 140:
		c.server, c.method = 24, 30
	case width >= 120:
		c.server, c.method = 18, 28
	case width >= 100:
		c.server, c.method = 14, 24
	case width >= 80:
		c.server, c.method = 12, 20
	default:
		c.server, c.method = 10, 16
	}
	if width <= 0 {
		c.args = unbounded
		return c
	}

	// room — что осталось на сервер, метод и аргументы вместе с отбивками,
	// после отметки времени, источника и правого блока исхода.
	room := width - mcpCellWidth - timeWidth - colGap - c.source - colGap
	if room < minMethod {
		// Окно уже самой раскладки: показываем сколько влезет одним методом.
		return mcpCols{source: c.source, method: max(room, 0)}
	}

	if c.server+colGap+c.method > room {
		// Сервер жмётся и исчезает первым: метод называет саму операцию, а
		// сервер по методу обычно угадывается (find_symbol → serena).
		c.server = room - colGap - c.method
		if c.server < minServer {
			c.server, c.method = 0, min(c.method, room)
		}
	}

	used := c.method
	if c.server > 0 {
		used += c.server + colGap
	}
	if c.args = room - used - colGap; c.args < minArgs {
		c.args = 0
	}
	return c
}

// mcpView — вызовы MCP-ручек: сервер и метод разнесены по колонкам, потому
// что вопрос «какой сервер дёргали» решается взглядом по одной колонке, а не
// вычитыванием слипшегося mcp__server__method.
type mcpView struct{}

func (mcpView) Pick(ev parse.Event) bool {
	_, _, ok := parse.MCPParts(ev.Tool)
	return ok
}

// Key — сервер: вопрос сводки «кого дёргали чаще всего» решается по нему.
func (mcpView) Key(ev parse.Event) string {
	server, _, _ := parse.MCPParts(ev.Tool)
	return server
}

// Summary — сводка таба mcp. Отказы считаются ОТДЕЛЬНО от ошибок: в живом
// корпусе 85 из 252 неуспешных вызовов — это отказы правила разрешений, и
// слитый счётчик врал бы про сбои втрое.
func (mcpView) Summary(s paneStats) string {
	if s.total == 0 {
		return ""
	}
	line := fmt.Sprintf("mcp: %d calls · %d err · %d denied",
		s.total, s.count(parse.StatusError), s.count(parse.StatusDenied))
	if top, n := s.top(); top != "" {
		line += fmt.Sprintf(" · %s %d", top, n)
	}
	return line
}

// Empty — пустой таб MCP это норма, а не сбой: в живом корпусе больше
// половины сессий с инструментами не делают ни одного MCP-вызова.
func (mcpView) Empty() string { return "no MCP calls in this session" }

// StatusAware — да: исход и длительность и есть то, ради чего таб заведён.
func (mcpView) StatusAware() bool { return true }

func (mcpView) Line(th *Theme, ev parse.Event, width int) string {
	cols := mcpColumns(width)
	prefix, detail := mcpParts(th, ev, width, cols)

	line := prefix
	// Аргументы — первая жертва тесноты: на узком окне колонки для них нет.
	// У неуспешного вызова их замещает причина сбоя: в одну строку не влезает
	// и то и другое, а причина важнее.
	if tail := tailText(ev, detail); tail != "" && cols.args != 0 {
		line += "  " + th.detail.Render(tail)
	}
	return withCell(line, mcpCell(th, ev), mcpCellWidth, width)
}

func (mcpView) Wrapped(th *Theme, ev parse.Event, width int) []string {
	prefix, detail := mcpParts(th, ev, width, mcpColumns(width))
	body := bodyWidth(width, mcpCellWidth)

	// В режиме переноса не жертвуется ничто: аргументы продолжаются ниже, а
	// причина сбоя идёт отдельной подстрокой — ради этого перенос и включают.
	lines := wrapTail(prefix+"  ", detail, th.detail, body)
	lines[0] = withCell(lines[0], mcpCell(th, ev), mcpCellWidth, width)
	return appendFail(lines, th, ev, prefix+"  ", body)
}

// mcpParts собирает голову строки таба и деталь, которую можно переносить.
// Одна раскладка на оба режима: обычный и с переносом обязаны совпадать по
// колонкам. Отбивку после головы приписывает вызывающий — в обычном режиме
// хвоста может не быть вовсе.
// Колонку источника считает сам parse.PartsFor — та же, что у транскрипта
// (cols.source равен ей по построению): второй способ посчитать её разошёлся
// бы с первым.
func mcpParts(th *Theme, ev parse.Event, width int, cols mcpCols) (prefix, detail string) {
	server, method, _ := parse.MCPParts(ev.Tool)
	ts, source, _, detail := parse.PartsFor(ev, width)

	prefix = th.dim.Render(ts) + "  " + th.ColorFor(ev.Source).Render(source) + "  "
	if cols.server > 0 {
		prefix += th.label.Render(parse.Fit(server, cols.server)) + "  "
	}
	prefix += th.tool.Render(parse.Fit(method, cols.method))
	return prefix, detail
}

// tailText — что показать в колонке аргументов таба mcp. У неуспешного вызова
// её ЗАМЕЩАЕТ текст ошибки: причина отказа важнее аргументов, а таблица
// остаётся по строке на событие. Аргументы при этом не теряются — они видны в
// транскрипте, где эта строка тоже есть, и в режиме переноса.
func tailText(ev parse.Event, detail string) string {
	if hasFail(ev) {
		return parse.Clean(ev.Fail)
	}
	return detail
}

// hasFail — есть ли у события причина сбоя, которую стоит показать. Текст
// ошибки без неуспешного исхода не показывается: он приезжает только вместе с
// результатом, и рисовать его у успешного вызова было бы враньём.
func hasFail(ev parse.Event) bool {
	return ev.Fail != "" && (ev.Status == parse.StatusError || ev.Status == parse.StatusDenied)
}

// fileOpWidth — колонка операции: одна буква R/W/E/N.
const fileOpWidth = 1

// fileOpOrder — порядок букв в сводке таба файлов. Фиксированный, а не обход
// карты: сводка не должна переставляться от кадра к кадру.
var fileOpOrder = []string{"R", "W", "E", "N"}

// fileCols — раскладка таба файлов. Жертвовать тут нечем, кроме пути: колонка
// операции — одна буква, а исход не жертвуется никогда.
type fileCols struct {
	source int
	path   int // unbounded — ширина окна не известна
}

// fileColumns раскладывает колонки таба файлов по ширине окна.
func fileColumns(width int) fileCols {
	c := fileCols{source: parse.SourceWidth(width)}
	if width <= 0 {
		c.path = unbounded
		return c
	}
	c.path = max(width-fileCellWidth-timeWidth-colGap-c.source-colGap-fileOpWidth-colGap, 0)
	return c
}

// filesView — файловые операции с полным путём. Длительности здесь нет
// намеренно: у чтения и правки медиана 94 мс, колонка была бы шумом, а место
// нужнее пути.
type filesView struct{}

func (filesView) Pick(ev parse.Event) bool {
	_, ok := parse.FileOp(ev.Tool)
	return ok
}

func (filesView) Empty() string { return "no file operations in this session" }

// Key — буква операции: сводка отвечает на вопрос «читали или писали».
func (filesView) Key(ev parse.Event) string {
	op, ok := parse.FileOp(ev.Tool)
	if !ok {
		return ""
	}
	return string(op)
}

// Summary — сводка таба файлов: сколько операций и каких. Ноль по букве не
// печатается: «W 0» занимает место, отвечая на незаданный вопрос.
func (filesView) Summary(s paneStats) string {
	if s.total == 0 {
		return ""
	}
	line := fmt.Sprintf("files: %d ops", s.total)
	for _, op := range fileOpOrder {
		if n := s.byKey[op]; n > 0 {
			line += fmt.Sprintf(" · %s %d", op, n)
		}
	}
	return line
}

// StatusAware — да: отказ записи и «файл не прочитан» видно именно здесь.
func (filesView) StatusAware() bool { return true }

func (filesView) Line(th *Theme, ev parse.Event, width int) string {
	prefix, tail := fileParts(th, ev, width)
	// Путь режется С ГОЛОВЫ: хвост важнее — по имени файла строку и узнают,
	// а общее начало у всех путей проекта одинаковое.
	tail = clipPathHead(tail, fileColumns(width).path)
	return withCell(prefix+th.detail.Render(tail), statusCell(th, ev), fileCellWidth, width)
}

func (filesView) Wrapped(th *Theme, ev parse.Event, width int) []string {
	prefix, tail := fileParts(th, ev, width)
	body := bodyWidth(width, fileCellWidth)

	// Голова пути в режиме переноса не режется: он продолжается ниже целиком.
	lines := wrapTail(prefix, tail, th.detail, body)
	lines[0] = withCell(lines[0], statusCell(th, ev), fileCellWidth, width)
	return appendFail(lines, th, ev, prefix, body)
}

func fileParts(th *Theme, ev parse.Event, width int) (prefix, tail string) {
	op, _ := parse.FileOp(ev.Tool)
	ts, source, _, _ := parse.PartsFor(ev, width)

	prefix = th.dim.Render(ts) + "  " +
		th.ColorFor(ev.Source).Render(source) + "  " +
		th.label.Render(parse.Fit(string(op), fileOpWidth)) + "  "
	// Путь текстом ошибки НЕ замещается: у файловой операции это единственная
	// содержательная колонка, и подменять её значило бы терять сам предмет
	// строки. Причина сбоя видна в транскрипте, где эта строка тоже есть, а в
	// режиме переноса — подстрокой «└ …» под самим путём.
	return prefix, filePathOf(ev)
}

// filePathOf — путь файловой операции для показа. Пустой путь рисуется знаком
// вопроса: у пары вызовов из тысячи аргументы приходят нераспарсенными
// (__unparsedToolInput), и молчаливая дыра в колонке выглядит как баг рендера.
func filePathOf(ev parse.Event) string {
	if ev.Path == "" {
		return "?"
	}
	return parse.Clean(ev.Path)
}

// clipPathHead укорачивает путь с ГОЛОВЫ: "…/internal/parse/decode.go".
// Обычная обрезка с хвоста оставила бы от всех путей проекта одинаковое
// начало и съела бы имя файла — то единственное, ради чего на путь смотрят.
//
// Рез идёт по границе компонента, пока хоть один компонент влезает: "…/decode.go"
// читается, "…ode.go" — нет. width == unbounded означает «ширина окна не
// известна», путь остаётся целым.
func clipPathHead(path string, width int) string {
	if width == unbounded {
		return path
	}
	if width <= 0 {
		return ""
	}
	r := []rune(path)
	if len(r) <= width {
		return path
	}
	if width == 1 {
		return "…"
	}
	// Что считать границей, решает сам путь: у виндового её ставит и «\», у
	// прочих — только «/», иначе бэкслеш в linux-имени сойдёт за разделитель.
	win := parse.WindowsPath(path)
	for i, c := range r {
		// Первая же граница, укладывающаяся в ширину вместе с многоточием,
		// даёт самый длинный читаемый хвост.
		if parse.PathSep(c, win) && len(r)-i+1 <= width {
			return "…" + string(r[i:])
		}
	}
	// Даже последний компонент не влезает — режем по рунам.
	return "…" + string(r[len(r)-(width-1):])
}

// Правый блок строки: исход вызова и его длительность.
//
// Ширины фиксированы под худший случай (DENY и 15h26m), а сам блок прижат к
// правому краю окна: одна строка из тысячи не должна двигать колонку, по
// которой взгляд ищет неудачи. Место под блок отбирается у аргументов —
// исход и длительность не жертвуются никогда, ради них таб и заведён.
const (
	statusWidth = 4 // самое длинное имя исхода — DENY
	durWidth    = 6 // худшая длительность корпуса — 15h26m

	// Отбивка перед блоком — та же colGap, что и между прочими колонками.
	mcpCellWidth  = colGap + statusWidth + 1 + durWidth
	fileCellWidth = colGap + statusWidth
)

// statusText — как исход подписан в колонке. Незакрытый вызов помечается
// точкой, а не пустотой: «результата ещё нет» — это состояние живой сессии,
// и путать его с «показывать нечего» нельзя.
func statusText(s parse.Status) string {
	switch s {
	case parse.StatusOK:
		return "ok"
	case parse.StatusError:
		return "ERR"
	case parse.StatusDenied:
		return "DENY"
	default:
		return "·"
	}
}

// slowMCP — с какой длительности вызов MCP-ручки считается медленным и
// подсвечивается тёплым. Порог свой у таба: по корпусу p90 MCP-вызова 4.2 с,
// так что за пять секунд уходит десятая часть — ровно те, на которые стоит
// посмотреть. Общий порог с файловыми операциями (p90 = 461 мс) красил бы
// либо всё, либо ничего.
const slowMCP = 5 * time.Second

// statusCell собирает правый блок строки: только исход. Длительности у
// файловых операций нет намеренно — медиана 94 мс, колонка была бы шумом, а
// место нужнее пути.
func statusCell(th *Theme, ev parse.Event) string {
	return strings.Repeat(" ", colGap) +
		th.statusStyle(ev.Status).Render(parse.Fit(statusText(ev.Status), statusWidth))
}

// mcpCell — правый блок таба mcp: исход и длительность. Длительность выше
// порога подсвечивается тёплым, чтобы медленная ручка бросалась в глаза, а не
// вычитывалась из колонки цифр.
func mcpCell(th *Theme, ev parse.Event) string {
	style := th.dim
	if ev.Dur >= slowMCP {
		style = th.tokWrite
	}
	return statusCell(th, ev) + " " + style.Render(durText(ev.Dur))
}

// durText — длительность вызова, выровненная вправо по колонке. Нулевая
// длительность означает «не известна» (результата ещё нет либо метки времени
// испорчены) и рисуется пробелами: «0s» соврало бы про мгновенный ответ.
//
// Проверка идёт по округлению HumanMillis (d.Milliseconds()), а не по самому
// d: доля миллисекунды тоже округляется в 0 и напечаталась бы как «0s»,
// хотя это тот же обман про мгновенный ответ.
func durText(d time.Duration) string {
	if d.Milliseconds() <= 0 {
		return strings.Repeat(" ", durWidth)
	}
	text := parse.HumanMillis(d.Milliseconds())
	if pad := durWidth - len([]rune(text)); pad > 0 {
		return strings.Repeat(" ", pad) + text
	}
	// Длительность приходит из меток записей транскрипта, то есть снаружи, а
	// ветка часов (%dh%dm) ничем не ограничена: «15h26m» — наблюдённый
	// максимум корпуса, а не предел формата. Без обрезки более длинная строка
	// сдвигала бы колонку исхода вправо — та же
	// ошибка, что уже правили в maxToolName.
	return parse.Fit(text, durWidth)
}

// bodyWidth — сколько колонок остаётся строке слева от правого блока.
func bodyWidth(width, cellWidth int) int {
	if width <= cellWidth {
		return width
	}
	return width - cellWidth
}

// withCell ставит правый блок на фиксированное место у края окна, отбирая
// место у хвоста строки. Когда ширина окна ещё не известна (width<=0), блок
// просто приписывается следом. Единственное исключение, где он всё же
// теряется, — окно уже, чем сам блок (width<=cellWidth): раскладывать в нём
// нечего, а такая ширина в реальном терминале не встречается.
func withCell(line, cell string, cellWidth, width int) string {
	if width <= 0 {
		return line + cell
	}
	if width <= cellWidth {
		// В такое окно не влезает и сам блок: раскладывать нечего.
		return line
	}
	body := width - cellWidth
	return padTo(clip(line, body), body) + cell
}

// minTailWidth — сколько колонок должно остаться хвосту, чтобы перенос под
// колонкой имел смысл; иначе продолжение прижимается к левому краю.
const minTailWidth = 20

// tailIndent — отступ и ширина продолжения строки, плюс признак «хвост
// начинается на строке головы».
//
// Отступ считается по видимой ширине уже собранной головы: складывать его из
// ширин колонок значило бы держать вторую, независимую копию раскладки,
// которая молча разъедется с первой.
//
// Когда голова съедает почти всё окно, продолжение прижимается к левому краю —
// и тогда хвосту на строке головы делать нечего: первый его кусок посчитан по
// широкому отступу и был бы срезан краем окна, а срезанное в режиме переноса
// теряется совсем (продолжение идёт со следующего куска).
func tailIndent(prefix string, width int) (indent, avail int, inline bool) {
	indent = lipgloss.Width(prefix)
	if avail := width - indent; avail >= minTailWidth {
		return indent, avail, true
	}
	return 2, max(width-2, minTailWidth), false
}

// appendFail дописывает причину сбоя отдельной строкой «└ …» под колонкой
// деталей — та самая подстрока из макета спеки.
//
// Только в режиме переноса: там для неё есть место и аргументы никуда не
// деваются. В обычном режиме текст ошибки замещает колонку аргументов
// (tailText) — иначе таблица перестала бы быть по строке на событие.
func appendFail(lines []string, th *Theme, ev parse.Event, prefix string, width int) []string {
	if !hasFail(ev) {
		return lines
	}
	indent, avail, _ := tailIndent(prefix, width)

	const mark = "└ "
	for i, chunk := range wrapRunes(parse.Clean(ev.Fail), max(avail-len([]rune(mark)), 1)) {
		lead := mark
		if i > 0 {
			// Продолжение длинной ошибки идёт под её же текстом, а не под
			// уголком: уголок помечает начало, а не каждую строку.
			lead = "  "
		}
		lines = append(lines, strings.Repeat(" ", indent)+th.failure.Render(lead+chunk))
	}
	return lines
}

// wrapTail продолжает длинный хвост строки под той же колонкой. Приём тот же,
// что у транскрипта (Theme.StyledWrapped), но колонки у табов свои.
func wrapTail(prefix, tail string, style lipgloss.Style, width int) []string {
	indent, avail, inline := tailIndent(prefix, width)

	chunks := wrapRunes(tail, avail)
	if len(chunks) == 0 {
		return []string{strings.TrimRight(prefix, " ")}
	}

	out := make([]string, 0, len(chunks)+1)
	if inline {
		out = append(out, prefix+style.Render(chunks[0]))
		chunks = chunks[1:]
	} else {
		out = append(out, strings.TrimRight(prefix, " "))
	}
	for _, chunk := range chunks {
		out = append(out, strings.Repeat(" ", indent)+style.Render(chunk))
	}
	return out
}
