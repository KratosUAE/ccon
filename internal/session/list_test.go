package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSession кладёт транскрипт в подложное хранилище и задаёт ему mtime:
// порядок в списке строится на времени изменения, и полагаться на порядок
// создания файлов в тесте нельзя.
func writeSession(t *testing.T, root, slug, id, body string, mtime time.Time) string {
	t.Helper()

	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+transcriptExt)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListGroupsAndOrders(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)

	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"user","cwd":"/home/u/alpha"}`+"\n"+
			`{"type":"ai-title","aiTitle":"старая альфа"}`+"\n",
		base.Add(-48*time.Hour))
	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000002",
		`{"type":"user","cwd":"/home/u/alpha"}`+"\n"+
			`{"type":"ai-title","aiTitle":"свежая альфа"}`+"\n"+
			`{"type":"last-prompt","lastPrompt":"продолжаем"}`+"\n",
		base.Add(-time.Hour))
	writeSession(t, root, "-home-u-beta", "bbbbbbbb-0000-0000-0000-000000000001",
		`{"type":"user","cwd":"/home/u/beta"}`+"\n"+
			`{"type":"ai-title","aiTitle":"бета"}`+"\n",
		base.Add(-24*time.Hour))

	groups, err := List(ListOptions{ProjectsDir: root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("групп %d, ожидалось 2", len(groups))
	}

	// Первым идёт проект, в котором работали позже всех.
	if groups[0].Project != "alpha" {
		t.Errorf("первая группа %q, ожидалась alpha", groups[0].Project)
	}
	if groups[1].Project != "beta" {
		t.Errorf("вторая группа %q, ожидалась beta", groups[1].Project)
	}

	alpha := groups[0].Entries
	if len(alpha) != 2 {
		t.Fatalf("сессий в alpha %d, ожидалось 2", len(alpha))
	}
	if alpha[0].Title != "свежая альфа" {
		t.Errorf("первая сессия %q, ожидалась свежая альфа", alpha[0].Title)
	}
	if alpha[0].Prompt != "продолжаем" {
		t.Errorf("реплика %q, ожидалось «продолжаем»", alpha[0].Prompt)
	}
	if !alpha[0].Newest {
		t.Error("самая свежая сессия проекта не помечена Newest")
	}
	if alpha[1].Newest {
		t.Error("вторая по свежести сессия помечена Newest")
	}
	if alpha[0].Cwd != "/home/u/alpha" {
		t.Errorf("Cwd %q, ожидался /home/u/alpha", alpha[0].Cwd)
	}
}

// Заголовка в транскрипте может не быть. Подставлять вместо него обрывок
// реплики нельзя: строка заголовка обязана означать заголовок.
func TestListLeavesTitleEmptyWhenAbsent(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"user","cwd":"/home/u/alpha"}`+"\n"+
			`{"type":"last-prompt","lastPrompt":"привет"}`+"\n",
		time.Now())

	groups, err := List(ListOptions{ProjectsDir: root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	e := groups[0].Entries[0]
	if e.Title != "" {
		t.Errorf("Title %q, ожидалась пустая строка", e.Title)
	}
	if e.Prompt != "привет" {
		t.Errorf("Prompt %q, ожидалось «привет»", e.Prompt)
	}
}

// Заголовок дописывается каждый ход, но между ним и концом файла может лечь
// сотня килобайт вывода. Первое окно хвоста его не увидит — и функция обязана
// углубиться, а не соврать прочерком.
func TestListFindsTitleBeyondFirstWindow(t *testing.T) {
	root := t.TempDir()

	var b strings.Builder
	b.WriteString(`{"type":"user","cwd":"/home/u/alpha"}` + "\n")
	b.WriteString(`{"type":"ai-title","aiTitle":"глубоко зарытый"}` + "\n")
	for b.Len() < tailStart*2 {
		b.WriteString(`{"type":"assistant","text":"` + strings.Repeat("x", 4000) + `"}` + "\n")
	}
	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		b.String(), time.Now())

	groups, err := List(ListOptions{ProjectsDir: root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := groups[0].Entries[0].Title; got != "глубоко зарытый" {
		t.Errorf("Title %q, ожидался «глубоко зарытый» — углубление в файл не сработало", got)
	}
}

// Из нескольких заголовков берётся последний: сессию могли переименовать.
func TestListTakesLastTitle(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"ai-title","aiTitle":"первое имя"}`+"\n"+
			`{"type":"user","cwd":"/home/u/alpha"}`+"\n"+
			`{"type":"ai-title","aiTitle":"второе имя"}`+"\n",
		time.Now())

	groups, err := List(ListOptions{ProjectsDir: root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := groups[0].Entries[0].Title; got != "второе имя" {
		t.Errorf("Title %q, ожидалось «второе имя»", got)
	}
}

// Битая строка в хвосте — не повод остаться без описания.
func TestListSurvivesBrokenLine(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"ai-title","aiTitle":"живой заголовок"}`+"\n"+
			`{"type":"user","cwd":"/home/u/alpha"}`+"\n"+
			`{"type":"last-prompt","lastPrompt":`+"\n",
		time.Now())

	groups, err := List(ListOptions{ProjectsDir: root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	e := groups[0].Entries[0]
	if e.Title != "живой заголовок" {
		t.Errorf("Title %q, ожидался «живой заголовок»", e.Title)
	}
	if e.Prompt != "" {
		t.Errorf("Prompt %q, ожидалась пустая строка: запись битая", e.Prompt)
	}
}

// Многострочная реплика в списке обязана стать одной строкой.
func TestListFlattensMultilinePrompt(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"user","cwd":"/home/u/alpha"}`+"\n"+
			`{"type":"last-prompt","lastPrompt":"первая\nвторая\t третья"}`+"\n",
		time.Now())

	groups, err := List(ListOptions{ProjectsDir: root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := groups[0].Entries[0].Prompt; got != "первая вторая третья" {
		t.Errorf("Prompt %q, ожидалось «первая вторая третья»", got)
	}
}

// Каталог без cwd в записях: имя проекта берётся из слага, но не выдумывается.
func TestListFallsBackToSlugName(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "-home-u-gamma", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"ai-title","aiTitle":"без cwd"}`+"\n", time.Now())

	groups, err := List(ListOptions{ProjectsDir: root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if groups[0].Project != "gamma" {
		t.Errorf("Project %q, ожидалось gamma", groups[0].Project)
	}
	if groups[0].Entries[0].Cwd != "" {
		t.Errorf("Cwd %q, ожидалась пустая строка", groups[0].Entries[0].Cwd)
	}
}

// Пустой каталог проекта в список не попадает: строка без единой сессии
// сообщает не больше, чем её отсутствие.
func TestListSkipsEmptyProjects(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "-home-u-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"ai-title","aiTitle":"есть"}`+"\n", time.Now())

	groups, err := List(ListOptions{ProjectsDir: root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("групп %d, ожидалась 1", len(groups))
	}
}

func TestListOnlyOneSlug(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"ai-title","aiTitle":"альфа"}`+"\n", time.Now())
	writeSession(t, root, "-home-u-beta", "bbbbbbbb-0000-0000-0000-000000000001",
		`{"type":"ai-title","aiTitle":"бета"}`+"\n", time.Now())

	groups, err := List(ListOptions{ProjectsDir: root, Slug: "-home-u-beta"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(groups) != 1 || groups[0].Entries[0].Title != "бета" {
		t.Fatalf("получено %+v, ожидалась одна группа беты", groups)
	}
}

func TestListMissingStore(t *testing.T) {
	if _, err := List(ListOptions{ProjectsDir: filepath.Join(t.TempDir(), "нет")}); err == nil {
		t.Error("ошибки нет, ожидался отказ по отсутствию хранилища")
	}
}

// TargetFor обязан давать живой режим для самой свежей сессии и архивный для
// остальных — тем же правилом, что и Discover, иначе выбор из списка молча
// лишит пользователя тейлинга.
func TestTargetForMode(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t, root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001",
		`{"type":"ai-title","aiTitle":"альфа"}`+"\n", time.Now())

	live := TargetFor(Entry{Path: path, SessionID: "aaaaaaaa-0000-0000-0000-000000000001",
		Slug: "-home-u-alpha", Cwd: "/home/u/alpha", Newest: true})
	if live.Mode != ModeLive {
		t.Errorf("режим %v, ожидался live", live.Mode)
	}
	if live.Path != path {
		t.Errorf("Path %q, ожидался %q", live.Path, path)
	}
	want := filepath.Join(root, "-home-u-alpha", "aaaaaaaa-0000-0000-0000-000000000001", "subagents")
	if live.SubagentDir != want {
		t.Errorf("SubagentDir %q, ожидался %q", live.SubagentDir, want)
	}

	old := TargetFor(Entry{Path: path, Slug: "-home-u-alpha"})
	if old.Mode != ModeArchive {
		t.Errorf("режим %v, ожидался archive", old.Mode)
	}
}
