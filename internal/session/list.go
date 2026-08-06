package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// tailStart — сколько байт хвоста читается за первый заход. Заголовок
	// сессии переписывается каждый ход, поэтому свежая копия почти всегда
	// лежит в самом конце файла.
	tailStart = 128 << 10

	// tailMax — предел углубления в файл. Транскрипт бывает в сотни мегабайт,
	// и вычитывать его целиком ради строки заголовка недопустимо: список
	// должен открываться мгновенно. Не нашлось за этот объём — покажем
	// прочерк, но не заставим ждать.
	tailMax = 4 << 20

	// maxDesc — предел длины заголовка и реплики. Обрезка происходит здесь, а
	// не при отрисовке: незачем таскать килобайты ради одной видимой строки.
	maxDesc = 300
)

// Entry — одна сессия в списке.
type Entry struct {
	Path      string    // абсолютный путь к транскрипту
	SessionID string    // идентификатор, он же имя файла без расширения
	Slug      string    // каталог в ~/.claude/projects
	Cwd       string    // рабочий каталог сессии; пусто — в записях его нет
	Project   string    // человекочитаемое имя проекта
	Title     string    // заголовок сессии; пусто — его в транскрипте нет
	Prompt    string    // последняя реплика пользователя; пусто — нет записи
	Modified  time.Time // время последней дозаписи файла
	Size      int64
	// Newest — самый свежий транскрипт своего проекта, то есть ровно тот,
	// который выбрал бы Discover без --session. Признак объясняет выбор по
	// умолчанию и не выдумывает порога «живости».
	Newest bool
}

// Group — сессии одного проекта.
type Group struct {
	Slug    string
	Project string
	Entries []Entry
}

// ListOptions — что перечислять.
type ListOptions struct {
	// ProjectsDir — корень хранилища; пусто — ~/.claude/projects.
	ProjectsDir string
	// Slug — ограничить одним проектом; пусто — все проекты хранилища.
	Slug string
}

// List перечисляет сессии, сгруппированные по проектам.
//
// Порядок групп и сессий внутри — по убыванию свежести: сверху то, с чем
// работали только что. Ошибка возвращается только если недоступно само
// хранилище; нечитаемый отдельный проект пропускается, потому что ронять
// список целиком из-за одного каталога хуже, чем показать остальные.
func List(o ListOptions) ([]Group, error) {
	root := o.ProjectsDir
	if root == "" {
		var err error
		if root, err = ProjectsDir(); err != nil {
			return nil, err
		}
	}

	slugs, err := listSlugs(root, o.Slug)
	if err != nil {
		return nil, err
	}

	groups := make([]Group, 0, len(slugs))
	for _, slug := range slugs {
		entries := readGroup(filepath.Join(root, slug), slug)
		if len(entries) == 0 {
			continue
		}
		groups = append(groups, Group{Slug: slug, Project: entries[0].Project, Entries: entries})
	}

	// Группы упорядочены по своей самой свежей сессии: проект, в котором
	// работали минуту назад, обязан быть первым.
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].Entries[0].Modified.After(groups[j].Entries[0].Modified)
	})
	return groups, nil
}

func listSlugs(root, only string) ([]string, error) {
	if only != "" {
		if fi, err := os.Stat(filepath.Join(root, only)); err != nil || !fi.IsDir() {
			return nil, ErrNoProjectDir
		}
		return []string{only}, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, ErrNoProjectDir
	}

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// readGroup собирает сессии одного проекта, самые свежие первыми.
func readGroup(dir, slug string) []Entry {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil // проект без доступа пропускаем, но список не роняем
	}

	out := make([]Entry, 0, len(files))
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), transcriptExt) {
			continue
		}
		path := filepath.Join(dir, f.Name())
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue // гонка либо битая ссылка
		}

		e := Entry{
			Path:      path,
			SessionID: strings.TrimSuffix(f.Name(), transcriptExt),
			Slug:      slug,
			Modified:  fi.ModTime(),
			Size:      fi.Size(),
		}
		e.Title, e.Prompt, e.Cwd = describe(path, fi.Size())
		if e.Cwd != "" {
			e.Project = filepath.Base(e.Cwd)
		} else {
			e.Project = projectFromSlug(slug)
		}
		out = append(out, e)
	}

	if len(out) == 0 {
		return nil
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Modified.Equal(out[j].Modified) {
			return out[i].Modified.After(out[j].Modified)
		}
		// Ничья по времени разрешается именем — тем же правилом, что и в
		// Discover, иначе список и выбор по умолчанию разъедутся.
		return out[i].SessionID > out[j].SessionID
	})
	out[0].Newest = true
	return out
}

