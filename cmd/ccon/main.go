// Команда ccon — наблюдатель за сессией Claude Code.
// Пока без TUI: режим --dump по транскрипту, с тейлингом по флагу --follow.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/KratosUAE/ccon/internal/cost"
	"github.com/KratosUAE/ccon/internal/parse"
	"github.com/KratosUAE/ccon/internal/session"
	"github.com/KratosUAE/ccon/internal/ui"
)

// Коды возврата: 2 — ошибка вызова, 1 — ошибка работы.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// isSet — задавал ли пользователь флаг явно. Значение по умолчанию от
// заданного не отличить иначе, а «--view вместе с --dump» обязано ругаться
// только на явную просьбу.
func isSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func main() {
	// Ctrl+C и SIGTERM останавливают тейлинг штатно: сводка успевает
	// напечататься, горутины успевают закрыться.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run — тело команды, вынесено ради тестируемости: возвращает код возврата.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ccon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "ccon [--version] [--list] [--dump] [--follow] [--view transcript|mcp|files] [--project <dir>] [--session <id>] [path to .jsonl]")
		_, _ = fmt.Fprintln(stderr, "Event order: live mode (--follow) emits them as they arrive,")
		_, _ = fmt.Fprintln(stderr, "archive parsing sorts them by record time.")
		fs.PrintDefaults()
	}
	showVersion := fs.Bool("version", false, "print version and exit")
	list := fs.Bool("list", false, "pick a session from a list of all projects")
	dump := fs.Bool("dump", false, "print transcript events to stdout without the TUI")
	follow := fs.Bool("follow", false, "follow the transcript as it grows (only with --dump)")
	// Имена видов берутся у самого интерфейса: список в двух местах разъедется
	// на четвёртом табе, и флаг начнёт открывать не то.
	view := fs.String("view", ui.ViewTranscript.String(), "start on this tab: "+strings.Join(ui.ViewNames(), "|"))
	project := fs.String("project", "", "project directory instead of the current one")
	sessionID := fs.String("session", "", "session id instead of the newest one")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	// Версия печатается раньше любых проверок: это первое, что запускает
	// человек, у которого что-то не работает, и оно обязано отвечать без
	// терминала, без сессии и без ~/.claude.
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, versionString())
		return exitOK
	}

	startView, known := ui.ParseView(*view)
	if !known {
		_, _ = fmt.Fprintf(stderr, "ccon: unknown --view %q; pick one of: %s\n",
			*view, strings.Join(ui.ViewNames(), ", "))
		return exitUsage
	}
	// У текстового вывода табов нет, и молча игнорировать флаг нельзя: человек
	// просил другой вид и не получит ни его, ни объяснения.
	if *dump && isSet(fs, "view") {
		_, _ = fmt.Fprintln(stderr, "ccon: --view picks a TUI tab and does nothing with --dump")
		return exitUsage
	}

	if *list {
		if len(fs.Args()) > 0 || *sessionID != "" {
			_, _ = fmt.Fprintln(stderr, "ccon: --list picks the session itself; a path or --session cannot be combined with it")
			return exitUsage
		}
		return runList(ctx, stdout, stderr, *dump, *follow, startView)
	}

	rest := fs.Args()
	if len(rest) > 1 {
		_, _ = fmt.Fprintln(stderr, "ccon: give at most one path to a .jsonl transcript")
		return exitUsage
	}
	if len(rest) == 1 && (*project != "" || *sessionID != "") {
		_, _ = fmt.Fprintln(stderr, "ccon: a transcript path and the --project/--session flags are mutually exclusive")
		return exitUsage
	}

	if *follow && !*dump {
		_, _ = fmt.Fprintln(stderr, "ccon: --follow works only together with --dump")
		return exitUsage
	}

	root, err := os.Getwd()
	if err != nil {
		// Сломанный текущий каталог — сбой среды, а не ошибка вызова.
		_, _ = fmt.Fprintf(stderr, "ccon: cannot determine the current directory: %v\n", err)
		return exitFail
	}

	path, target, err := locate(root, rest, *project, *sessionID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ccon: %v\n", err)
		return exitUsage
	}

	// Спека велит считать явно указанную сессию архивной. Если она при этом
	// самая свежая, пользователь, скорее всего, показал на свою же живую
	// сессию — сказать об этом дешевле, чем оставить его без потока молча.
	if target.Mode == session.ModeArchive && target.Newest && !*follow && *sessionID != "" {
		_, _ = fmt.Fprintln(stderr, "ccon: this is the newest transcript of the project; add --follow for a live stream")
	}

	if !*dump {
		// Без терминала bubbletea вываливает свою внутреннюю ошибку про
		// /dev/tty; пользователю нужен совет, а не потроха библиотеки.
		if !isTerminal(stdout) {
			_, _ = fmt.Fprintln(stderr, "ccon: stdout is not a terminal; use --dump")
			return exitUsage
		}
		// TUI по завершённой сессии. Живой режим — слайс S8.
		if err := showTUI(ctx, path, target, startView); err != nil {
			_, _ = fmt.Fprintf(stderr, "ccon: %v\n", err)
			return exitFail
		}
		return exitOK
	}

	// Позиционный путь показывает ровно один файл — это осознанный контракт.
	// Но молчать о соседних потоках нельзя: ради них инструмент и затевался.
	if len(rest) == 1 {
		if n := siblingSubagents(path); n > 0 {
			_, _ = fmt.Fprintf(stderr, "ccon: found %d subagent stream(s) alongside; use --session %s to merge them\n",
				n, strings.TrimSuffix(filepath.Base(path), ".jsonl"))
		}
	}

	source := target.Mode.String()
	if *follow {
		source += ", tailing"
	}
	_, _ = fmt.Fprintf(stderr, "ccon: source %s (%s)\n", path, source)

	// Путь выбора: живое наблюдение за всеми потоками сессии, разбор
	// завершённой сессии с делегированиями либо одиночный файл как есть.
	switch {
	case *follow:
		err = followSession(ctx, target, stdout, stderr)
	case target.SubagentDir != "":
		err = dumpSession(target, stdout)
	default:
		err = dumpFile(path, stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ccon: %v\n", err)
		return exitFail
	}
	return exitOK
}

