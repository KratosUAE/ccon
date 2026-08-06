package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	testFileInterval = 10 * time.Millisecond
	testDirInterval  = 20 * time.Millisecond
)

// fakeTarget собирает раскладку живой сессии: главный поток и каталог
// субагентов рядом. Раскладка подтверждена разведкой буквально.
func fakeTarget(t *testing.T, mainBody string) Target {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(path, []byte(mainBody), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sess", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	return Target{Path: path, SessionID: "sess", SubagentDir: sub, Mode: ModeLive}
}

// writeAgent кладёт транскрипт субагента, при meta != "" — и его описание.
func writeAgent(t *testing.T, target Target, id, body, agentType string) string {
	t.Helper()

	path := filepath.Join(target.SubagentDir, "agent-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if agentType != "" {
		writeMeta(t, path, agentType)
	}
	return path
}

// appendLine дописывает строку в существующий файл.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, line); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeMeta(t *testing.T, transcript, agentType string) {
	t.Helper()
	body := fmt.Sprintf(`{"agentType":%q,"description":"описание","toolUseId":"toolu_1","spawnDepth":1}`, agentType)
	if err := os.WriteFile(MetaPath(transcript), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func watchTestW(ctx context.Context, t Target) *Watcher {
	return Watch(ctx, t, WithFileInterval(testFileInterval), WithDirInterval(testDirInterval))
}

func watchTest(ctx context.Context, t Target) (<-chan Line, <-chan error) {
	w := watchTestW(ctx, t)
	return w.Lines(), w.Errs()
}

// Опоздавшее описание меняет подпись агента на ходу. Уже показанные строки
// потребитель обязан уметь переподписать — значит, о переименовании надо
// сообщать явно, а не надеяться, что агент напишет ещё хоть строку.
func TestWatchReportsRename(t *testing.T) {
	target := fakeTarget(t, "главная\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	w := watchTestW(ctx, target)
	collect(t, w.Lines(), 1, "главный поток")

	agent := writeAgent(t, target, "9f8e7d6c5b4a39281", "первая-без-описания\n", "")
	collect(t, w.Lines(), 1, "строка без описания")

	writeMeta(t, agent, "go-code-fixer")

	select {
	case r := <-w.Renames():
		if r.ID != "agent-9f8e7d6c" || r.Name != "go-code-fixer" {
			t.Errorf("переименование %+v, ожидалось agent-9f8e7d6c → go-code-fixer", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("сигнал о переименовании не пришёл")
	}

	if got := w.Names()["agent-9f8e7d6c"]; got != "go-code-fixer" {
		t.Errorf("снимок имён отдал %q", got)
	}
}

// Потребитель может уйти, не дочитав: горутины обязаны завершиться всё равно.
func TestWatchSurvivesAbandonedConsumer(t *testing.T) {
	target := fakeTarget(t, "главная\n")
	for i := range 10 {
		writeAgent(t, target, fmt.Sprintf("%017d", i),
			strings.Repeat(fmt.Sprintf("строка-%d\n", i), 50), fmt.Sprintf("тип-%d", i))
	}

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())

	w := watchTestW(ctx, target)
	time.Sleep(5 * testDirInterval) // строки копятся, их никто не читает
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("горутин было %d, стало %d: брошенный потребитель подвесил наблюдение", before, after)
	}
	select {
	case _, ok := <-w.Lines():
		if ok {
			t.Errorf("канал строк не закрыт")
		}
	case <-time.After(time.Second):
		t.Errorf("канал строк не закрылся")
	}
}

// Отсутствующее описание не должно перечитываться на каждой строке:
// у агента с битым .meta.json это десятки тысяч лишних сисколов за сессию.
func TestMetaRetryIsThrottled(t *testing.T) {
	w := &Watcher{
		cfg:       watchConfig{dirInterval: time.Second},
		names:     map[string]string{},
		metaTry:   map[string]time.Time{},
		metaTries: map[string]int{},
		renames:   make(chan Rename, 1),
		lastSeen:  map[string]bool{},
	}
	now := time.Now()

	// Первые попытки бесплатны: описание пишется сразу вслед за транскриптом.
	for i := range freeMetaTries {
		if !w.shouldRetryMeta("/x/agent-a.jsonl", now) {
			t.Fatalf("попытка %d обязана состояться", i+1)
		}
	}
	if w.shouldRetryMeta("/x/agent-a.jsonl", now.Add(10*time.Millisecond)) {
		t.Errorf("повтор через 10 мс — это перечитывание на каждой строке")
	}
	if !w.shouldRetryMeta("/x/agent-a.jsonl", now.Add(2*time.Second)) {
		t.Errorf("через такт опроса каталога повтор обязан быть разрешён")
	}
}

// Делегирование в потоке — сигнал, что субагент вот-вот появится: каталог
// начинает опрашиваться часто, и новый поток подхватывается быстрее.
func TestWatchFastPickupAfterDelegation(t *testing.T) {
	target := fakeTarget(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Редкий опрос каталога: без ускорения подхват занял бы пять секунд.
	w := Watch(ctx, target, WithFileInterval(testFileInterval), WithDirInterval(5*time.Second))

	appendLine(t, target.Path, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","input":{"subagent_type":"go-linter"}}]}}`)
	collect(t, w.Lines(), 1, "строка делегирования")

	writeAgent(t, target, "a1a73a076a75841a2", "родился\n", "go-linter")

	start := time.Now()
	got := collect(t, w.Lines(), 1, "строка нового субагента")
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("подхват занял %v — ускорение после делегирования не сработало", d)
	}
	if string(got[0].Data) != "родился" {
		t.Errorf("получено %q", got[0].Data)
	}
}

// collect собирает строки, пока не наберёт n штук либо не истечёт время.
func collect(t *testing.T, lines <-chan Line, n int, what string) []Line {
	t.Helper()

	var got []Line
	deadline := time.After(5 * time.Second)
	for len(got) < n {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("канал закрыт: собрано %d из %d (%s)", len(got), n, what)
			}
			got = append(got, l)
		case <-deadline:
			t.Fatalf("за 5 с собрано %d из %d (%s)", len(got), n, what)
		}
	}
	return got
}

func TestLineSource(t *testing.T) {
	tests := []struct {
		name       string
		line       Line
		fromRecord string
		want       string
	}{
		{
			name:       "главный поток",
			line:       Line{ID: SourceMain},
			fromRecord: SourceMain,
			want:       SourceMain,
		},
		{
			name:       "имя из meta главнее всего",
			line:       Line{ID: "agent-a1a73a07", Name: "kotlin-verifier"},
			fromRecord: "kotlin-adapter",
			want:       "kotlin-verifier",
		},
		{
			name:       "без meta берём attributionAgent из записи",
			line:       Line{ID: "agent-a1a73a07"},
			fromRecord: "kotlin-adapter",
			want:       "kotlin-adapter",
		},
		{
			name:       "нет ни meta, ни attributionAgent — короткий id",
			line:       Line{ID: "agent-a1a73a07"},
			fromRecord: SourceMain,
			want:       "agent-a1a73a07",
		},
		{
			name:       "пустая запись не подменяет id",
			line:       Line{ID: "agent-a1a73a07"},
			fromRecord: "",
			want:       "agent-a1a73a07",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.line.Source(tt.fromRecord); got != tt.want {
				t.Errorf("Source(%q)=%q, ожидалось %q", tt.fromRecord, got, tt.want)
			}
		})
	}
}

func TestWatchStreamsMainAndAgents(t *testing.T) {
	target := fakeTarget(t, "главная-1\n")
	writeAgent(t, target, "a1a73a076a75841a2", "агент-один\n", "kotlin-adapter")
	writeAgent(t, target, "b2b84815c27bcb83b", "агент-два\n", "kotlin-verifier")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := watchTest(ctx, target)
	got := collect(t, lines, 3, "главный поток и два агента")

	names := map[string]string{}
	for _, l := range got {
		names[string(l.Data)] = l.Source("")
	}
	want := map[string]string{
		"главная-1":  SourceMain,
		"агент-один": "kotlin-adapter",
		"агент-два":  "kotlin-verifier",
	}
	for data, wantName := range want {
		if names[data] != wantName {
			t.Errorf("строка %q пришла как %q, ожидалось %q", data, names[data], wantName)
		}
	}
}

// Ровно тот случай, ради которого затевался инструмент: главный поток молчит,
// а субагент пишет. Наблюдение за одним файлом показало бы тишину.
func TestWatchDeliversAgentWhileMainIsSilent(t *testing.T) {
	target := fakeTarget(t, "главная-до-делегирования\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := watchTest(ctx, target)
	collect(t, lines, 1, "строка главного потока")

	// Главный файл больше не растёт — как и в живой сессии с делегированием.
	agent := writeAgent(t, target, "c3c95926d38cdc94c", "", "kotlin-fixer")
	for i := range 3 {
		f, err := os.OpenFile(agent, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(f, "работа-агента-%d\n", i)
		f.Close()

		got := collect(t, lines, 1, "строка субагента при молчащем главном")
		if want := fmt.Sprintf("работа-агента-%d", i); string(got[0].Data) != want {
			t.Fatalf("получено %q, ожидалось %q", got[0].Data, want)
		}
		if got[0].Source("") != "kotlin-fixer" {
			t.Errorf("источник %q, ожидался kotlin-fixer", got[0].Source(""))
		}
	}
}

// Новый субагент появляется в середине сессии — его надо подхватить.
func TestWatchPicksUpNewAgentFile(t *testing.T) {
	target := fakeTarget(t, "главная\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := watchTest(ctx, target)
	collect(t, lines, 1, "главный поток")

	writeAgent(t, target, "d4da6a37e49ded05d", "новорождённый\n", "Explore")

	got := collect(t, lines, 1, "строка нового субагента")
	if string(got[0].Data) != "новорождённый" {
		t.Errorf("получено %q", got[0].Data)
	}
	if got[0].Source("") != "Explore" {
		t.Errorf("источник %q, ожидался Explore", got[0].Source(""))
	}
}

// Гонка из разведданных: .meta.json может быть ещё не дописан, когда первые
// строки агента уже пошли. Первые строки идут по attributionAgent, дальше — по meta.
func TestWatchMetaAppearsLater(t *testing.T) {
	target := fakeTarget(t, "главная\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := watchTest(ctx, target)
	collect(t, lines, 1, "главный поток")

	agent := writeAgent(t, target, "e5eb7b48f5aefe16e", "до-описания\n", "")
	got := collect(t, lines, 1, "строка агента без описания")
	if got[0].Name != "" {
		t.Errorf("Name=%q, описание ещё не дописано", got[0].Name)
	}
	if src := got[0].Source("kotlin-adapter"); src != "kotlin-adapter" {
		t.Errorf("источник %q, ожидался откат на attributionAgent", src)
	}

	// Описание доезжает: имя подхватывается в течение такта опроса каталога,
	// а уже отданные строки потребитель чинит по сигналу Renames.
	writeMeta(t, agent, "kotlin-verifier")
	time.Sleep(4 * testDirInterval)

	f, err := os.OpenFile(agent, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "после-описания")
	f.Close()

	after := collect(t, lines, 1, "строка агента после появления описания")
	if after[0].Name != "kotlin-verifier" {
		t.Errorf("Name=%q, ожидался kotlin-verifier", after[0].Name)
	}
}

func TestWatchSurvivesBrokenMeta(t *testing.T) {
	target := fakeTarget(t, "главная\n")
	agent := writeAgent(t, target, "f6fc8c59a6bfaf27f", "строка-агента\n", "")
	if err := os.WriteFile(MetaPath(agent), []byte("{битый json"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := watchTest(ctx, target)
	got := collect(t, lines, 2, "строки главного и агента")

	for _, l := range got {
		if string(l.Data) == "строка-агента" {
			if l.Name != "" {
				t.Errorf("Name=%q при битом описании", l.Name)
			}
			if l.Source("") != "agent-f6fc8c59" {
				t.Errorf("источник %q, ожидался короткий id", l.Source(""))
			}
		}
	}
}

// Сессия без делегирований: каталога subagents нет вовсе.
func TestWatchWithoutSubagentDir(t *testing.T) {
	target := fakeTarget(t, "одинокая\n")
	if err := os.RemoveAll(filepath.Dir(target.SubagentDir)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, errs := watchTest(ctx, target)
	collect(t, lines, 1, "строка главного потока")

	select {
	case err := <-errs:
		t.Errorf("отсутствие каталога субагентов выдано как ошибка: %v", err)
	case <-time.After(5 * testDirInterval):
	}
}

// Полигон: 44 субагента одновременно — столько их было в самой широкой
// сессии живого корпуса.
func TestWatchManyAgents(t *testing.T) {
	const agents = 44
	target := fakeTarget(t, "главная\n")
	for i := range agents {
		writeAgent(t, target, fmt.Sprintf("%017d", i),
			fmt.Sprintf("агент-%d-строка\n", i), fmt.Sprintf("тип-%d", i))
	}

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())

	lines, errs := watchTest(ctx, target)
	got := collect(t, lines, agents+1, "строки всех агентов и главного потока")

	seen := map[string]bool{}
	for _, l := range got {
		seen[l.Source("")] = true
	}
	if len(seen) != agents+1 {
		t.Errorf("различных источников %d, ожидалось %d", len(seen), agents+1)
	}

	cancel()
	for range lines {
	}
	for range errs {
	}

	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("горутин было %d, стало %d: тейлеры не остановлены", before, after)
	}
}

func TestWatchCancelClosesChannels(t *testing.T) {
	target := fakeTarget(t, "главная\n")
	writeAgent(t, target, "a1a73a076a75841a2", "агент\n", "Plan")

	ctx, cancel := context.WithCancel(context.Background())
	lines, errs := watchTest(ctx, target)
	collect(t, lines, 2, "обе строки")
	cancel()

	deadline := time.After(3 * time.Second)
	for lines != nil || errs != nil {
		select {
		case _, ok := <-lines:
			if !ok {
				lines = nil
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
		case <-deadline:
			t.Fatal("каналы не закрылись после отмены")
		}
	}
}

func TestReadAll(t *testing.T) {
	target := fakeTarget(t, "главная-1\nглавная-2\n")
	writeAgent(t, target, "a1a73a076a75841a2", "агент-один-1\nагент-один-2\n", "kotlin-adapter")
	writeAgent(t, target, "b2b84815c27bcb83b", "агент-два-1\n", "")

	got, _, err := ReadAll(target)
	if err != nil {
		t.Fatalf("ReadAll вернул ошибку: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("строк %d, ожидалось 5", len(got))
	}

	// Главный поток идёт первым, дальше агенты в порядке имён файлов.
	if string(got[0].Data) != "главная-1" || got[0].ID != SourceMain {
		t.Errorf("первая строка %q от %q", got[0].Data, got[0].ID)
	}

	var sources []string
	for _, l := range got {
		sources = append(sources, l.Source(""))
	}
	joined := strings.Join(sources, ",")
	if !strings.Contains(joined, "kotlin-adapter") {
		t.Errorf("имя из meta не подхвачено: %s", joined)
	}
	if !strings.Contains(joined, "agent-b2b84815") {
		t.Errorf("короткий id не подхвачен: %s", joined)
	}
}

func TestReadAllWithoutSubagents(t *testing.T) {
	target := fakeTarget(t, "одна\nдве\n")
	if err := os.RemoveAll(filepath.Dir(target.SubagentDir)); err != nil {
		t.Fatal(err)
	}

	got, _, err := ReadAll(target)
	if err != nil {
		t.Fatalf("ReadAll вернул ошибку: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("строк %d, ожидалось 2", len(got))
	}
}

// Каталог опрашивается редко, файлы часто: гонять ReadDir по пять раз в
// секунду при полусотне файлов незачем.
func TestWatchDirIntervalIsRarerThanFile(t *testing.T) {
	if DefaultDirInterval <= DefaultFileInterval {
		t.Errorf("каталог опрашивается не реже файлов: %v против %v",
			DefaultDirInterval, DefaultFileInterval)
	}
}

func TestShortAgentID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/x/subagents/agent-a1a73a076a75841a2.jsonl", "agent-a1a73a07"},
		{"/x/subagents/agent-short.jsonl", "agent-short"},
		{"/x/subagents/странный.jsonl", "странный"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := shortAgentID(tt.path); got != tt.want {
				t.Errorf("shortAgentID(%q)=%q, ожидалось %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestReadAllSortedAgentOrder(t *testing.T) {
	target := fakeTarget(t, "главная\n")
	for _, id := range []string{"c3", "a1", "b2"} {
		writeAgent(t, target, id, "строка-"+id+"\n", "")
	}

	got, _, err := ReadAll(target)
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, l := range got[1:] {
		ids = append(ids, string(l.Data))
	}
	want := []string{"строка-a1", "строка-b2", "строка-c3"}
	if !sort.StringsAreSorted(ids) || len(ids) != len(want) {
		t.Errorf("порядок агентов %v, ожидался детерминированный %v", ids, want)
	}
}
