// Package session находит транскрипт активной сессии Claude Code по каталогу
// проекта и разрешает режим работы: живая сессия или завершённый транскрипт.
package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// transcriptExt — расширение файлов транскриптов.
const transcriptExt = ".jsonl"

// maxHintDepth — сколько родительских каталогов просматривать ради подсказки.
const maxHintDepth = 8

// Mode — режим работы над найденным транскриптом.
type Mode int

const (
	// ModeLive — активная сессия: файл дописывается, нужен тейлинг (S4/S5).
	ModeLive Mode = iota + 1
	// ModeArchive — завершённый транскрипт: читается целиком, тейлер не нужен.
	ModeArchive
)

// String возвращает короткое имя режима.
func (m Mode) String() string {
	switch m {
	case ModeLive:
		return "live"
	case ModeArchive:
		return "archive"
	default:
		return "unknown"
	}
}

// Target — найденная сессия.
type Target struct {
	Path        string // абсолютный путь к .jsonl главного потока
	SessionID   string // идентификатор сессии, он же имя файла без расширения
	SubagentDir string // <slug>/<session-id>/subagents — источник потоков субагентов
	Project     string // абсолютный путь каталога проекта
	Slug        string // имя каталога в ~/.claude/projects
	Mode        Mode
	// Newest — этот файл и есть самый свежий транскрипт проекта. Именно
	// «самый свежий», а не «живой»: живость без порога свежести не доказать,
	// а любой порог (пять минут? час?) — выдумка, дрожащая на границе.
	// Признак нужен, чтобы указавший --session на свой же текущий транскрипт
	// не остался без тейлинга молча.
	Newest bool
}

// Options — что искать.
type Options struct {
	Project     string // каталог проекта; пусто — текущий рабочий каталог
	Session     string // идентификатор сессии; пусто — самая свежая по mtime
	ProjectsDir string // корень хранилища; пусто — ~/.claude/projects
}

// Причины неудачи обнаружения.
var (
	ErrNoProjectDir    = errors.New("session directory for this project not found")
	ErrNoSessions      = errors.New("the project directory holds no transcripts")
	ErrSessionNotFound = errors.New("no session with this id")
	ErrNoHome          = errors.New("home directory is undefined")
	ErrBadSession      = errors.New("invalid session id")
)

// DiscoverError — неудача обнаружения с готовой подсказкой пользователю.
type DiscoverError struct {
	Err     error
	Project string
	Dir     string // ожидавшийся каталог хранилища, если речь о каталоге
	File    string // ожидавшийся файл транскрипта, если речь о файле
	Hint    string // готовая команда запуска, если нашёлся подходящий родитель
}

func (e *DiscoverError) Error() string {
	msg := fmt.Sprintf("%v: project %s", e.Err, e.Project)
	switch {
	case e.File != "":
		msg += fmt.Sprintf(" (file %s)", e.File)
	case e.Dir != "":
		msg += fmt.Sprintf(" (directory %s)", e.Dir)
	}
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	return msg
}

func (e *DiscoverError) Unwrap() error { return e.Err }

// Slugify превращает путь каталога в имя каталога хранилища транскриптов.
//
// Claude Code заменяет ЛЮБОЙ символ вне [A-Za-z0-9] на дефис, а не только "/",
// как написано в спеке (my_project → my-project, pipe.final →
// pipe-final, /home/user/.claude → -home-user--claude).
//
// Точность доказательства: в ~/.claude/projects лежат 32 каталога, но исходный
// путь восстановим только у 12 — у остальных в записях нет поля cwd. На этих
// 12 правило сходится один в один; остальные 20 ему не противоречат, но
// доказательством не являются.
//
// Все 12 путей — ASCII. Здесь выбрана замена ОДИН ДЕФИС НА РУНУ. Если Claude
// Code применяет replace(/[^a-zA-Z0-9]/g,'-') без флага /u, то суррогатная
// пара даст два дефиса, и путь с не-BMP символом (эмодзи) промахнётся мимо
// каталога. Кириллица и прочий BMP совпадают в обеих схемах. Живых каталогов
// с не-ASCII нет — проверить нечем, поведение зафиксировано тестом как выбор.
func Slugify(path string) string {
	path = filepath.Clean(path)

	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// validSessionID — идентификатор годится, только если он имя файла и ничем
// больше: ни разделителей пути, ни "." с "..".
func validSessionID(s string) bool {
	if s == "." || s == ".." {
		return false
	}
	return filepath.Base(s) == s
}

// ProjectsDir — путь к хранилищу транскриптов Claude Code.
func ProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", ErrNoHome
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// projectRoot разрешает каталог проекта: пусто — текущий каталог, "~" —
// домашний, относительный путь — от текущего каталога.
//
// Стратегия сознательно тупая: строго cwd, без подъёма к корню git. Решение
// принято осознанно; если понадобится другая — менять только здесь.
func projectRoot(project string) (string, error) {
	if project == "" {
		return os.Getwd()
	}
	if project == "~" || strings.HasPrefix(project, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", ErrNoHome
		}
		project = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(project, "~"), "/"))
	}

	abs, err := filepath.Abs(project)
	if err != nil {
		return "", err
	}
	// Claude Code пишет транскрипты по физическому пути, а cwd в shell часто
	// приходит через симлинк ($PWD). Slug от ссылки промахнётся мимо живой
	// сессии, поэтому путь разыменовываем; несуществующий каталог — не повод
	// падать, остаётся абсолютный путь как есть.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// Discover находит транскрипт по каталогу проекта и идентификатору сессии.