// locate разрешает, что открывать: позиционный путь — архив как есть,
// иначе обнаружение сессии по каталогу проекта.
func locate(root string, rest []string, project, sessionID string) (string, session.Target, error) {
	if len(rest) == 1 {
		path, err := resolvePath(root, rest[0])
		return path, session.Target{Path: path, Mode: session.ModeArchive}, err
	}

	target, err := session.Discover(session.Options{Project: project, Session: sessionID})
	if err != nil {
		return "", session.Target{}, err
	}
	path, err := resolvePath(root, target.Path)
	return path, target, err
}

// siblingSubagents считает потоки субагентов рядом с указанным транскриптом.
func siblingSubagents(path string) int {
	dir := strings.TrimSuffix(path, ".jsonl")
	entries, err := os.ReadDir(filepath.Join(dir, "subagents"))
	if err != nil {
		return 0
	}

	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "agent-") && strings.HasSuffix(e.Name(), ".jsonl") {
			n++
		}
	}
	return n
}

// resolvePath проверяет, что путь ведёт к обычному файлу внутри разрешённой
// области: рабочий каталог с подкаталогами плюс хранилище транскриптов
// ~/.claude/projects. Симлинки разыменовываются до проверки, поэтому ссылкой
// за пределы области не выйти.
func resolvePath(root, arg string) (string, error) {
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(root, arg)
	}
	path, err := filepath.EvalSymlinks(arg)
	if err != nil {
		return "", fmt.Errorf("path %s is unreachable: %w", arg, err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	// Нерегулярный файл (FIFO, устройство) подвесит чтение или раздует память.
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}

	if !withinAllowed(root, path) {
		return "", fmt.Errorf("%s is outside the working directory and outside ~/.claude/projects", path)
	}
	return path, nil
}

