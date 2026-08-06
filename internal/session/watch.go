package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KratosUAE/ccon/internal/parse"
	"github.com/KratosUAE/ccon/internal/tailer"
)

// SourceMain — имя источника главного потока сессии.
const SourceMain = "main"

// Интервалы опроса: файлы опрашиваются часто, каталог субагентов — редко.
// Новых файлов там появляются единицы за сессию, гонять ReadDir по пять раз в
// секунду при полусотне файлов незачем.
const (
	DefaultFileInterval = 200 * time.Millisecond
	DefaultDirInterval  = time.Second
)

// agentPrefix — имена транскриптов субагентов.
const agentPrefix = "agent-"

// shortIDLen — сколько знаков идентификатора оставлять в подписи источника.
const shortIDLen = 8

const (
	// hotDirInterval и hotWindow — как часто и как долго опрашивать каталог
	// после замеченного делегирования: субагент появляется в первые секунды.
	hotDirInterval = 100 * time.Millisecond
	hotWindow      = 3 * time.Second

	errBuffer    = 8
	renameBuffer = 32

	// freeMetaTries — сколько раз описание пробуется без троттлинга.
	freeMetaTries = 3
)

// Разметка вызова делегирования в сырой строке JSONL.
var (
	delegationAgent = []byte(`"name":"Agent"`)
	delegationTask  = []byte(`"name":"Task"`)
)

// Line — строка транскрипта вместе с тем, откуда она пришла.
type Line struct {
	Data []byte // сырая строка JSONL
	Name string // agentType из .meta.json; пусто, если описание ещё не дописано
	ID   string // main либо agent-<короткий id>
	Path string // файл-источник
}

// Source выбирает имя источника для показа. Приоритет: agentType из
// .meta.json, затем attributionAgent из самой записи, затем короткий agent-id.
// Средняя ступень нужна из-за реальной гонки: описание субагента может быть
// ещё не дописано, когда его первые строки уже появились.
func (l Line) Source(fromRecord string) string {
	if l.Name != "" {
		return l.Name
	}
	// attributionAgent встречается только в файлах субагентов, поэтому для
	// главного потока эта ветка не срабатывает и подписью остаётся main.
	if fromRecord != "" && fromRecord != SourceMain {
		return fromRecord
	}
	return l.ID
}

// WatchOption настраивает наблюдение.
type WatchOption func(*watchConfig)

type watchConfig struct {
	fileInterval time.Duration
	dirInterval  time.Duration
}

// WithFileInterval задаёт период опроса файлов.
func WithFileInterval(d time.Duration) WatchOption {
	return func(c *watchConfig) {
		if d > 0 {
			c.fileInterval = d
		}
	}
}

// WithDirInterval задаёт период опроса каталога субагентов.
func WithDirInterval(d time.Duration) WatchOption {
	return func(c *watchConfig) {
		if d > 0 {
			c.dirInterval = d
		}
	}
}

// Rename — субагент получил настоящее имя после того, как его строки уже
// ушли в лог под откатной подписью. Потребитель (кольцевой буфер лога)
// обязан уметь переподписать показанное.
type Rename struct {
	ID   string // прежняя подпись, agent-<короткий id>
	Name string // agentType из появившегося .meta.json
	Path string
}

// Watcher — наблюдение за всеми потоками одной сессии.
type Watcher struct {
	target  Target
	cfg     watchConfig
	lines   chan Line
	errs    chan error
	renames chan Rename
	nudge   chan struct{}
	wg      sync.WaitGroup
	dropped atomic.Int64

	// caught закрывается, когда догон накопленного завершён.
	caught     chan struct{}
	caughtOnce sync.Once

	mu        sync.RWMutex
	names     map[string]string    // путь транскрипта → agentType из .meta.json
	metaTry   map[string]time.Time // когда описание последний раз пытались прочесть
	metaTries map[string]int       // сколько раз пытались до включения троттлинга
	lastSeen  map[string]bool      // уже читаемые файлы
	pending   int                  // сколько изначальных файлов ещё догоняют
}

