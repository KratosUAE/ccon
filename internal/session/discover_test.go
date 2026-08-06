package session

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Правило slug проверено по 32 живым каталогам ~/.claude/projects:
// заменяется ЛЮБОЙ символ вне [A-Za-z0-9], а не только "/" как в спеке.
// Иначе claude_con_ecc и pipe.final не находятся никогда.
func TestSlugify(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"обычный путь", "/home/user/Devs", "-home-user-Devs"},
		{"подчёркивания", "/home/user/Devs/claude_con_ecc", "-home-user-Devs-claude-con-ecc"},
		{"точки", "/home/user/Devs/pipe.final", "-home-user-Devs-pipe-final"},
		{"скрытый каталог", "/home/user/.claude", "-home-user--claude"},
		{"регистр сохраняется", "/home/user/Devs/ChatBot", "-home-user-Devs-ChatBot"},
		{"дефис не трогается", "/home/user/Devs/Data-Sync", "-home-user-Devs-Data-Sync"},
		{"цифры сохраняются", "/home/user/Devs/app3", "-home-user-Devs-app3"},
		{"хвостовой слеш", "/home/user/Devs/", "-home-user-Devs"},
		{"двойные слеши", "/home//user///Devs", "-home-user-Devs"},
		{"корень", "/", "-"},
		{"пробел в имени", "/home/user/My Project", "-home-user-My-Project"},
		{"пробел и точка вместе", "/home/user/My Project v1.2", "-home-user-My-Project-v1-2"},
		// Не-ASCII живыми данными не проверяем — таких каталогов нет.
		// Тест закрепляет выбранное поведение: один дефис на РУНУ.
		{"кириллица", "/home/user/Проект", "-home-user-------"},
		{"эмодзи вне BMP", "/home/user/🔥", "-home-user--"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Slugify(tt.path); got != tt.want {
				t.Errorf("Slugify(%q)=%q, ожидалось %q", tt.path, got, tt.want)
			}
		})
	}
}