// withinAllowed — путь внутри рабочего каталога либо внутри хранилища
// транскриптов Claude Code. Второе исключение обязательно: живой режим
// читает именно оттуда, а этот каталог всегда лежит вне проекта.
func withinAllowed(root, path string) bool {
	if evaluated, err := filepath.EvalSymlinks(root); err == nil {
		root = evaluated
	}
	if under(root, path) {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	projects := filepath.Join(home, ".claude", "projects")
	if evaluated, err := filepath.EvalSymlinks(projects); err == nil {
		projects = evaluated
	}
	return under(projects, path)
}

func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// stats — счётчики прохода по файлу.
type stats struct {
	lines   int
	skipped int
	events  int
}

// printer превращает строки транскрипта в поток событий и копит расход.
// Один и тот же путь используют разовый дамп и тейлинг.
type printer struct {
	out      *bufio.Writer
	acc      *cost.Accumulator
	lastTime map[string]time.Time
	st       stats

	// hold != nil — события копятся, чтобы выйти в лог в хронологическом
	// порядке: у завершённой сессии файлы читаются по одному, и без
	// пересортировки поток субагента шёл бы одним куском после главного.
	hold []held

	// results копятся под тем же условием, что и hold: собрать их можно только
	// там, где события копятся целиком. Текстовому дампу исходы вызовов не
	// показываются — он их просто не читает, и формат остаётся прежним.
	results []parse.Result
}

// held — событие вместе с файлом-источником: без источника нечем чинить
// записи с испорченной меткой времени.
type held struct {
	ev  parse.Event
	key string
}

// Ошибки печати намеренно не проверяются поштучно. У буферизованного
// писателя первая ошибка запоминается внутри, последующие записи становятся
// пустышками, и всё всплывает на Flush — он обработан через errors.Join в
// dumpFile. Диагностика в stderr игнорируется по другой причине: если не
// удалось напечатать сообщение об ошибке, писать вторую бессмысленно.
func newPrinter(w io.Writer) *printer {
	return &printer{out: bufio.NewWriter(w), acc: cost.NewAccumulator(), lastTime: map[string]time.Time{}}
}

// feed обрабатывает одну строку транскрипта.
func (p *printer) feed(line []byte) error { return p.feedFrom(line, nil, "") }

// feedFrom обрабатывает строку, позволяя источнику переопределить подпись:
// имя субагента известно наблюдателю, а не самой записи. key — файл-источник,
// он нужен только режиму с пересортировкой.
func (p *printer) feedFrom(line []byte, srcOf func(string) string, key string) error {
	p.st.lines++

	d, ok := parse.Decode(line)
	if !ok {
		p.st.skipped++
		return nil
	}
	for _, ev := range d.Events {
		if srcOf != nil {
			ev.Source = srcOf(ev.Source)
		}
		// Битая метка времени не должна показываться как 00:00:00: берём
		// время предыдущей записи того же файла. Делается в обоих режимах,
		// иначе живой и архивный вывод расходятся на подпорченных данных.
		if ev.Time.IsZero() {
			ev.Time = p.lastTime[key]
		} else {
			p.lastTime[key] = ev.Time
		}
		if p.hold != nil {
			p.hold = append(p.hold, held{ev: ev, key: key})
		} else {
			_, _ = fmt.Fprintln(p.out, parse.Line(ev))
		}
		p.st.events++
	}
	if p.hold != nil {
		// Порядок результатов не значим: интерфейс сшивает их после того, как
		// поставил на учёт все события, поэтому сортировать их не нужно.
		p.results = append(p.results, d.Results...)
	}
	if d.Usage != nil {
		p.acc.Add(*d.Usage)
	}
	return nil
}

// releaseHeld печатает накопленные события по времени. Сортировка
// устойчивая: события с одинаковой отметкой сохраняют порядок чтения.
func (p *printer) releaseHeld() {
	p.fillMissingTimes()

	sort.SliceStable(p.hold, func(i, j int) bool {
		return p.hold[i].ev.Time.Before(p.hold[j].ev.Time)
	})
	for _, h := range p.hold {
		_, _ = fmt.Fprintln(p.out, parse.Line(h.ev))
	}
	p.hold = nil
}

// fillMissingTimes подставляет записям с битой или отсутствующей меткой
// времени время соседа по тому же файлу. Иначе нулевое время выносит их в
// самое начало лога как 00:00:00, и архивный режим расходится с живым ровно
// там, где данные подпорчены.
func (p *printer) fillMissingTimes() {
	prev := make(map[string]time.Time)
	for i := range p.hold {
		h := &p.hold[i]
		if h.ev.Time.IsZero() {
			h.ev.Time = prev[h.key]
		} else {
			prev[h.key] = h.ev.Time
		}
	}

	// Битая запись в самом начале файла соседа сверху не имеет — берём снизу.
	next := make(map[string]time.Time)
	for i := len(p.hold) - 1; i >= 0; i-- {
		h := &p.hold[i]
		if h.ev.Time.IsZero() {
			h.ev.Time = next[h.key]
		} else {
			next[h.key] = h.ev.Time
		}
	}
}

func (p *printer) summary() { writeSummary(p.out, p.acc.Totals(), p.st) }

func (p *printer) flush() error { return p.out.Flush() }

// dumpFile читает транскрипт целиком, печатает поток событий и сводку расхода.
// Ошибка сброса буфера (закрытый pipe, полный диск) обязана дойти до вызова:
// иначе сводка теряется, а код возврата остаётся нулевым.
func dumpFile(path string, w io.Writer) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	// Файл открыт на чтение: данные уже получены, и ошибка закрытия на их
	// корректность не влияет — игнорируем сознательно.
	defer func() { _ = f.Close() }()

	p := newPrinter(w)
	defer func() {
		err = errors.Join(err, p.flush())
	}()

	skipped, err := parse.Scan(f, p.feed)
	if err != nil {
		return err
	}
	// Строка, отброшенная по длине, — такая же потеря, как неразобранная:
	// в сводке она обязана быть видна, а не исчезать молча.
	p.st.skipped += skipped

	p.summary()
	return nil
}

