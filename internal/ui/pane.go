package ui

import (
	"fmt"
	"sort"

	"charm.land/bubbles/v2/viewport"

	"github.com/KratosUAE/ccon/internal/parse"
)

// statusCount — сколько исходов бывает у вызова (parse.Status). Счётчики
// сводки лежат массивом, а не картой: исходов ровно четыре и они плотные.
// Совпадение с parse стережёт TestStatusCountCoversParse.
const statusCount = 4

// paneStats — счётчики таба по ВСЕМУ буферу: фильтр влияет только на видимое,
// а сводка футера — нет (прямое требование спеки).
type paneStats struct {
	total    int
	byStatus [statusCount]int
	// byKey — разбивка сводки: сервер у mcp, буква операции у files.
	byKey map[string]int
}

// add учитывает событие, взятое панелью.
func (s *paneStats) add(ev parse.Event, v view) {
	s.total++
	s.bump(ev.Status, 1)

	key := v.Key(ev)
	if key == "" {
		return
	}
	if s.byKey == nil {
		s.byKey = make(map[string]int)
	}
	s.byKey[key]++
}

// restate переносит счётчик события из прежнего исхода в новый: строка была
// незакрытой, приехал результат. Без этого сводка считала бы вечно ожидающими
// все вызовы сессии.
func (s *paneStats) restate(from, to parse.Status) {
	if from == to {
		return
	}
	s.bump(from, -1)
	s.bump(to, 1)
}

// bump правит счётчик исхода. Исход за пределами известных не учитывается
// вовсе — соврать в сводке хуже, чем недосчитать; за тем, чтобы таких не
// появилось, следит TestStatusCountCoversParse.
func (s *paneStats) bump(st parse.Status, delta int) {
	if i := int(st); i >= 0 && i < statusCount {
		s.byStatus[i] += delta
	}
}

// count — сколько вызовов кончились этим исходом.
func (s *paneStats) count(st parse.Status) int {
	if i := int(st); i >= 0 && i < statusCount {
		return s.byStatus[i]
	}
	return 0
}

// top — самая частая разбивка сводки и её счёт. При равенстве побеждает
// меньшее имя: сводка не должна плясать от обхода карты.
func (s *paneStats) top() (string, int) {
	best, n := "", 0
	for key, count := range s.byKey {
		if count > n || (count == n && key < best) {
			best, n = key, count
		}
	}
	return best, n
}

// paneRow — мемо одной строки таба.
//
// Строка стоит дорого (тема, колонки, обрезка), а перебирается дёшево. Пока
// набирают фильтр, панель перебирает мемо и НЕ пересчитывает строки: иначе
// каждая буква стоит перерисовки всей сессии — на полном буфере это две
// десятых секунды на нажатие.
//
// Ключ строки — сквозной номер события, а не индекс в буфере: пока строка
// живёт, голова буфера вытесняется.
type paneRow struct {
	seq int64
	// status — исход, уже учтённый в счётчиках сводки; по нему правится
	// сводка, когда к вызову приезжает результат.
	status parse.Status
	plain  string
	// wrap — та же строка с переносом; nil означает «ещё не считали»:
	// перенос включают редко, а стоит он столько же, сколько обычная строка.
	wrap []string
}

// pane — состояние показа одного таба.
//
// События панель не хранит: они лежат в одном кольцевом буфере на все табы, а
// панель держит только готовые строки. Поэтому три вида над одним потоком не
// стоят трёх копий сессии — и мемо у каждого своё лишь потому, что строки у
// табов РАЗНЫЕ: mcp и files берут сотни строк там, где транскрипт берёт все.
//
// Фильтр, тумблеры, режим переноса, место чтения и автоскролл у каждой панели
// свои: переключение туда и обратно не должно терять место чтения.
type pane struct {
	v  view
	vp viewport.Model

	filter    string // применённый фильтр
	input     string // строка ввода, пока фильтр набирают
	filtering bool

	// errOnly — тумблер «только неуспешные»: всё, кроме успеха и ожидания.
	// Отказ он НЕ прячет: отказ правила — такой же незакрывшийся вопрос, как
	// сбой, и искать его приходится там же.
	errOnly bool
	// hideSystem — тумблер системного шума. Системных записей в живом корпусе
	// 14.9 % строк (в основном turn_duration), и прятать их фильтром нельзя:
	// фильтр отбирает совпавшее, а тут нужно ровно обратное.
	hideSystem bool
	wrap       bool
	autoFollow bool

	// rows — мемо строк ВСЕХ событий, взятых предикатом вида, без учёта
	// фильтра и тумблеров. nil до первого показа: непосещённый таб не стоит
	// ничего.
	rows  []paneRow
	stats paneStats

	// cachePln и cacheWrap — ВИДИМОЕ: то, что осталось от мемо после фильтра
	// и тумблеров. Оба ДОПИСЫВАЮТСЯ, а не пересобираются: при старте живой
	// сессии watcher отдаёт тысячи строк подряд, и полная пересборка на
	// каждую — квадратичный разогрев и замерзший интерфейс.
	cachePln  []string
	cacheWrap []string
}