// projects готовит поддельное хранилище: каталог проекта со списком файлов.
func projects(t *testing.T, project string, files map[string]time.Time) string {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, Slugify(project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, mtime := range files {
		path := filepath.Join(dir, name)
		body := []byte("{\"type\":\"assistant\"}\n")
		if strings.HasPrefix(name, "empty") {
			body = nil
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDiscoverPicksNewestByMtime(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"

	root := projects(t, project, map[string]time.Time{
		"старая.jsonl":  base.Add(-2 * time.Hour),
		"свежая.jsonl":  base,
		"средняя.jsonl": base.Add(-time.Hour),
	})

	got, err := Discover(Options{Project: project, ProjectsDir: root})
	if err != nil {
		t.Fatalf("Discover вернул ошибку: %v", err)
	}
	if filepath.Base(got.Path) != "свежая.jsonl" {
		t.Errorf("выбран %q, ожидалась свежая.jsonl", filepath.Base(got.Path))
	}
	if got.SessionID != "свежая" {
		t.Errorf("SessionID=%q, ожидался свежая", got.SessionID)
	}
	if got.Mode != ModeLive {
		t.Errorf("Mode=%v, ожидался ModeLive", got.Mode)
	}
	if got.Slug != Slugify(project) {
		t.Errorf("Slug=%q", got.Slug)
	}
	if got.Project != project {
		t.Errorf("Project=%q, ожидался %q", got.Project, project)
	}
	// Каталог потоков субагентов подтверждён разведкой буквально.
	wantSub := filepath.Join(root, Slugify(project), "свежая", "subagents")
	if got.SubagentDir != wantSub {
		t.Errorf("SubagentDir=%q, ожидался %q", got.SubagentDir, wantSub)
	}
}

// Только что созданная сессия ещё пуста, но она и есть активная:
// нулевой размер не повод её пропускать.
func TestDiscoverKeepsFreshEmptySession(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"

	root := projects(t, project, map[string]time.Time{
		"старая.jsonl":    base.Add(-time.Hour),
		"empty-new.jsonl": base,
	})

	got, err := Discover(Options{Project: project, ProjectsDir: root})
	if err != nil {
		t.Fatalf("Discover вернул ошибку: %v", err)
	}
	if filepath.Base(got.Path) != "empty-new.jsonl" {
		t.Errorf("выбран %q, ожидался пустой, но самый свежий файл", filepath.Base(got.Path))
	}
}

// Одинаковый mtime не должен давать случайный выбор от запуска к запуску.
func TestDiscoverTieBreakIsDeterministic(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"

	root := projects(t, project, map[string]time.Time{
		"aaa.jsonl": base,
		"bbb.jsonl": base,
		"ccc.jsonl": base,
	})

	first, err := Discover(Options{Project: project, ProjectsDir: root})
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		got, err := Discover(Options{Project: project, ProjectsDir: root})
		if err != nil {
			t.Fatal(err)
		}
		if got.Path != first.Path {
			t.Fatalf("выбор скачет: %q против %q", got.Path, first.Path)
		}
	}
}

func TestDiscoverIgnoresNonTranscripts(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"

	root := projects(t, project, map[string]time.Time{
		"сессия.jsonl": base.Add(-time.Hour),
		"заметка.md":   base,
		"дамп.json":    base,
	})
	// Каталог сессии с субагентами — не транскрипт, хотя лежит рядом.
	if err := os.MkdirAll(filepath.Join(root, Slugify(project), "сессия", "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Битая ссылка: файл исчез между ReadDir и Stat — нормальная гонка.
	if err := os.Symlink(filepath.Join(root, "нет.jsonl"), filepath.Join(root, Slugify(project), "призрак.jsonl")); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(Options{Project: project, ProjectsDir: root})
	if err != nil {
		t.Fatalf("Discover вернул ошибку: %v", err)
	}
	if filepath.Base(got.Path) != "сессия.jsonl" {
		t.Errorf("выбран %q, ожидалась сессия.jsonl", filepath.Base(got.Path))
	}
}

func TestDiscoverBySessionID(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"

	root := projects(t, project, map[string]time.Time{
		"старая.jsonl": base.Add(-time.Hour),
		"свежая.jsonl": base,
	})

	got, err := Discover(Options{Project: project, Session: "старая", ProjectsDir: root})
	if err != nil {
		t.Fatalf("Discover вернул ошибку: %v", err)
	}
	if filepath.Base(got.Path) != "старая.jsonl" {
		t.Errorf("выбран %q, ожидалась запрошенная сессия", filepath.Base(got.Path))
	}
	// Явно указанная сессия — завершённый транскрипт, тейлер не нужен.
	if got.Mode != ModeArchive {
		t.Errorf("Mode=%v, ожидался ModeArchive", got.Mode)
	}
}

// Явно указанная сессия может оказаться текущей живой: пользователь должен
// узнать, что тейлинга у него нет, а не догадываться.
func TestDiscoverMarksNewestSession(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"

	root := projects(t, project, map[string]time.Time{
		"старая.jsonl": base.Add(-time.Hour),
		"свежая.jsonl": base,
	})

	live, err := Discover(Options{Project: project, Session: "свежая", ProjectsDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !live.Newest {
		t.Errorf("самая свежая сессия не помечена как активная")
	}

	old, err := Discover(Options{Project: project, Session: "старая", ProjectsDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if old.Newest {
		t.Errorf("старая сессия помечена как активная")
	}

	auto, err := Discover(Options{Project: project, ProjectsDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !auto.Newest {
		t.Errorf("автообнаружение обязано давать активную сессию")
	}
}

func TestDiscoverErrors(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"

	t.Run("каталога проекта нет", func(t *testing.T) {
		root := t.TempDir()
		_, err := Discover(Options{Project: project, ProjectsDir: root})
		if !errors.Is(err, ErrNoProjectDir) {
			t.Fatalf("ошибка %v, ожидалась ErrNoProjectDir", err)
		}
		if !strings.Contains(err.Error(), project) {
			t.Errorf("в тексте ошибки нет каталога проекта: %v", err)
		}
	})

	t.Run("каталог пуст", func(t *testing.T) {
		root := projects(t, project, nil)
		_, err := Discover(Options{Project: project, ProjectsDir: root})
		if !errors.Is(err, ErrNoSessions) {
			t.Fatalf("ошибка %v, ожидалась ErrNoSessions", err)
		}
	})

	t.Run("сессия не найдена", func(t *testing.T) {
		root := projects(t, project, map[string]time.Time{"есть.jsonl": base})
		_, err := Discover(Options{Project: project, Session: "нету", ProjectsDir: root})
		if !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("ошибка %v, ожидалась ErrSessionNotFound", err)
		}
		if !strings.Contains(err.Error(), "нету") {
			t.Errorf("в тексте ошибки нет идентификатора: %v", err)
		}
		// Речь о файле, а не о каталоге: путать пользователя нельзя.
		if strings.Contains(err.Error(), "directory /") {
			t.Errorf("файл назван каталогом: %v", err)
		}
		if !strings.Contains(err.Error(), "file ") {
			t.Errorf("в тексте нет слова «file»: %v", err)
		}
	})

	t.Run("на месте каталога проекта файл", func(t *testing.T) {
		store := t.TempDir()
		if err := os.WriteFile(filepath.Join(store, Slugify(project)), []byte("не каталог"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Discover(Options{Project: project, ProjectsDir: store})
		if !errors.Is(err, ErrNoProjectDir) {
			t.Fatalf("ошибка %v, ожидалась ErrNoProjectDir", err)
		}
	})

	t.Run("нет доступа к каталогу", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("под root права не проверить")
		}
		root := projects(t, project, map[string]time.Time{"есть.jsonl": base})
		dir := filepath.Join(root, Slugify(project))
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		_, err := Discover(Options{Project: project, ProjectsDir: root})
		if err == nil {
			t.Fatal("ошибки нет, ожидался отказ по правам")
		}
		// «Нет транскриптов» — неверное объяснение отказа в доступе.
		if errors.Is(err, ErrNoSessions) {
			t.Errorf("отказ по правам выдан как ErrNoSessions: %v", err)
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("ошибка %v не опознаётся как fs.ErrPermission", err)
		}
	})
}

// Идентификатор сессии подставляется в путь, поэтому он обязан быть именем
// файла и ничем больше: иначе ".." уводит за пределы хранилища, а SubagentDir
// вообще не проходит через resolvePath и остался бы без гейта.
func TestDiscoverRejectsTraversalSession(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"
	root := projects(t, project, map[string]time.Time{"есть.jsonl": base})

	bad := []string{
		"../../../../../etc/hosts",
		"..",
		".",
		"под/каталог",
		"/etc/hosts",
		"a/../../b",
		"",
	}

	for _, id := range bad {
		name := id
		if name == "" {
			continue // пустой идентификатор — это «выбери свежую», отдельный путь
		}
		t.Run(name, func(t *testing.T) {
			got, err := Discover(Options{Project: project, Session: id, ProjectsDir: root})
			if !errors.Is(err, ErrBadSession) {
				t.Fatalf("ошибка %v, ожидалась ErrBadSession", err)
			}
			if got.Path != "" || got.SubagentDir != "" {
				t.Errorf("при отказе вернулась цель: %+v", got)
			}
		})
	}
}

// Каталог субагентов обязан оставаться внутри хранилища при любом вводе.
func TestDiscoverSubagentDirStaysInsideStore(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const project = "/home/user/Devs/тест"
	root := projects(t, project, map[string]time.Time{"есть.jsonl": base})

	got, err := Discover(Options{Project: project, ProjectsDir: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{got.Path, got.SubagentDir} {
		rel, err := filepath.Rel(root, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("путь %q вне хранилища %q", p, root)
		}
	}
}

// Каталог проекта может быть виден через симлинк, а Claude Code пишет по
// физическому пути: slug обязан считаться от разыменованного каталога.
func TestDiscoverResolvesSymlinkedProject(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)

	home := t.TempDir()
	real := filepath.Join(home, "физический")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "ссылка")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	root := projects(t, real, map[string]time.Time{"сессия.jsonl": base})

	got, err := Discover(Options{Project: link, ProjectsDir: root})
	if err != nil {
		t.Fatalf("сессия по симлинку не найдена: %v", err)
	}
	if got.Project != real {
		t.Errorf("Project=%q, ожидался физический путь %q", got.Project, real)
	}
}

// Промах по cwd должен давать готовую команду, а не сухой отказ.
func TestDiscoverHintFromParent(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	const parent = "/home/user/Devs/проект"
	child := filepath.Join(parent, "internal", "ui")

	root := projects(t, parent, map[string]time.Time{"сессия.jsonl": base})

	_, err := Discover(Options{Project: child, ProjectsDir: root})
	if !errors.Is(err, ErrNoProjectDir) {
		t.Fatalf("ошибка %v, ожидалась ErrNoProjectDir", err)
	}

	var de *DiscoverError
	if !errors.As(err, &de) {
		t.Fatalf("ошибка %T не типизирована как *DiscoverError", err)
	}
	if !strings.Contains(de.Hint, "--project "+parent) {
		t.Errorf("подсказка %q не содержит команду с родителем %q", de.Hint, parent)
	}
	if !strings.Contains(err.Error(), de.Hint) {
		t.Errorf("подсказка не попала в текст ошибки: %v", err)
	}
}

// Родитель без сессий подсказки не даёт: врать пользователю нельзя.
func TestDiscoverNoHintWhenParentEmpty(t *testing.T) {
	const parent = "/home/user/Devs/проект"
	child := filepath.Join(parent, "internal")

	root := projects(t, parent, nil) // каталог есть, транскриптов нет

	_, err := Discover(Options{Project: child, ProjectsDir: root})
	var de *DiscoverError
	if !errors.As(err, &de) {
		t.Fatalf("ошибка %T не типизирована", err)
	}
	if de.Hint != "" {
		t.Errorf("подсказка %q выдана на пустой каталог родителя", de.Hint)
	}
}

func TestDiscoverExpandsProjectPath(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	home := t.TempDir()
	t.Setenv("HOME", home)

	project := filepath.Join(home, "Devs", "тильда")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	root := projects(t, project, map[string]time.Time{"сессия.jsonl": base})

	t.Run("тильда", func(t *testing.T) {
		got, err := Discover(Options{Project: "~/Devs/тильда", ProjectsDir: root})
		if err != nil {
			t.Fatalf("Discover вернул ошибку: %v", err)
		}
		if got.Project != project {
			t.Errorf("Project=%q, ожидался %q", got.Project, project)
		}
	})

	t.Run("относительный путь", func(t *testing.T) {
		t.Chdir(filepath.Join(home, "Devs"))
		got, err := Discover(Options{Project: "тильда", ProjectsDir: root})
		if err != nil {
			t.Fatalf("Discover вернул ошибку: %v", err)
		}
		if got.Project != project {
			t.Errorf("Project=%q, ожидался %q", got.Project, project)
		}
	})

	t.Run("пустой Project берёт текущий каталог", func(t *testing.T) {
		t.Chdir(project)
		got, err := Discover(Options{ProjectsDir: root})
		if err != nil {
			t.Fatalf("Discover вернул ошибку: %v", err)
		}
		if got.Project != project {
			t.Errorf("Project=%q, ожидался %q", got.Project, project)
		}
	})
}

// Без переменной HOME хранилище не определить — это внятная ошибка, не паника.
func TestProjectsDirNeedsHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := ProjectsDir(); err == nil {
		t.Errorf("ошибки нет, ожидалась ErrNoHome")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ProjectsDir()
	if err != nil {
		t.Fatalf("ProjectsDir вернул ошибку: %v", err)
	}
	if want := filepath.Join(home, ".claude", "projects"); got != want {
		t.Errorf("ProjectsDir()=%q, ожидалось %q", got, want)
	}
}

func TestModeString(t *testing.T) {
	if ModeLive.String() == ModeArchive.String() {
		t.Errorf("режимы неразличимы по имени")
	}
	for _, m := range []Mode{ModeLive, ModeArchive} {
		if m.String() == "" {
			t.Errorf("Mode(%d).String() пуст", int(m))
		}
	}
}