// isTerminal — можно ли рисовать TUI в этот вывод.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// showTUI открывает интерфейс: живая сессия наблюдается, завершённая
// читается один раз. view — таб, на котором интерфейс открывается.
func showTUI(ctx context.Context, path string, target session.Target, view ui.View) error {
	if target.Mode == session.ModeLive {
		feed, cancel := liveFeed(ctx, target)
		defer cancel()

		return ui.Run(ui.Options{
			Project: projectName(path, target, nil),
			Mode:    ui.ModeLive,
			Feed:    feed,
			View:    view,
		})
	}

	opts, err := loadArchive(path, target)
	if err != nil {
		return err
	}
	opts.View = view
	return ui.Run(opts)
}

// batchInterval — как часто копившиеся события уходят в интерфейс. Батч
// стоит десятки миллисекунд против секунды на тысяче событий поштучно.
const batchInterval = 100 * time.Millisecond

// send отдаёт порцию интерфейсу, не подвисая навсегда, если тот занят.
func send(ctx context.Context, out chan<- ui.Batch, batch ui.Batch) bool {
	select {
	case out <- batch:
		return true
	case <-ctx.Done():
		return false
	}
}

// liveFeed поднимает наблюдение за сессией и превращает строки транскрипта в
// порции готовых событий: разбор и подсчёт расхода остаются здесь, интерфейс
// получает готовое.
func liveFeed(ctx context.Context, target session.Target) (*ui.Feed, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	w := session.Watch(ctx, target)

	batches := make(chan ui.Batch)
	go func() {
		defer close(batches)

		acc := cost.NewAccumulator()
		counts := map[string]int{}
		var pending []parse.Event
		// Результаты копятся рядом с событиями и уходят той же порцией: во
		// время догона наружу не идёт ничего, значит и они ждут его конца.
		var pendingResults []parse.Result
		skipped := 0

		// Догон и слежение — фазы разной природы. Пока идёт догон, объём
		// конечен и время всех событий известно: их можно упорядочить, как в
		// архиве. После догона сортировать нечего — события приходят по
		// одному, и ожидание ради порядка убило бы смысл живого режима.
		//
		// Без этого разделения короткий свежий файл субагента дочитывается
		// мгновенно, длинный главный ещё догоняется, и метки времени в логе
		// скачут назад на возраст сессии.
		catching := true
		settling := false
		caught := w.CaughtUp()

		ticker := time.NewTicker(batchInterval)
		defer ticker.Stop()

		flush := func() bool {
			if len(pending) == 0 && len(pendingResults) == 0 {
				return true
			}
			if catching {
				// Во время догона наружу уходит только ход работы: сами
				// события ждут сортировки.
				return send(ctx, batches, ui.Batch{
					Status: fmt.Sprintf("catching up: %d events", len(pending)),
				})
			}
			totals := acc.Totals()
			model := ""
			if len(totals.Models) > 0 {
				// Модели отсортированы по числу запросов: берём преобладающую,
				// иначе шапка мигает на сессии с чередованием моделей.
				model = totals.Models[0].Model
			}
			batch := ui.Batch{Events: pending, Results: pendingResults, Totals: totals,
				Agents: sortedAgents(counts), Model: model, Skipped: skipped}
			if !send(ctx, batches, batch) {
				return false
			}
			pending, pendingResults = nil, nil
			return true
		}

		for {
			select {
			case line, ok := <-w.Lines():
				if !ok {
					flush()
					return
				}
				d, valid := parse.Decode(line.Data)
				if !valid {
					// Молчать о неразобранных строках нельзя: при смене
					// формата лог опустеет без единого объяснения.
					skipped++
					continue
				}
				for _, ev := range d.Events {
					ev.Source = line.Source(ev.Source)
					counts[ev.Source]++
					pending = append(pending, ev)
				}
				// Источник результатам НЕ назначается: у записи с tool_result
				// его нет вовсе, и подстановка соврала бы про «кем».
				pendingResults = append(pendingResults, d.Results...)
				if d.Usage != nil {
					acc.Add(*d.Usage)
				}

			case <-caught:
				// Догон закончен. Сортировку откладываем на ближайший такт:
				// сигнал поднимается, когда тейлеры дочитали файлы, но по
				// строке на файл может ещё лететь между горутинами, и сортировка
				// прямо сейчас оставила бы этот хвост за бортом порядка.
				caught, settling = nil, true

			case <-ticker.C:
				if settling {
					settling, catching = false, false
					sort.SliceStable(pending, func(i, j int) bool {
						return pending[i].Time.Before(pending[j].Time)
					})
				}
				if !flush() {
					return
				}

			case <-ctx.Done():
				return
			}
		}
	}()

	return &ui.Feed{
		Batches: batches,
		Renames: w.Renames(),
		Errs:    w.Errs(),
		Names:   w.Names,
		Cancel:  cancel,
	}, cancel
}