func newPane(v view) *pane {
	return &pane{v: v, vp: viewport.New(), autoFollow: true}
}

// shows — показывать ли событие сейчас: фильтр и тумблеры. Предикат вида сюда
// не входит — он уже отобрал события в мемо.
func (p *pane) shows(ev parse.Event) bool {
	if p.hideSystem && ev.Kind == parse.KindSystem {
		return false
	}
	if !matches(ev, p.filter) {
		return false
	}
	return !p.errOnly || failed(ev)
}

// failed — считается ли строка неуспешной для тумблера «только ошибки».
//
// Род KindError входит сюда ради транскрипта: исхода вызова у его строк нет,
// а красная строка ошибки результата видна именно там, и тумблер обязан её
// сохранять. Табам mcp и files это ничего не даёт — событий такого рода они
// не берут вовсе.
func failed(ev parse.Event) bool {
	return ev.Status == parse.StatusError || ev.Status == parse.StatusDenied || ev.Kind == parse.KindError
}

// observe дописывает событие в мемо и в непустые кэши видимого. Пустое мемо не
// трогает: его построит content, когда таб покажут.
func (p *pane) observe(ev parse.Event, seq int64, th *Theme, width int) {
	if p.rows == nil || !p.v.Pick(ev) {
		return
	}
	p.rows = append(p.rows, p.render(seq, ev, th, width))
	p.stats.add(ev, p.v)

	if !p.shows(ev) {
		return
	}
	row := &p.rows[len(p.rows)-1]
	if p.cachePln != nil {
		p.cachePln = append(p.cachePln, row.plain)
	}
	if p.cacheWrap != nil {
		p.cacheWrap = append(p.cacheWrap, p.wrapOf(row, ev, th, width)...)
	}
}

// update перерисовывает строку вызова, к которому приехал исход, и сообщает,
// надо ли пересобрать видимое.
func (p *pane) update(seq int64, ev parse.Event, th *Theme, width int) bool {
	if p.rows == nil {
		// Таб не показывали — мемо соберётся сразу с исходом.
		return false
	}
	i, ok := p.rowAt(seq)
	if !ok {
		// Строка вытеснена вместе со своим событием: чинить нечего.
		return false
	}

	p.stats.restate(p.rows[i].status, ev.Status)
	p.rows[i].status = ev.Status

	if p.v.StatusAware() {
		// Строка рисует исход — её надо пересчитать, но только её одну.
		p.rows[i].plain = p.line(ev, th, width)
		p.rows[i].wrap = nil
		return true
	}
	// Рендер от исхода не зависит (транскрипт), но ОТБОР зависит: тумблер
	// ошибок читает Status, фильтр — Fail через parse.Haystack. Оба поля
	// дописываются задним числом.
	return p.errOnly || p.filter != ""
}

// rowAt находит строку мемо по сквозному номеру события. Двоичный поиск:
// номера возрастают, а исход вызова адресуется задним числом.
func (p *pane) rowAt(seq int64) (int, bool) {
	i := sort.Search(len(p.rows), func(i int) bool { return p.rows[i].seq >= seq })
	if i < len(p.rows) && p.rows[i].seq == seq {
		return i, true
	}
	return 0, false
}

func (p *pane) render(seq int64, ev parse.Event, th *Theme, width int) paneRow {
	return paneRow{seq: seq, status: ev.Status, plain: p.line(ev, th, width)}
}

func (p *pane) line(ev parse.Event, th *Theme, width int) string {
	return fitLine(p.v.Line(th, ev, width), width)
}

// wrapOf отдаёт перенесённое представление строки, считая его при первой
// нужде.
func (p *pane) wrapOf(row *paneRow, ev parse.Event, th *Theme, width int) []string {
	if row.wrap == nil {
		lines := p.v.Wrapped(th, ev, width)
		for i, line := range lines {
			lines[i] = fitLine(line, width)
		}
		row.wrap = lines
	}
	return row.wrap
}