// CaughtUp закрывается, когда все файлы, известные на момент старта, дочитаны
// до конца. До этого момента поток — это догон накопленного: события известны
// целиком и их можно упорядочить по времени. После — слежение, где порядок
// прихода единственно возможный.
func (w *Watcher) CaughtUp() <-chan struct{} { return w.caught }

// reportCaught отмечает, что очередной изначальный файл дочитан.
func (w *Watcher) reportCaught() {
	w.mu.Lock()
	w.pending--
	done := w.pending <= 0
	w.mu.Unlock()

	if done {
		w.caughtOnce.Do(func() { close(w.caught) })
	}
}

// Lines — общий поток строк всех файлов сессии.
func (w *Watcher) Lines() <-chan Line { return w.lines }

// Errs — сбои чтения; наблюдение при них продолжается.
func (w *Watcher) Errs() <-chan error { return w.errs }

// Renames — сообщения о поздно приехавших именах субагентов.
func (w *Watcher) Renames() <-chan Rename { return w.renames }

// Names — снимок «подпись → имя» на текущий момент. Нужен, чтобы свериться,
// если сигнал о переименовании был отброшен переполненным каналом.
//
// Возвращается копия: карта принадлежит вызывающему, наблюдение продолжает
// писать в свою. Потребителей уже двое, и общий доступ к внутренней карте
// стал бы гонкой.
func (w *Watcher) Names() map[string]string {
	w.mu.RLock()
	defer w.mu.RUnlock()

	out := make(map[string]string, len(w.names))
	for path, name := range w.names {
		out[shortAgentID(path)] = name
	}
	return out
}

// Watch следит за главным потоком и всеми потоками субагентов сразу,
// подхватывая новые agent-*.jsonl по мере появления.
//
// Порядок строк — порядок фактического прихода: при живом наблюдении
// глобальная сортировка по timestamp невозможна без неограниченной задержки,
// а задержка убивает весь смысл инструмента. Для завершённой сессии порядок
// восстанавливается сортировкой в архивном режиме (см. ReadAll).
func Watch(ctx context.Context, t Target, opts ...WatchOption) *Watcher {
	cfg := watchConfig{fileInterval: DefaultFileInterval, dirInterval: DefaultDirInterval}
	for _, opt := range opts {
		opt(&cfg)
	}

	w := &Watcher{
		target:    t,
		cfg:       cfg,
		lines:     make(chan Line),
		errs:      make(chan error, errBuffer),
		renames:   make(chan Rename, renameBuffer),
		nudge:     make(chan struct{}, 1),
		caught:    make(chan struct{}),
		names:     make(map[string]string),
		metaTry:   make(map[string]time.Time),
		metaTries: make(map[string]int),
		lastSeen:  make(map[string]bool),
	}

	// Изначальный состав узнаём до запуска тейлеров: иначе признак «догон
	// закончен» поднялся бы раньше, чем зарегистрированы файлы субагентов.
	initial := w.initialAgents()
	w.pending = 1 + len(initial)

	w.follow(ctx, t.Path, SourceMain, true)
	for _, path := range initial {
		w.refreshMeta(path)
		w.lastSeen[path] = true
		w.follow(ctx, path, shortAgentID(path), true)
	}

	w.wg.Add(1)
	go w.watchDir(ctx)

	go func() {
		w.wg.Wait()
		close(w.lines)
		close(w.errs)
		close(w.renames)
	}()

	return w
}