// loadArchive читает сессию целиком и готовит данные для TUI: события,
// агрегат расхода и счётчики источников.
func loadArchive(path string, target session.Target) (ui.Options, error) {
	var lines []session.Line
	skipped := 0
	if target.SubagentDir != "" {
		var err error
		if lines, skipped, err = session.ReadAll(target); err != nil {
			return ui.Options{}, err
		}
	} else {
		f, err := os.Open(path)
		if err != nil {
			return ui.Options{}, err
		}
		defer func() { _ = f.Close() }()

		skipped, err = parse.Scan(f, func(line []byte) error {
			data := make([]byte, len(line))
			copy(data, line)
			lines = append(lines, session.Line{Data: data, ID: session.SourceMain, Path: path})
			return nil
		})
		if err != nil {
			return ui.Options{}, err
		}
	}

	p := newPrinter(io.Discard)
	p.hold = make([]held, 0, len(lines))
	for _, line := range lines {
		if err := p.feedFrom(line.Data, line.Source, line.Path); err != nil {
			return ui.Options{}, err
		}
	}
	p.fillMissingTimes()
	sort.SliceStable(p.hold, func(i, j int) bool { return p.hold[i].ev.Time.Before(p.hold[j].ev.Time) })

	events := make([]parse.Event, 0, len(p.hold))
	counts := map[string]int{}
	var model, effort string
	for _, h := range p.hold {
		events = append(events, h.ev)
		counts[h.ev.Source]++
		if h.ev.Model != "" {
			model = h.ev.Model
		}
		if h.ev.Effort != "" {
			effort = h.ev.Effort
		}
	}

	return ui.Options{
		Project: projectName(path, target, lines),
		Model:   model,
		Effort:  effort,
		Mode:    target.Mode.String(),
		Events:  events,
		Results: p.results,
		Totals:  p.acc.Totals(),
		Agents:  sortedAgents(counts),
		Skipped: skipped + p.st.skipped,
	}, nil
}