func Discover(opts Options) (Target, error) {
	root, err := projectRoot(opts.Project)
	if err != nil {
		return Target{}, err
	}

	store := opts.ProjectsDir
	if store == "" {
		if store, err = ProjectsDir(); err != nil {
			return Target{}, err
		}
	}

	// Идентификатор сессии подставляется в путь: он обязан быть именем файла
	// и ничем больше, иначе ".." уводит и Path, и SubagentDir за пределы
	// хранилища, а SubagentDir проверять уже некому.
	if opts.Session != "" && !validSessionID(opts.Session) {
		return Target{}, &DiscoverError{Err: ErrBadSession, Project: root}
	}

	slug := Slugify(root)
	dir := filepath.Join(store, slug)
	target := Target{Project: root, Slug: slug}

	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return Target{}, &DiscoverError{
			Err:     ErrNoProjectDir,
			Project: root,
			Dir:     dir,
			Hint:    hintForParents(store, root),
		}
	}

	if opts.Session != "" {
		path := filepath.Join(dir, opts.Session+transcriptExt)
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			return Target{}, &DiscoverError{
				Err:     ErrSessionNotFound,
				Project: root,
				File:    path,
			}
		}
		// Явно указанная сессия считается завершённой: тейлер не нужен.
		// Но если это самый свежий транскрипт, отмечаем — вызывающий обязан
		// предупредить пользователя, а не молча лишать его живого потока.
		newestPath, _ := newestTranscript(dir)
		return fill(target, dir, path, ModeArchive, newestPath == path), nil
	}

	path, err := newestTranscript(dir)
	if err != nil {
		return Target{}, &DiscoverError{Err: err, Project: root, Dir: dir}
	}
	return fill(target, dir, path, ModeLive, true), nil
}

func fill(t Target, dir, path string, mode Mode, newest bool) Target {
	t.Path = path
	t.SessionID = strings.TrimSuffix(filepath.Base(path), transcriptExt)
	t.SubagentDir = filepath.Join(dir, t.SessionID, "subagents")
	t.Mode = mode
	t.Newest = newest
	return t
}

// newestTranscript выбирает самый свежий по mtime .jsonl каталога.
//
// Пустые файлы не отбрасываются: только что созданная сессия ещё нулевого
// размера, но именно она и активна. Файл, исчезнувший между ReadDir и Stat, —
// нормальная гонка, а не ошибка: такие записи пропускаются. При равных mtime
// выбор детерминирован по имени, чтобы результат не скакал между запусками.
func newestTranscript(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Отказ в доступе — не то же самое, что «транскриптов нет»:
		// подменять причину значит врать пользователю.
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("no access to the project directory: %w", err)
		}
		return "", ErrNoSessions
	}

	type candidate struct {
		name  string
		mtime int64
	}
	var best *candidate

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), transcriptExt) {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil || !fi.Mode().IsRegular() {
			continue // гонка либо битая ссылка — молча пропускаем
		}

		cur := candidate{name: e.Name(), mtime: fi.ModTime().UnixNano()}
		if best == nil || cur.mtime > best.mtime ||
			(cur.mtime == best.mtime && cur.name > best.name) {
			c := cur
			best = &c
		}
	}

	if best == nil {
		return "", ErrNoSessions
	}
	return filepath.Join(dir, best.name), nil
}

// hintForParents ищет ближайшего родителя, у которого сессии есть, и собирает
// готовую команду запуска. Пустая строка — подсказки нет; выдумывать нельзя.
func hintForParents(store, root string) string {
	dir := root
	for range maxHintDepth {
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent

		if _, err := newestTranscript(filepath.Join(store, Slugify(dir))); err == nil {
			return fmt.Sprintf("the session seems to live in %s — run: ccon --project %s", dir, dir)
		}
	}
	return ""
}
