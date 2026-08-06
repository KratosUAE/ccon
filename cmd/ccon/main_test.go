package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KratosUAE/ccon/internal/cost"
	"github.com/KratosUAE/ccon/internal/parse"
	"github.com/KratosUAE/ccon/internal/session"
)

// atRepoRoot переводит тест в корень репозитория: пути транскриптов
// разрешаются относительно текущего каталога.
func atRepoRoot(t *testing.T) {
	t.Helper()
	t.Chdir(filepath.Join("..", ".."))
}

func runArgs(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code = run(t.Context(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// syncBuffer — буфер, в который пишет живой тейлер, пока тест его читает.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor ждёт появления подстроки в живом выводе.
func waitFor(t *testing.T, b *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("не дождались %q в выводе:\n%s", want, b.String())
}

func TestRunDumpTolerantToBrokenLines(t *testing.T) {
	atRepoRoot(t)
	code, out, errOut := runArgs(t, "--dump", "testdata/broken.jsonl")

	if code != 0 {
		t.Fatalf("код возврата %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "первая строка цела") {
		t.Errorf("событие целой строки потеряно:\n%s", out)
	}
	if !strings.Contains(out, "x/y.txt") {
		t.Errorf("событие после битых строк потеряно:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL     events 2 · requests 2 · lines 6 · skipped 2") {
		t.Errorf("счётчики сводки разошлись:\n%s", out)
	}
}

func TestRunDumpDedupSynthetic(t *testing.T) {
	atRepoRoot(t)
	code, out, _ := runArgs(t, "--dump", "testdata/dup-usage.jsonl")

	if code != 0 {
		t.Fatalf("код возврата %d", code)
	}
	if !strings.Contains(out, "in 8 · out 879 · cache read 1020 · cache write 500") {
		t.Errorf("дедуплицированная сводка не сошлась:\n%s", out)
	}
	if !strings.Contains(out, "requests 2") {
		t.Errorf("число запросов не сошлось:\n%s", out)
	}
}

// Срез со всеми семью системными подтипами, отказом по правилу и
// автономными типами записей: разбор не паникует и ничего не теряет.
func TestRunDumpSubtypesSlice(t *testing.T) {
	atRepoRoot(t)
	code, out, errOut := runArgs(t, "--dump", "testdata/subtypes-slice.jsonl")

	if code != 0 {
		t.Fatalf("код возврата %d, stderr: %s", code, errOut)
	}

	for _, want := range []string{
		"system     turn 4m34s",
		"system     Сводка: перенесли конфиг на zsh",
		"system     /context",
		"system     context compaction: 385077 → 24939",
		"⚠ swap     claude-fable-5 → claude-opus-5 · cyber",
		"system     Unknown command: /remember",
		"system     stop hooks: 6",
		"system     неизвестный_подтип",
		"✗ ERROR    Заблокировано локальным правилом разрешений",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет строки %q", want)
		}
	}

	// Автономные типы событий не порождают: last-prompt, queue-operation,
	// file-history-snapshot — три строки из тринадцати.
	if !strings.Contains(out, "TOTAL     events 10 · requests 1 · lines 13 · skipped 0") {
		t.Errorf("счётчики сводки разошлись:\n%s", out)
	}
	// Огромный код хука в лог не попадает.
	if strings.Contains(out, "node -e") {
		t.Errorf("в лог утёк код стоп-хука:\n%s", out)
	}
}

// Веб-поиск тарифицируется отдельно и показывается только когда он был:
// в сводке по обычному транскрипту лишней строки быть не должно.
func TestRunDumpWebSearch(t *testing.T) {
	atRepoRoot(t)

	code, out, _ := runArgs(t, "--dump", "testdata/websearch.jsonl")
	if code != 0 {
		t.Fatalf("код возврата %d", code)
	}
	// Строка веб-поиска не должна читаться как добавка к ЦЕНЕ: она в неё входит.
	if !strings.Contains(out, "WEBSEARCH 3 queries · $0.03 (included in COST)") {
		t.Errorf("в сводке нет строки веб-поиска:\n%s", out)
	}

	_, plain, _ := runArgs(t, "--dump", "testdata/dup-usage.jsonl")
	if strings.Contains(plain, "WEBSEARCH") {
		t.Errorf("строка веб-поиска показана при нулевом счётчике:\n%s", plain)
	}
}

// Связка Decode → Cost для ускоренного режима, сквозь весь конвейер:
// 1M выходных токенов Opus 5 в fast стоят $50, а не $25.
func TestRunDumpFastSpeed(t *testing.T) {
	atRepoRoot(t)

	code, out, errOut := runArgs(t, "--dump", "testdata/fast.jsonl")
	if code != 0 {
		t.Fatalf("код возврата %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "COST      $50.00") {
		t.Errorf("ускоренный режим не удвоил тариф:\n%s", out)
	}
}

// fakeSession готовит поддельное хранилище транскриптов в подменённом HOME
// и возвращает каталог проекта, сессия которого туда положена.
func fakeSession(t *testing.T, body string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	project := filepath.Join(home, "Devs", "проект")
	dir := filepath.Join(home, ".claude", "projects", session.Slugify(project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aaa11111-2222-3333.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return project
}

// Без аргументов ccon обязан САМ найти сессию проекта и сказать, что нашёл.
func TestRunDiscoversSession(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","message":{"id":"m","model":"claude-opus-5","content":[{"type":"text","text":"живой поток"}]}}`+"\n")

	// Без --dump открывается TUI: терминала в тесте нет, поэтому проверяем
	// то, что ему готовится, — обнаруженную сессию и собранные данные.
	target, err := session.Discover(session.Options{Project: project})
	if err != nil {
		t.Fatalf("сессия не найдена: %v", err)
	}
	if filepath.Base(target.Path) != "aaa11111-2222-3333.jsonl" {
		t.Errorf("найден %q", target.Path)
	}
	if target.Mode != session.ModeLive {
		t.Errorf("режим %v, ожидался live", target.Mode)
	}

	opts, err := loadArchive(target.Path, target)
	if err != nil {
		t.Fatalf("loadArchive вернул ошибку: %v", err)
	}
	if len(opts.Events) != 1 || opts.Events[0].Detail != "живой поток" {
		t.Errorf("события собраны неверно: %+v", opts.Events)
	}
	if opts.Project != "проект" {
		t.Errorf("Project=%q", opts.Project)
	}
	if opts.Model != "claude-opus-5" {
		t.Errorf("Model=%q", opts.Model)
	}
	if len(opts.Agents) != 1 || opts.Agents[0].Name != "main" {
		t.Errorf("Agents=%+v", opts.Agents)
	}
}

// Идентификатор сессии с ".." отвергается ещё в discover.
func TestRunRejectsTraversalSession(t *testing.T) {
	project := fakeSession(t, "{}\n")

	code, out, errOut := runArgs(t, "--dump", "--project", project, "--session", "../../../../../etc/hosts")
	if code == 0 {
		t.Errorf("код возврата 0, ожидался отказ")
	}
	if !strings.Contains(errOut, "invalid session id") {
		t.Errorf("причина отказа не названа: %q", errOut)
	}
	if strings.Contains(out, "TOTAL") {
		t.Errorf("при отказе напечатана сводка:\n%s", out)
	}
}

// --dump без пути печатает события найденной сессии.
func TestRunDumpDiscoveredSession(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","requestId":"r","message":{"id":"m","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"живой поток"}],"usage":{"input_tokens":1,"output_tokens":2}}}`+"\n")

	code, out, errOut := runArgs(t, "--dump", "--project", project)
	if code != 0 {
		t.Fatalf("код возврата %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "живой поток") {
		t.Errorf("события найденной сессии не напечатаны:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL     events 1 · requests 1 · lines 1 · skipped 0") {
		t.Errorf("сводки нет:\n%s", out)
	}
	// Служебная строка об источнике не должна засорять stdout: он для событий.
	if !strings.Contains(errOut, "aaa11111-2222-3333.jsonl") {
		t.Errorf("источник не назван в stderr: %q", errOut)
	}
}

// --session переводит в архивный режим и выбирает указанный файл.
func TestRunDumpBySessionID(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","message":{"id":"m","content":[{"type":"text","text":"по идентификатору"}]}}`+"\n")

	code, out, errOut := runArgs(t, "--dump", "--project", project, "--session", "aaa11111-2222-3333")
	if code != 0 {
		t.Fatalf("код возврата %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "по идентификатору") {
		t.Errorf("события не напечатаны:\n%s", out)
	}
	if !strings.Contains(errOut, "archive") {
		t.Errorf("режим archive не объявлен: %q", errOut)
	}
}

// Промах по каталогу проекта даёт готовую команду, а не сухой отказ.
func TestRunPrintsHintOnMiss(t *testing.T) {
	project := fakeSession(t, "{}\n")
	deep := filepath.Join(project, "internal", "ui")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runArgs(t, "--project", deep)
	if code == 0 {
		t.Errorf("код возврата 0, ожидалась ошибка")
	}
	if !strings.Contains(errOut, "ccon --project "+project) {
		t.Errorf("в stderr нет готовой команды:\n%s", errOut)
	}
}

// --follow: строки, дописанные в файл на ходу, доезжают до stdout, а сводка
// печатается при остановке. Живая проверка, фикстурой не подделывается.
func TestRunDumpFollowLiveAppend(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","requestId":"r1","message":{"id":"m1","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"до запуска"}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n")
	path := filepath.Join(os.Getenv("HOME"), ".claude", "projects",
		session.Slugify(project), "aaa11111-2222-3333.jsonl")

	ctx, cancel := context.WithCancel(t.Context())
	out := &syncBuffer{}
	var stderr bytes.Buffer

	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--dump", "--follow", "--project", project}, out, &stderr) }()

	waitFor(t, out, "до запуска")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"assistant","timestamp":"2026-08-04T09:00:02Z","requestId":"r2","message":{"id":"m2","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"дописано на ходу"}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	start := time.Now()
	waitFor(t, out, "дописано на ходу")
	if d := time.Since(start); d > time.Second {
		t.Errorf("строка шла %v, ожидалось меньше секунды", d)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("код возврата %d, stderr: %s", code, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ccon не завершился после отмены контекста")
	}

	// Сводка печатается на выходе и учитывает обе записи.
	if !strings.Contains(out.String(), "requests 2") {
		t.Errorf("итоговая сводка не учла дозапись:\n%s", out.String())
	}
	// Режим источника не должен выглядеть как разовый дамп.
	if !strings.Contains(stderr.String(), "tailing") {
		t.Errorf("тейлинг не объявлен в stderr: %q", stderr.String())
	}
}

// agentLine собирает строку транскрипта субагента.
func agentLine(id, text, attribution string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-08-04T09:00:%02dZ","requestId":"r%s","attributionAgent":%q,"message":{"id":"m%s","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":%q}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n",
		len(id)%60, id, attribution, id, text)
}

// subagentsDir возвращает каталог потоков субагентов найденной сессии.
func subagentsDir(project string) string {
	return filepath.Join(os.Getenv("HOME"), ".claude", "projects",
		session.Slugify(project), "aaa11111-2222-3333", "subagents")
}

// Главная проверка всего инструмента: главный поток молчит, субагент пишет —
// строки субагента обязаны идти в лог. На этом ломался bash-прототип.
func TestRunFollowShowsAgentWhileMainSilent(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","requestId":"r0","message":{"id":"m0","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"делегирую задачу"}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n")

	sub := subagentsDir(project)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	out := &syncBuffer{}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--dump", "--follow", "--project", project}, out, &stderr) }()

	waitFor(t, out, "делегирую задачу")

	// Субагент рождается уже после старта наблюдения; главный файл не растёт.
	agent := filepath.Join(sub, "agent-a1a73a076a75841a2.jsonl")
	if err := os.WriteFile(session.MetaPath(agent),
		[]byte(`{"agentType":"go-code-adapter","description":"фикс","toolUseId":"t1","spawnDepth":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, []byte(agentLine("a1", "работаю пока главный молчит", "go-code-adapter")), 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	waitFor(t, out, "работаю пока главный молчит")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("строка субагента шла %v", d)
	}
	if !strings.Contains(out.String(), "go-code-adapter") {
		t.Errorf("строка не подписана именем агента:\n%s", out.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("код возврата %d, stderr: %s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ccon не завершился после отмены")
	}
}

// Архивный разбор сессии с делегированием: события всех файлов в одном
// потоке и в хронологическом порядке.
func TestRunDumpMergesSubagentsSorted(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:10Z","requestId":"r0","message":{"id":"m0","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"главный-позже"}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n")

	sub := subagentsDir(project)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(sub, "agent-b2b84815c27bcb83b.jsonl")
	early := `{"type":"assistant","timestamp":"2026-08-04T09:00:05Z","requestId":"r1","attributionAgent":"go-linter","message":{"id":"m1","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"агент-раньше"}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"
	if err := os.WriteFile(agent, []byte(early), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runArgs(t, "--dump", "--project", project)
	if code != 0 {
		t.Fatalf("код возврата %d, stderr: %s", code, errOut)
	}

	iEarly := strings.Index(out, "агент-раньше")
	iLate := strings.Index(out, "главный-позже")
	if iEarly < 0 || iLate < 0 {
		t.Fatalf("не все события напечатаны:\n%s", out)
	}
	if iEarly > iLate {
		t.Errorf("порядок не хронологический: агент 09:00:05 напечатан после главного 09:00:10:\n%s", out)
	}
	// Имя агента взято из записи: .meta.json у этого субагента нет.
	if !strings.Contains(out, "go-linter") {
		t.Errorf("откат на attributionAgent не сработал:\n%s", out)
	}
	if !strings.Contains(out, "requests 2") {
		t.Errorf("расход субагента не учтён:\n%s", out)
	}
}

// Запись с битой меткой времени обязана остаться у своих соседей, а не
// всплыть в начало лога как 00:00:00. Архивный и живой режимы обязаны
// сходиться именно там, где данные подпорчены.
func TestRunKeepsBrokenTimestampInPlace(t *testing.T) {
	ev := func(ts, text string) string {
		stamp := ""
		if ts != "" {
			stamp = fmt.Sprintf(`"timestamp":%q,`, ts)
		}
		return fmt.Sprintf(`{"type":"assistant",%s"requestId":"r%s","message":{"id":"m%s","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":%q}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n",
			stamp, text, text, text)
	}
	body := ev("2026-08-04T09:00:01Z", "первая") + ev("вчера", "битая") + ev("2026-08-04T09:00:03Z", "третья")

	project := fakeSession(t, body)
	sub := subagentsDir(project)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	order := func(out string) []int {
		return []int{
			strings.Index(out, "первая"),
			strings.Index(out, "битая"),
			strings.Index(out, "третья"),
		}
	}

	code, out, errOut := runArgs(t, "--dump", "--project", project)
	if code != 0 {
		t.Fatalf("код возврата %d, stderr: %s", code, errOut)
	}
	got := order(out)
	if got[0] < 0 || got[1] < 0 || got[2] < 0 {
		t.Fatalf("не все события напечатаны:\n%s", out)
	}
	if !(got[0] < got[1] && got[1] < got[2]) {
		t.Errorf("битая запись сдвинулась в архивном режиме:\n%s", out)
	}
	if strings.Contains(out, "00:00:00") {
		t.Errorf("битая метка напечатана как 00:00:00:\n%s", out)
	}

	// Живой режим читает тот же файл и обязан дать тот же порядок.
	ctx, cancel := context.WithCancel(t.Context())
	live := &syncBuffer{}
	var liveErr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--dump", "--follow", "--project", project}, live, &liveErr) }()

	waitFor(t, live, "третья")
	cancel()
	<-done

	if lg := order(live.String()); !(lg[0] < lg[1] && lg[1] < lg[2]) {
		t.Errorf("порядок в живом режиме разошёлся с архивным:\n%s", live.String())
	}
}

// Позиционный путь показывает ровно один файл, но о соседних потоках
// пользователь должен узнать: ради них инструмент и затевался.
func TestRunHintsAboutSiblingSubagents(t *testing.T) {
	project := fakeSession(t, "{}\n")
	sub := subagentsDir(project)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-a1a73a076a75841a2.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	main := filepath.Join(os.Getenv("HOME"), ".claude", "projects",
		session.Slugify(project), "aaa11111-2222-3333.jsonl")

	_, _, errOut := runArgs(t, "--dump", main)
	if !strings.Contains(errOut, "found 1 subagent stream") {
		t.Errorf("подсказка о соседних потоках не напечатана:\n%s", errOut)
	}
	if !strings.Contains(errOut, "--session aaa11111-2222-3333") {
		t.Errorf("в подсказке нет готовой команды:\n%s", errOut)
	}
}

// У обычного транскрипта без соседей подсказки быть не должно.
func TestRunNoHintWithoutSubagents(t *testing.T) {
	atRepoRoot(t)

	_, _, errOut := runArgs(t, "--dump", "testdata/tools.jsonl")
	if strings.Contains(errOut, "потоков субагентов") {
		t.Errorf("лишняя подсказка:\n%s", errOut)
	}
}

// --follow без --dump бессмыслен: TUI появится в S8.
func TestRunFollowRequiresDump(t *testing.T) {
	project := fakeSession(t, "{}\n")

	code, _, errOut := runArgs(t, "--follow", "--project", project)
	if code == 0 {
		t.Errorf("код возврата 0, ожидалась ошибка")
	}
	if !strings.Contains(errOut, "--follow") {
		t.Errorf("причина отказа не названа: %q", errOut)
	}
}

// Архивный путь обязан доносить до интерфейса то, из чего собираются новые
// табы: ключ сшивки и полный путь файловой операции. Сортировка по времени и
// починка меток их не теряют.
func TestLoadArchiveCarriesToolFields(t *testing.T) {
	body := `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","message":{"id":"m1","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_01mcp","name":"mcp__serena__find_symbol","input":{"name_path":"X"}}]}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-04T09:00:02Z","message":{"id":"m2","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_01read","name":"Read","input":{"file_path":"/home/user/proj/internal/parse/decode.go"}}]}}` + "\n"
	project := fakeSession(t, body)
	path := filepath.Join(os.Getenv("HOME"), ".claude", "projects",
		session.Slugify(project), "aaa11111-2222-3333.jsonl")

	target, err := session.Discover(session.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := loadArchive(path, target)
	if err != nil {
		t.Fatal(err)
	}

	if len(opts.Events) != 2 {
		t.Fatalf("событий %d, ожидалось 2: %+v", len(opts.Events), opts.Events)
	}
	if got := opts.Events[0]; got.ToolID != "toolu_01mcp" || got.Tool != "mcp__serena__find_symbol" {
		t.Errorf("вызов MCP приехал как %+v", got)
	}
	if got := opts.Events[1]; got.ToolID != "toolu_01read" ||
		got.Path != "/home/user/proj/internal/parse/decode.go" {
		t.Errorf("файловая операция приехала как %+v", got)
	}
	// Деталь транскрипта прежняя: полный путь живёт отдельным полем.
	if got := opts.Events[1].Detail; got != "parse/decode.go" {
		t.Errorf("Detail=%q, ожидалась пара последних компонент", got)
	}
}

// Архивный путь обязан донести до интерфейса и корм линкера: без результатов
// каждый вызов завершённой сессии навсегда остался бы «выполняется».
// Фикстура — та же, что у parse- и ui-тестов.
func TestLoadArchiveCarriesResults(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	project := fakeSession(t, string(body))
	path := filepath.Join(os.Getenv("HOME"), ".claude", "projects",
		session.Slugify(project), "aaa11111-2222-3333.jsonl")

	target, err := session.Discover(session.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := loadArchive(path, target)
	if err != nil {
		t.Fatal(err)
	}

	// В фикстуре шесть блоков tool_result (шестой — дубль пятого).
	if len(opts.Results) != 6 {
		t.Fatalf("результатов %d, ожидалось 6: %+v", len(opts.Results), opts.Results)
	}

	seen := map[string]parse.Result{}
	for _, r := range opts.Results {
		if r.ToolUseID == "" {
			t.Errorf("результат без ключа сшивки: %+v", r)
		}
		if r.Time.IsZero() {
			t.Errorf("результат без отметки времени: %+v", r)
		}
		seen[r.ToolUseID] = r
	}
	if r := seen["toolu_01Deny0000000000000001"]; !r.IsError || r.Denial != "permission-rule" {
		t.Errorf("отказ приехал как %+v", r)
	}
	if r := seen["toolu_01Read0000000000000001"]; r.IsError || r.Denial != "" {
		t.Errorf("успешный результат приехал как %+v", r)
	}
}

// Стартовый таб задаётся флагом. Проверка значения — одна и до всего
// остального: с мусором в --view открывать сессию незачем.
func TestRunViewFlag(t *testing.T) {
	project := fakeSession(t, "{}\n")

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		// Терминала у теста нет, поэтому исправный таб доходит до прежней
		// проверки вывода — это и означает, что значение принято.
		{"transcript", []string{"--view", "transcript"}, exitUsage, "not a terminal"},
		{"mcp", []string{"--view", "mcp"}, exitUsage, "not a terminal"},
		{"files", []string{"--view", "files"}, exitUsage, "not a terminal"},
		{"мусор", []string{"--view", "bogus"}, exitUsage, `unknown --view "bogus"`},
		{"пустое значение", []string{"--view", ""}, exitUsage, `unknown --view ""`},
		{"регистр значим", []string{"--view", "MCP"}, exitUsage, `unknown --view "MCP"`},
		// У текстового вывода табов нет: молча проглотить просьбу нельзя.
		{"вместе с --dump", []string{"--dump", "--view", "mcp"}, exitUsage, "does nothing with --dump"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runArgs(t, append(tt.args, "--project", project)...)
			if code != tt.wantCode {
				t.Errorf("код возврата %d, ожидался %d (stderr: %s)", code, tt.wantCode, errOut)
			}
			if !strings.Contains(errOut, tt.wantErr) {
				t.Errorf("в stderr нет %q:\n%s", tt.wantErr, errOut)
			}
			if strings.Contains(out, "TOTAL") {
				t.Errorf("при отказе напечатана сводка:\n%s", out)
			}
		})
	}

	// Без флага текстовый вывод работает как прежде: проверка на --dump
	// срабатывает только на ЯВНУЮ просьбу, а не на значение по умолчанию.
	if code, out, errOut := runArgs(t, "--dump", "--project", project); code != exitOK {
		t.Errorf("код возврата %d без --view (stderr: %s, stdout: %s)", code, errOut, out)
	}
}

// Пользователь, явно указавший СВОЮ ЖЕ живую сессию, не должен молча остаться
// без тейлинга: ему говорят, что делать.
func TestRunWarnsAboutActiveSessionWithoutFollow(t *testing.T) {
	project := fakeSession(t, "{}\n")

	_, _, errOut := runArgs(t, "--dump", "--project", project, "--session", "aaa11111-2222-3333")
	if !strings.Contains(errOut, "--follow") {
		t.Errorf("нет подсказки про --follow для активной сессии:\n%s", errOut)
	}

	// С --follow предупреждать не о чем.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	var stderr bytes.Buffer
	run(ctx, []string{"--dump", "--follow", "--project", project, "--session", "aaa11111-2222-3333"}, &out, &stderr)
	if strings.Contains(stderr.String(), "добавьте --follow") {
		t.Errorf("предупреждение выдано при уже включённом --follow:\n%s", stderr.String())
	}
}

// Точка вместо имени проекта в шапке недопустима ни при каком входе.
func TestProjectNameNeverDot(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","cwd":"/home/user/Devs/витрина","requestId":"r","message":{"id":"m","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"привет"}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n")
	path := filepath.Join(os.Getenv("HOME"), ".claude", "projects",
		session.Slugify(project), "aaa11111-2222-3333.jsonl")

	target, err := session.Discover(session.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]session.Target{
		"обнаружение":      target,
		"позиционный путь": {Path: path, Mode: session.ModeArchive},
	}
	for name, tg := range cases {
		t.Run(name, func(t *testing.T) {
			opts, err := loadArchive(path, tg)
			if err != nil {
				t.Fatalf("loadArchive: %v", err)
			}
			if opts.Project == "." || opts.Project == "" {
				t.Errorf("имя проекта %q", opts.Project)
			}
		})
	}

	// У позиционного пути имя берётся из cwd записи, а не из слага каталога.
	opts, err := loadArchive(path, session.Target{Path: path, Mode: session.ModeArchive})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Project != "витрина" {
		t.Errorf("Project=%q, ожидалось имя из cwd записи", opts.Project)
	}
}

// Выход из TUI обязан гасить всё дерево наблюдения: тейлеры, опрос каталога
// и мост в интерфейс. В S5 такой тест есть у watcher, здесь — у моста.
func TestLiveFeedStopsEverything(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","requestId":"r","message":{"id":"m","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"живое"}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n")
	sub := subagentsDir(project)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		name := filepath.Join(sub, fmt.Sprintf("agent-%017d.jsonl", i))
		if err := os.WriteFile(name, []byte(agentLine(fmt.Sprint(i), "работа", "go-linter")), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	target, err := session.Discover(session.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}

	before := runtime.NumGoroutine()
	feed, cancel := liveFeed(t.Context(), target)

	// Дожидаемся первой порции: наблюдение точно поднялось.
	select {
	case batch := <-feed.Batches:
		if len(batch.Events) == 0 {
			t.Errorf("пустая порция событий")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("живых событий не дождались")
	}

	cancel()
	for range feed.Batches { //nolint:revive // дочитываем до закрытия
	}

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("горутин было %d, стало %d: наблюдение пережило выход", before, after)
	}
}

// Мост обязан отдавать интерфейсу не только события, но и счётчики:
// агенты и расход считаются здесь, интерфейс их только показывает.
func TestLiveFeedFillsTotalsAndAgents(t *testing.T) {
	project := fakeSession(t, `{"type":"assistant","timestamp":"2026-08-04T09:00:01Z","requestId":"r","message":{"id":"m","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"главный"}],"usage":{"input_tokens":1,"output_tokens":7}}}`+"\n")
	sub := subagentsDir(project)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-a1a73a076a75841a2.jsonl"),
		[]byte(agentLine("a1", "агентское", "go-code-adapter")), 0o600); err != nil {
		t.Fatal(err)
	}

	target, err := session.Discover(session.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	feed, cancel := liveFeed(t.Context(), target)
	defer cancel()

	seen := map[string]int{}
	var totals cost.Totals
	deadline := time.After(5 * time.Second)
	for len(seen) < 2 {
		select {
		case b := <-feed.Batches:
			for _, a := range b.Agents {
				seen[a.Name] = a.Count
			}
			totals = b.Totals
		case <-deadline:
			t.Fatalf("агенты не собрались: %v", seen)
		}
	}

	if seen["main"] == 0 || seen["go-code-adapter"] == 0 {
		t.Errorf("счётчики агентов пусты: %v", seen)
	}
	if totals.Output == 0 {
		t.Errorf("расход не посчитан: %+v", totals)
	}
}

// Живой мост обязан доносить не только события, но и результаты вызовов:
// без них строка вызова навсегда осталась бы без исхода.
func TestLiveFeedCarriesResults(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	project := fakeSession(t, string(body))

	target, err := session.Discover(session.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	feed, cancel := liveFeed(t.Context(), target)
	defer cancel()

	var events []parse.Event
	var results []parse.Result
	deadline := time.After(5 * time.Second)
	for len(results) < 6 {
		select {
		case b := <-feed.Batches:
			events = append(events, b.Events...)
			results = append(results, b.Results...)
		case <-deadline:
			t.Fatalf("собрано %d результатов из 6", len(results))
		}
	}

	// Ключи сшивки доезжают настоящими: пять вызовов фикстуры закрываются,
	// шестой результат — дубль, и сшивать его уже не с чем.
	l := parse.NewLinker()
	for _, ev := range events {
		l.Track(ev)
	}
	sewn := 0
	for _, r := range results {
		if _, ok := l.Resolve(r); ok {
			sewn++
		}
	}
	if sewn != 5 {
		t.Errorf("сшито %d вызовов из шести результатов, ожидалось 5", sewn)
	}
}

// Конфигурация, на которой инструмент врёт: длинный СТАРЫЙ главный файл и
// короткий СВЕЖИЙ файл субагента. Короткий дочитывается мгновенно, длинный
// ещё догоняется — и свежие строки обгоняют старые на возраст сессии.
func TestLiveCatchUpKeepsTimeMonotonic(t *testing.T) {
	var main strings.Builder
	for i := range 400 {
		fmt.Fprintf(&main, `{"type":"assistant","timestamp":"2026-08-04T08:%02d:%02dZ","requestId":"r%d","message":{"id":"m%d","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"главный-%d"}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n",
			i/60, i%60, i, i, i)
	}
	project := fakeSession(t, main.String())

	sub := subagentsDir(project)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var agent strings.Builder
	for i := range 3 {
		fmt.Fprintf(&agent, `{"type":"assistant","timestamp":"2026-08-04T11:35:%02dZ","requestId":"a%d","attributionAgent":"go-linter","message":{"id":"am%d","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"агент-%d"}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n",
			i, i, i, i)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-a1a73a076a75841a2.jsonl"), []byte(agent.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	target, err := session.Discover(session.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	feed, cancel := liveFeed(t.Context(), target)
	defer cancel()

	var got []parse.Event
	deadline := time.After(10 * time.Second)
	for len(got) < 403 {
		select {
		case b := <-feed.Batches:
			got = append(got, b.Events...)
		case <-deadline:
			t.Fatalf("собрано %d событий из 403", len(got))
		}
	}

	for i := 1; i < len(got); i++ {
		if got[i].Time.Before(got[i-1].Time) {
			t.Fatalf("время скачет назад на %v: событие %d (%s, %q) после %d (%s, %q)",
				got[i-1].Time.Sub(got[i].Time), i, got[i].Time.Format("15:04:05"), got[i].Detail,
				i-1, got[i-1].Time.Format("15:04:05"), got[i-1].Detail)
		}
	}
}

func TestRunFlagConflicts(t *testing.T) {
	atRepoRoot(t)

	tests := []struct {
		name string
		args []string
	}{
		{"--session вместе с путём", []string{"--dump", "--session", "abc", "testdata/tools.jsonl"}},
		{"--project вместе с путём", []string{"--dump", "--project", ".", "testdata/tools.jsonl"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runArgs(t, tt.args...)
			if code == 0 {
				t.Errorf("код возврата 0, ожидалась ошибка")
			}
			if strings.TrimSpace(errOut) == "" {
				t.Errorf("пустой stderr при ошибке")
			}
			if strings.Contains(out, "TOTAL") {
				t.Errorf("при конфликте флагов напечатана сводка:\n%s", out)
			}
		})
	}
}

func TestRunErrors(t *testing.T) {
	atRepoRoot(t)

	tests := []struct {
		name string
		args []string
	}{
		{"два пути", []string{"--dump", "testdata/tools.jsonl", "testdata/broken.jsonl"}},
		{"несуществующий файл", []string{"--dump", "нет-такого-файла.jsonl"}},
		{"неизвестный флаг", []string{"--вертолёт"}},
		{"несуществующий каталог проекта", []string{"--project", "/нет/такого/каталога"}},
		{"путь выше текущего каталога", []string{"--dump", "../../../../../etc/hostname"}},
		{"каталог вместо файла", []string{"--dump", "testdata"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runArgs(t, tt.args...)
			if code == 0 {
				t.Errorf("код возврата 0, ожидалась ошибка")
			}
			if strings.TrimSpace(errOut) == "" {
				t.Errorf("пустой stderr при ошибке")
			}
			if strings.Contains(out, "TOTAL") {
				t.Errorf("при ошибке напечатана сводка:\n%s", out)
			}
		})
	}
}

// Нерегулярный файл (FIFO, /dev/zero) вешает чтение или раздувает память —
// отказ должен быть немедленным и внятным.
func TestResolvePathRejectsNonRegular(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "каталог")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := resolvePath(root, dir); err == nil {
		t.Errorf("каталог принят как транскрипт")
	}
}

func TestResolvePath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "sess.jsonl")
	if err := os.WriteFile(inside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "под", "каталог")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(sub, "deep.jsonl")
	if err := os.WriteFile(deep, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "чужой.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "ссылка.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		arg     string
		wantErr bool
	}{
		{"файл в корне", inside, false},
		{"файл в подкаталоге", deep, false},
		{"выход через ..", filepath.Join(root, "..", "чужой.jsonl"), true},
		{"файл вне каталога", outside, true},
		{"симлинк наружу", link, true},
		{"несуществующий", filepath.Join(root, "нет.jsonl"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePath(root, tt.arg)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ошибки нет, путь принят: %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if !filepath.IsAbs(got) {
				t.Errorf("путь %q не абсолютный", got)
			}
		})
	}
}

// Относительный путь считается от корня, а не от процесса.
func TestResolvePathRelativeToRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sess.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolvePath(root, "sess.jsonl")
	if err != nil {
		t.Fatalf("относительный путь отвергнут: %v", err)
	}
	if filepath.Base(got) != "sess.jsonl" {
		t.Errorf("разрешён путь %q", got)
	}
}

// Хранилище транскриптов Claude Code лежит вне cwd, но это штатный источник
// данных инструмента: живой режим без него невозможен.
func TestResolvePathAllowsClaudeProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projects := filepath.Join(home, ".claude", "projects", "-home-user-Devs-x")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projects, "sess.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := resolvePath(t.TempDir(), transcript); err != nil {
		t.Errorf("транскрипт из ~/.claude/projects отвергнут: %v", err)
	}

	// Соседний файл в домашней папке остаётся закрытым.
	other := filepath.Join(home, "секрет.jsonl")
	if err := os.WriteFile(other, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePath(t.TempDir(), other); err == nil {
		t.Errorf("файл вне cwd и вне ~/.claude/projects принят")
	}
}

// failWriter ломается на записи: имитирует `ccon --dump ... | head` и полный диск.
type failWriter struct {
	after int
	n     int
}

var errWrite = errors.New("канал закрыт")

func (w *failWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	if w.n > w.after {
		return 0, errWrite
	}
	return len(p), nil
}

func TestRunReportsWriteError(t *testing.T) {
	atRepoRoot(t)

	var stderr bytes.Buffer
	code := run(t.Context(), []string{"--dump", "testdata/tools.jsonl"}, &failWriter{after: 1024}, &stderr)

	if code == 0 {
		t.Errorf("код возврата 0, хотя вывод не записался")
	}
	if strings.TrimSpace(stderr.String()) == "" {
		t.Errorf("ошибка записи не объяснена в stderr")
	}
}