// initialAgents перечисляет транскрипты субагентов, существующие на старте.
func (w *Watcher) initialAgents() []string {
	if w.target.SubagentDir == "" {
		return nil
	}
	entries, err := os.ReadDir(w.target.SubagentDir)
	if err != nil {
		return nil
	}

	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), agentPrefix) &&
			strings.HasSuffix(e.Name(), transcriptExt) {
			out = append(out, filepath.Join(w.target.SubagentDir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// follow заводит тейлер на один файл и перекладывает его строки в общий поток.
// initial отмечает файлы, по которым считается завершение догона; субагент,
// родившийся позже, к догону не относится — он свежий по определению.
func (w *Watcher) follow(ctx context.Context, path, id string, initial bool) {
	// Лимит длины строки берётся из парсера: живой и архивный режимы обязаны
	// отбрасывать одни и те же строки, иначе цифры разойдутся необъяснимо.
	opts := []tailer.Option{
		tailer.WithInterval(w.cfg.fileInterval),
		tailer.WithMaxLine(parse.MaxLineBytes),
	}
	if initial {
		opts = append(opts, tailer.WithCaughtUp(w.reportCaught))
	}
	src, srcErrs := tailer.Tail(ctx, path, opts...)

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case data, ok := <-src:
				if !ok {
					return
				}
				// Делегирование — сигнал, что вот-вот появится новый поток:
				// проверка по сырым байтам стоит наносекунды и не требует
				// разбора JSON, зато снимает секундное опоздание подхвата.
				if isDelegation(data) {
					w.poke()
				}

				line := Line{Data: data, ID: id, Path: path, Name: w.lookupName(path)}
				select {
				case w.lines <- line:
				case <-ctx.Done():
					return
				}
			case err, ok := <-srcErrs:
				if !ok {
					srcErrs = nil // закрытый канал иначе крутил бы select вхолостую
					continue
				}
				if err != nil {
					w.report(err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// watchDir редко опрашивает каталог субагентов: подхватывает новые файлы и
// дочитывает описания, которых не было в прошлый раз.
func (w *Watcher) watchDir(ctx context.Context) {
	defer w.wg.Done()

	var hotUntil time.Time
	for {
		w.scanDir(ctx)
		w.reportDropped()

		wait := w.cfg.dirInterval
		if time.Now().Before(hotUntil) {
			wait = hotDirInterval
		}

		select {
		case <-ctx.Done():
			return
		case <-w.nudge:
			// Делегирование замечено: ближайшие пару секунд смотрим часто.
			hotUntil = time.Now().Add(hotWindow)
		case <-time.After(wait):
		}
	}
}

// poke просит опросить каталог не дожидаясь редкого такта.
func (w *Watcher) poke() {
	select {
	case w.nudge <- struct{}{}:
	default: // сигнал уже стоит в очереди — второй не нужен
	}
}

// isDelegation — в строке виден вызов Agent или Task.
func isDelegation(data []byte) bool {
	return bytes.Contains(data, delegationAgent) || bytes.Contains(data, delegationTask)
}

// reportDropped сообщает, сколько сообщений об ошибках пришлось выбросить:
// молча терять их при всплеске на полусотне файлов нельзя.
func (w *Watcher) reportDropped() {
	if n := w.dropped.Swap(0); n > 0 {
		select {
		case w.errs <- fmt.Errorf("dropped error messages: %d", n):
		default:
			w.dropped.Add(n)
		}
	}
}

func (w *Watcher) scanDir(ctx context.Context) {
	if w.target.SubagentDir == "" {
		return
	}
	entries, err := os.ReadDir(w.target.SubagentDir)
	if err != nil {
		// Каталога может не быть вовсе (сессия без делегирований) либо он
		// появится позже — это штатно, а не ошибка.
		return
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), agentPrefix) &&
			strings.HasSuffix(e.Name(), transcriptExt) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // детерминированный порядок появления источников

	for _, name := range names {
		path := filepath.Join(w.target.SubagentDir, name)
		w.refreshMeta(path)

		w.mu.Lock()
		fresh := !w.lastSeen[path]
		w.lastSeen[path] = true
		w.mu.Unlock()

		if fresh {
			// ENOENT между ReadDir и открытием — нормальная гонка: тейлер
			// просто подождёт, пока файл появится снова.
			w.follow(ctx, path, shortAgentID(path), false)
		}
	}
}

// refreshMeta дочитывает описание субагента, пока оно не появится.
// Появление имени — событие: о нём сообщаем, чтобы уже показанные строки
// можно было переподписать.
func (w *Watcher) refreshMeta(path string) {
	if !w.shouldRetryMeta(path, time.Now()) {
		return
	}

	meta, err := ReadMeta(path)
	if err != nil {
		return // описание ещё не дописано или битое — работаем по откату
	}

	w.mu.Lock()
	known := w.names[path] != ""
	w.names[path] = meta.AgentType
	w.mu.Unlock()

	if known {
		return
	}
	select {
	case w.renames <- Rename{ID: shortAgentID(path), Name: meta.AgentType, Path: path}:
	default: // канал переполнен — потребитель сверится через Names()
	}
}

// shouldRetryMeta не даёт перечитывать отсутствующее описание на каждой
// строке: у агента без .meta.json это неудачный os.Open на каждую запись.
// Чаще, чем раз в такт опроса каталога, пробовать незачем — каталог и так
// дёргает эту проверку сам.
func (w *Watcher) shouldRetryMeta(path string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.names[path] != "" {
		return false
	}

	// Первые попытки бесплатны: описание пишется сразу вслед за транскриптом,
	// и опрос каталога легко попадает в этот зазор в микросекунды. Без такой
	// поблажки первые строки агента уходили бы в лог под откатной подписью,
	// хотя описание уже на диске.
	if w.metaTries[path] < freeMetaTries {
		w.metaTries[path]++
		w.metaTry[path] = now
		return true
	}

	if last, ok := w.metaTry[path]; ok && now.Sub(last) < w.cfg.dirInterval {
		return false
	}
	w.metaTry[path] = now
	return true
}

func (w *Watcher) nameOf(path string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.names[path]
}

// lookupName отдаёт имя субагента, пробуя дочитать описание прямо сейчас.
// Ждать редкого опроса каталога нельзя: первые строки агента ушли бы в лог
// под откатным именем, хотя описание уже на диске. Неудачное открытие стоит
// пару микросекунд и прекращается, как только описание найдено.
func (w *Watcher) lookupName(path string) string {
	if name := w.nameOf(path); name != "" {
		return name
	}
	if path == w.target.Path {
		return "" // у главного потока описания не бывает
	}

	w.refreshMeta(path)
	return w.nameOf(path)
}

// report доставляет ошибку, не блокируя чтение, если её не читают.
func (w *Watcher) report(err error) {
	select {
	case w.errs <- err:
	default:
	}
}

// ReadAll читает главный поток и все потоки субагентов целиком, без тейлеров.
// Порядок: сначала главный поток, затем субагенты по именам файлов —
// детерминированно от запуска к запуску.
//
// Второй результат — сколько строк отброшено по длине: молчать о потере
// нельзя, а ронять из-за неё всю сессию тем более.
func ReadAll(t Target) ([]Line, int, error) {
	out, skipped, err := readFile(nil, t.Path, SourceMain, "")
	if err != nil {
		return nil, skipped, err
	}

	entries, err := os.ReadDir(t.SubagentDir)
	if err != nil {
		return out, skipped, nil // каталога субагентов нет — сессия без делегирований
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), agentPrefix) &&
			strings.HasSuffix(e.Name(), transcriptExt) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(t.SubagentDir, name)

		agentType := ""
		if meta, err := ReadMeta(path); err == nil {
			agentType = meta.AgentType
		}
		var n int
		if out, n, err = readFile(out, path, shortAgentID(path), agentType); err != nil {
			// Файл мог исчезнуть между ReadDir и открытием — нормальная гонка.
			if os.IsNotExist(err) {
				continue
			}
			return nil, skipped, err
		}
		skipped += n
	}
	return out, skipped, nil
}

func readFile(out []Line, path, id, name string) ([]Line, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return out, 0, err
	}
	defer func() { _ = f.Close() }()

	skipped, err := parse.Scan(f, func(line []byte) error {
		data := make([]byte, len(line))
		copy(data, line)
		out = append(out, Line{Data: data, ID: id, Name: name, Path: path})
		return nil
	})
	return out, skipped, err
}

// shortAgentID — короткое имя источника по пути транскрипта субагента.
func shortAgentID(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), transcriptExt)

	id, found := strings.CutPrefix(base, agentPrefix)
	if !found {
		return base
	}
	if len(id) > shortIDLen {
		id = id[:shortIDLen]
	}
	return agentPrefix + id
}