// refilter сбрасывает ВИДИМОЕ, оставляя мемо строк: смена фильтра и тумблеров
// меняет отбор, но не сами строки.
func (p *pane) refilter() { p.cachePln, p.cacheWrap = nil, nil }

// restyle сбрасывает и мемо: изменилось то, из чего собраны сами строки —
// ширина окна, подпись источника или состав буфера.
func (p *pane) restyle() {
	p.rows, p.stats = nil, paneStats{}
	p.refilter()
}

// content отдаёт строки текущего представления, строя недостающее.
// Переключение переноса не должно стоить пересборки всей сессии, поэтому
// кэшируются оба.
func (p *pane) content(r *ring, th *Theme, width int) []string {
	p.fill(r, th, width)
	if p.wrap {
		if p.cacheWrap == nil {
			p.cacheWrap = p.collect(r, th, width, true)
		}
		return p.cacheWrap
	}
	if p.cachePln == nil {
		p.cachePln = p.collect(r, th, width, false)
	}
	return p.cachePln
}

// fill строит мемо потоковым проходом по буферу: копии событий никто не
// держит. Зовётся при первом показе таба и после сброса строк.
func (p *pane) fill(r *ring, th *Theme, width int) {
	if p.rows != nil {
		return
	}
	p.rows, p.stats = make([]paneRow, 0, r.len()), paneStats{}
	r.each(func(seq int64, ev parse.Event) {
		if !p.v.Pick(ev) {
			return
		}
		p.rows = append(p.rows, p.render(seq, ev, th, width))
		p.stats.add(ev, p.v)
	})
}

// collect отбирает из мемо то, что видно сейчас. Это и есть цена нажатия
// клавиши в фильтре: перебор готовых строк, а не их пересчёт.
func (p *pane) collect(r *ring, th *Theme, width int, wrap bool) []string {
	out := make([]string, 0, len(p.rows))
	for i := range p.rows {
		ev, live := r.at(p.rows[i].seq)
		if !live {
			// Событие вытеснено, а мемо ещё не сброшено: показывать нечего.
			continue
		}
		if !p.shows(ev) {
			continue
		}
		if !wrap {
			out = append(out, p.rows[i].plain)
			continue
		}
		out = append(out, p.wrapOf(&p.rows[i], ev, th, width)...)
	}
	return out
}

// apply кладёт представление в viewport. Показывать нечего — панель говорит
// об этом словами: пустой таб mcp это самое частое состояние, и молчаливая
// пустота неотличима от поломки.
func (p *pane) apply(r *ring, th *Theme, width int) {
	lines := p.content(r, th, width)
	if len(lines) == 0 {
		lines = []string{fitLine(th.dim.Render(p.emptyText()), width)}
	}

	p.vp.SetContentLines(lines)
	if p.autoFollow {
		p.vp.GotoBottom()
	}
}

// emptyText объясняет пустой экран: отсутствие таких событий вовсе,
// отфильтрованный в ноль список и включённый тумблер ошибок — три разных
// состояния, и путать их нельзя. «Ошибок нет» — хорошая новость, а не поломка.
func (p *pane) emptyText() string {
	switch {
	case p.errOnly && p.filter != "":
		return fmt.Sprintf("no failures match %q", p.filter)
	case p.errOnly:
		return "no failed calls in this session"
	case p.hideSystem:
		// Четвёртое состояние: события есть, их спрятал
		// тумблер s, а не отсутствие событий вовсе — путать эти два случая
		// запрещает комментарий над самой функцией.
		return "only system records here — press s to show them"
	case p.filter != "":
		return fmt.Sprintf("nothing matches %q", p.filter)
	default:
		return p.v.Empty()
	}
}

// label — что показать в строке фильтра футера.
func (p *pane) filterLabel() string {
	if p.filtering {
		return "/" + p.input + "▏"
	}
	return p.filter
}

// summary — сводка таба для футера. Счётчики берутся из мемо, а не пересчётом
// по буферу на каждый кадр; мемо активной панели заполняет applyContent.
func (p *pane) summary() string { return p.v.Summary(p.stats) }

// fitLine подгоняет строку под ширину окна: обрезка с многоточием и добивка
// пробелами. Без добивки при сжатии панели справа остаются обрывки прежнего,
// более широкого кадра — терминал перерисовывает только присланное.
// Нулевая ширина означает «окно ещё не измерено»: строка идёт как есть.
func fitLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return padTo(clip(s, width), width)
}