// TargetFor собирает цель по выбранной из списка записи, минуя Discover.
//
// Discover здесь не годится: он идёт от каталога проекта, а тот мог быть
// переименован или удалён — транскрипт при этом на месте и прекрасно
// читается. Режим выбирается тем же правилом, что и у Discover: самая свежая
// сессия проекта тейлится, остальные читаются как архив.
func TargetFor(e Entry) Target {
	mode := ModeArchive
	if e.Newest {
		mode = ModeLive
	}
	t := Target{Project: e.Cwd, Slug: e.Slug}
	return fill(t, filepath.Dir(e.Path), e.Path, mode, e.Newest)
}

// descRecord — те поля, ради которых стоит трогать транскрипт в списке.
type descRecord struct {
	Type       string `json:"type"`
	AITitle    string `json:"aiTitle"`
	LastPrompt string `json:"lastPrompt"`
	Cwd        string `json:"cwd"`
}

// Маркеры для дешёвой отсечки: разбирать JSON у каждой строки хвоста дорого,
// а строки транскрипта бывают многомегабайтными.
var (
	markTitle  = []byte(`"ai-title"`)
	markPrompt = []byte(`"last-prompt"`)
	markCwd    = []byte(`"cwd"`)
)

// describe достаёт из хвоста файла заголовок сессии, последнюю реплику и
// рабочий каталог. Читается именно хвост: заголовок переписывается каждый
// ход, и свежая копия лежит в конце. Пустая строка означает «в файле этого
// нет» — выдумывать замену нечем.
func describe(path string, size int64) (title, prompt, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	// Чтение: содержимое уже в буфере, ошибка закрытия на него не влияет.
	defer func() { _ = f.Close() }()

	for window := int64(tailStart); ; window *= 4 {
		window = min(window, tailMax)
		off := max(size-window, 0)

		buf := make([]byte, size-off)
		if _, err := f.ReadAt(buf, off); err != nil && len(buf) == 0 {
			return "", "", ""
		}
		// Первая строка куска почти наверняка обрублена посередине: начинаем
		// с ближайшего перевода строки, иначе разбор наткнётся на огрызок.
		if off > 0 {
			if i := bytes.IndexByte(buf, '\n'); i >= 0 {
				buf = buf[i+1:]
			} else {
				buf = nil
			}
		}

		title, prompt, cwd = scanTail(buf)
		if title != "" || off == 0 || window >= tailMax {
			return title, prompt, cwd
		}
	}
}

// scanTail идёт по строкам куска от конца к началу и берёт первое найденное
// значение каждого поля — то есть последнее по времени записи.
func scanTail(buf []byte) (title, prompt, cwd string) {
	for len(buf) > 0 && (title == "" || prompt == "" || cwd == "") {
		var line []byte
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			line, buf = buf[i+1:], buf[:i]
		} else {
			line, buf = buf, nil
		}

		wantTitle := title == "" && bytes.Contains(line, markTitle)
		wantPrompt := prompt == "" && bytes.Contains(line, markPrompt)
		wantCwd := cwd == "" && bytes.Contains(line, markCwd)
		if !wantTitle && !wantPrompt && !wantCwd {
			continue
		}

		var rec descRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // битая строка в списке — не повод для отказа
		}
		switch {
		case rec.Type == "ai-title" && title == "":
			title = clean(rec.AITitle)
		case rec.Type == "last-prompt" && prompt == "":
			prompt = clean(rec.LastPrompt)
		}
		if cwd == "" && rec.Cwd != "" {
			cwd = rec.Cwd
		}
	}
	return title, prompt, cwd
}

// clean приводит текст к одной строке: перевод строки ломает раскладку
// списка, а многострочная реплика — обычное дело.
func clean(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxDesc {
		s = s[:maxDesc]
	}
	return strings.TrimSpace(s)
}

// projectFromSlug — запасное имя проекта, когда в транскрипте нет ни одной
// записи с cwd. Слаг однозначно не разворачивается: он заменил на дефис и
// разделители пути, и дефисы самого имени. Берём последний сегмент — он чаще
// всего и есть имя каталога.
func projectFromSlug(slug string) string {
	if i := strings.LastIndex(slug, "-"); i >= 0 && i+1 < len(slug) {
		return slug[i+1:]
	}
	return slug
}