// projectName выбирает, что показать в шапке. Точка вместо имени —
// недопустимый исход: у позиционного пути Target.Project пуст, а родительский
// каталог назван слагом и нечитаем, поэтому имя берётся из cwd самой записи.
func projectName(path string, target session.Target, lines []session.Line) string {
	if target.Project != "" {
		return filepath.Base(target.Project)
	}
	for _, line := range lines {
		if cwd := parse.RecordCwd(line.Data); cwd != "" {
			return filepath.Base(cwd)
		}
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// sortedAgents выстраивает источники по убыванию активности.
func sortedAgents(counts map[string]int) []ui.Agent {
	out := make([]ui.Agent, 0, len(counts))
	for name, n := range counts {
		out = append(out, ui.Agent{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// dumpSession разбирает завершённую сессию целиком: главный поток и все
// потоки субагентов, событиями в хронологическом порядке.
func dumpSession(t session.Target, w io.Writer) (err error) {
	lines, skipped, err := session.ReadAll(t)
	if err != nil {
		return err
	}

	p := newPrinter(w)
	p.st.skipped += skipped
	p.hold = make([]held, 0, len(lines))
	defer func() {
		err = errors.Join(err, p.flush())
	}()

	for _, line := range lines {
		if err := p.feedFrom(line.Data, line.Source, line.Path); err != nil {
			return err
		}
	}

	p.releaseHeld()
	p.summary()
	return nil
}

// followSession печатает события всех потоков сессии по мере появления и
// подводит итог при остановке. Вывод сбрасывается после каждой строки: смысл
// режима в том, чтобы она была видна сразу, а не когда наберётся буфер.
//
// Порядок — порядок прихода: пока главный поток молчит, идут строки
// субагентов, и ждать ради сортировки нечего и незачем.
func followSession(ctx context.Context, t session.Target, w, stderr io.Writer) (err error) {
	p := newPrinter(w)
	defer func() {
		err = errors.Join(err, p.flush())
	}()

	watcher := session.Watch(ctx, t)
	lines, errs := watcher.Lines(), watcher.Errs()

	for lines != nil {
		select {
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			if err := p.feedFrom(line.Data, line.Source, line.Path); err != nil {
				return err
			}
			if err := p.flush(); err != nil {
				return err
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil // закрытый канал иначе крутил бы select вхолостую
				continue
			}
			if err != nil {
				// Сбой тейлера — повод сказать, а не повод умереть.
				_, _ = fmt.Fprintf(stderr, "ccon: tailer: %v\n", err)
			}
		}
	}

	// Последняя ошибка могла остаться в буфере канала: при закрытии обоих
	// каналов select мог выбрать lines и о ней бы никто не узнал.
	for {
		select {
		case err, ok := <-errs:
			if ok && err != nil {
				_, _ = fmt.Fprintf(stderr, "ccon: tailer: %v\n", err)
				continue
			}
		default:
		}
		break
	}

	p.summary()
	return nil
}

func writeSummary(w io.Writer, t cost.Totals, st stats) {
	_, _ = fmt.Fprintln(w, "──────────────────────────────────────────────────────────────────────")

	models := "(none)"
	if len(t.Models) > 0 {
		parts := make([]string, 0, len(t.Models))
		for _, m := range t.Models {
			parts = append(parts, fmt.Sprintf("%s ×%d", m.Model, m.Count))
		}
		models = strings.Join(parts, "   ")
	}
	if t.Unknown {
		models += "   (unknown model priced at the opus rate)"
	}
	_, _ = fmt.Fprintf(w, "MODELS    %s\n", models)

	_, _ = fmt.Fprintf(w, "TOKENS    in %d · out %d · cache read %d · cache write %d\n",
		t.Input, t.Output, t.CacheRead, t.CacheCreate())
	_, _ = fmt.Fprintf(w, "          write: 5m %d · 1h %d\n", t.Cache5m, t.Cache1h)
	_, _ = fmt.Fprintf(w, "COST      $%.2f%s\n", t.CostUSD, ui.PriceNote(t))
	// Веб-поиск оплачивается поштучно; строку показываем, только если он был.
	if t.WebSearch > 0 {
		_, _ = fmt.Fprintf(w, "WEBSEARCH %d queries · $%.2f (included in COST)\n", t.WebSearch, t.WebSearchUSD)
	}
	_, _ = fmt.Fprintf(w, "TOTAL     events %d · requests %d · lines %d · skipped %d\n",
		st.events, t.Requests, st.lines, st.skipped)
}
