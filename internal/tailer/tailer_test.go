package tailer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Тесты гоняют тейлер на быстром интервале: поведение от периода не зависит,
// а ждать по 200 мс на каждое событие незачем.
const testInterval = 10 * time.Millisecond

// waitLine ждёт одну строку из канала с внятным таймаутом.
func waitLine(t *testing.T, lines <-chan []byte, what string) string {
	t.Helper()
	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatalf("канал строк закрыт, ожидалось %s", what)
		}
		return string(line)
	case <-time.After(2 * time.Second):
		t.Fatalf("строка не пришла за 2 с: %s", what)
		return ""
	}
}

// appendTo дописывает кусок в файл, как это делает Claude Code.
func appendTo(t *testing.T, path, chunk string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(chunk); err != nil {
		t.Fatal(err)
	}
	// Это запись: ошибка закрытия означала бы недописанные данные, и тест на
	// таком файле проверял бы не то, что задумано.
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func tempFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "live.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Файл читается с начала: сводка расхода считается по всей сессии.
func TestTailReadsExistingContent(t *testing.T) {
	path := tempFile(t, "первая\nвторая\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))

	if got := waitLine(t, lines, "первая"); got != "первая" {
		t.Errorf("получено %q", got)
	}
	if got := waitLine(t, lines, "вторая"); got != "вторая" {
		t.Errorf("получено %q", got)
	}
}

func TestTailReadsAppendedLines(t *testing.T) {
	path := tempFile(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))

	for i := range 5 {
		want := fmt.Sprintf("строка-%d", i)
		appendTo(t, path, want+"\n")

		start := time.Now()
		if got := waitLine(t, lines, want); got != want {
			t.Fatalf("получено %q, ожидалось %q", got, want)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("строка шла %v, ожидалось меньше секунды", d)
		}
	}
}

// Главный тест слайса: запись JSONL не атомарна, опрос попадает в середину
// строки. Наверх отдаётся только целая строка и ровно один раз.
func TestTailJoinsPartialLine(t *testing.T) {
	path := tempFile(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))

	appendTo(t, path, `{"type":"assis`)
	time.Sleep(5 * testInterval) // тейлер обязан пройти по неполной строке не раз

	select {
	case got := <-lines:
		t.Fatalf("отдана неполная строка %q", got)
	default:
	}

	appendTo(t, path, `tant","id":1}`+"\n")
	if got := waitLine(t, lines, "склеенная строка"); got != `{"type":"assistant","id":1}` {
		t.Errorf("получено %q", got)
	}

	// И ровно один раз: хвост не должен продублироваться.
	appendTo(t, path, "следующая\n")
	if got := waitLine(t, lines, "следующая"); got != "следующая" {
		t.Errorf("получено %q, дубль предыдущей строки?", got)
	}
}

// Усечение определяется по размеру, а не по времени изменения.
func TestTailResetsOnTruncate(t *testing.T) {
	path := tempFile(t, "старая-1\nстарая-2\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, errs := Tail(ctx, path, WithInterval(testInterval))
	waitLine(t, lines, "старая-1")
	waitLine(t, lines, "старая-2")

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	appendTo(t, path, "новая\n")

	if got := waitLine(t, lines, "новая"); got != "новая" {
		t.Errorf("получено %q, смещение не сброшено", got)
	}
	select {
	case err := <-errs:
		t.Errorf("усечение выдано как ошибка: %v", err)
	default:
	}
}

// Перезапись файла НА МЕСТЕ до того же самого размера: по размеру такое не
// отличить от простоя, и без отдельной проверки новые строки теряются молча.
func TestTailDetectsSameSizeRewrite(t *testing.T) {
	path := tempFile(t, "старая-строка\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))
	waitLine(t, lines, "старая-строка")

	// Ровно та же длина в байтах, другое содержимое.
	replacement := "новая-строкаА\n"
	if len(replacement) != len("старая-строка\n") {
		t.Fatalf("подготовка теста: длины не совпали, %d против %d",
			len(replacement), len("старая-строка\n"))
	}
	time.Sleep(2 * testInterval) // чтобы ModTime заведомо отличался
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := waitLine(t, lines, "новая-строкаА"); got != "новая-строкаА" {
		t.Errorf("получено %q, перезапись потеряна", got)
	}
}

// Перезапись на БОЛЬШИЙ размер: чтение со старого смещения уехало бы в
// середину строки и отдало обрывок.
func TestTailDetectsLargerRewrite(t *testing.T) {
	path := tempFile(t, "коротко\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))
	waitLine(t, lines, "коротко")

	time.Sleep(2 * testInterval)
	if err := os.WriteFile(path, []byte("совсем другое содержимое подлиннее\nвторая\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := waitLine(t, lines, "первая строка после перезаписи"); got != "совсем другое содержимое подлиннее" {
		t.Errorf("получено %q — это обрывок, а не целая строка", got)
	}
	if got := waitLine(t, lines, "вторая"); got != "вторая" {
		t.Errorf("получено %q", got)
	}
}

// Подмена файла по rename: inode другой, размер может совпасть.
func TestTailDetectsReplacedFile(t *testing.T) {
	path := tempFile(t, "из первого файла\n")
	dir := filepath.Dir(path)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))
	waitLine(t, lines, "из первого файла")

	other := filepath.Join(dir, "другой.jsonl")
	if err := os.WriteFile(other, []byte("из второго файла\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}

	if got := waitLine(t, lines, "из второго файла"); got != "из второго файла" {
		t.Errorf("получено %q, подмена файла не замечена", got)
	}
}

// Опрос может разрезать многобайтовый символ пополам: склейка байтовая,
// и это обязано оставаться верным.
func TestTailJoinsSplitUTF8Rune(t *testing.T) {
	path := tempFile(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))

	full := []byte("мир\n")
	appendTo(t, path, string(full[:1])) // половина буквы «м»
	time.Sleep(3 * testInterval)
	appendTo(t, path, string(full[1:]))

	if got := waitLine(t, lines, "мир"); got != "мир" {
		t.Errorf("получено %q, руна разъехалась", got)
	}
}

// Строка длиннее лимита, целиком уместившаяся в один кусок чтения, тоже
// обязана отвергаться: иначе опция обещает не то, что делает.
func TestTailLineTooLongWithinSingleChunk(t *testing.T) {
	path := tempFile(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const limit = 1 << 10
	lines, errs := Tail(ctx, path, WithInterval(testInterval), WithMaxLine(limit))

	appendTo(t, path, strings.Repeat("z", 4*limit)+"\nнормальная\n")

	select {
	case err := <-errs:
		if !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("ошибка %v, ожидалась ErrLineTooLong", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ошибка о длинной строке не пришла")
	}

	if got := waitLine(t, lines, "нормальная"); got != "нормальная" {
		t.Errorf("получено %q длиной %d", got[:min(len(got), 20)], len(got))
	}
}

// Горутина тейлера обязана уходить: в S5 их будет полсотни.
func TestTailLeavesNoGoroutines(t *testing.T) {
	path := tempFile(t, "раз\n")
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	lines, errs := Tail(ctx, path, WithInterval(testInterval))
	waitLine(t, lines, "раз")
	cancel()

	for range lines { //nolint:revive // дочитываем до закрытия
	}
	for range errs {
	}

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("горутин было %d, стало %d", before, after)
	}
}

// Отмена обязана прерывать и внутренний цикл дочитывания: поток из пустых
// строк не зовёт send, и без явной проверки горутина дожёвывала бы файл.
func TestTailCancelStopsInnerLoop(t *testing.T) {
	path := tempFile(t, strings.Repeat("\n", 200_000))
	ctx, cancel := context.WithCancel(context.Background())

	lines, _ := Tail(ctx, path, WithInterval(time.Hour))
	cancel()

	select {
	case _, ok := <-lines:
		if ok {
			t.Errorf("после отмены пришла строка")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("горутина не завершилась после отмены: дочитывает файл")
	}
}

// Субагент только появился — файла ещё нет. Это норма, а не ошибка.
func TestTailWaitsForMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "позже.jsonl")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, errs := Tail(ctx, path, WithInterval(testInterval))

	time.Sleep(5 * testInterval)
	select {
	case err := <-errs:
		t.Fatalf("отсутствие файла выдано как ошибка: %v", err)
	default:
	}

	appendTo(t, path, "родился\n")
	if got := waitLine(t, lines, "родился"); got != "родился" {
		t.Errorf("получено %q", got)
	}
}

// В S5 таких тейлеров будет до полусотни: незакрытый канал или повисшая
// горутина там превращаются в утечку памяти.
func TestTailStopsOnContextCancel(t *testing.T) {
	path := tempFile(t, "раз\n")
	ctx, cancel := context.WithCancel(t.Context())

	lines, errs := Tail(ctx, path, WithInterval(testInterval))
	waitLine(t, lines, "раз")
	cancel()

	deadline := time.After(2 * time.Second)
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
			t.Fatal("каналы не закрылись после отмены контекста")
		}
	}
}

// Отмена не должна зависать, даже если строки никто не читает.
func TestTailCancelWithUnreadLines(t *testing.T) {
	path := tempFile(t, strings.Repeat("строка\n", 100))
	ctx, cancel := context.WithCancel(t.Context())

	lines, _ := Tail(ctx, path, WithInterval(testInterval))
	time.Sleep(3 * testInterval)
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("тейлер завис на неотданных строках")
		}
	}
}

// Бесконечно растущий carry — тот же OOM, что и в парсере, только с другой
// стороны. Лимит обязателен, а тейлер обязан пережить его и работать дальше.
func TestTailLineTooLong(t *testing.T) {
	path := tempFile(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const limit = 1 << 10
	lines, errs := Tail(ctx, path, WithInterval(testInterval), WithMaxLine(limit))

	appendTo(t, path, strings.Repeat("x", 4*limit))

	select {
	case err := <-errs:
		if !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("ошибка %v, ожидалась ErrLineTooLong", err)
		}
		if !strings.Contains(err.Error(), filepath.Base(path)) {
			t.Errorf("в тексте ошибки нет имени файла: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ошибка о длинной строке не пришла")
	}

	// Хвост чудовищной строки отбрасывается, следующая строка приходит целой.
	appendTo(t, path, "хвост-чудовища\nнормальная\n")
	if got := waitLine(t, lines, "нормальная"); got != "нормальная" {
		t.Errorf("получено %q, ожидалась нормальная", got)
	}
}

// Строка в 900 КБ — реальность живого корпуса (замерено 872 753 байта).
func TestTailLongLineWithinLimit(t *testing.T) {
	path := tempFile(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))

	long := strings.Repeat("y", 900_000)
	appendTo(t, path, long[:400_000])
	appendTo(t, path, long[400_000:]+"\n")

	if got := waitLine(t, lines, "длинная строка"); got != long {
		t.Errorf("длина полученной строки %d, ожидалось %d", len(got), len(long))
	}
}

func TestTailPreservesOrderOnBulkAppend(t *testing.T) {
	path := tempFile(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))

	var bulk strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&bulk, "строка-%d\n", i)
	}
	appendTo(t, path, bulk.String())

	for i := range 1000 {
		want := fmt.Sprintf("строка-%d", i)
		if got := waitLine(t, lines, want); got != want {
			t.Fatalf("получено %q, ожидалось %q", got, want)
		}
	}
}

// Пустые строки до парсера не доезжают: он их всё равно отбрасывает.
func TestTailSkipsBlankLines(t *testing.T) {
	path := tempFile(t, "\n   \nсодержимое\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))
	if got := waitLine(t, lines, "содержимое"); got != "содержимое" {
		t.Errorf("получено %q", got)
	}
}

// Файл с окончаниями CRLF не должен тащить \r в парсер.
func TestTailTrimsCarriageReturn(t *testing.T) {
	path := tempFile(t, "виндовая\r\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))
	if got := waitLine(t, lines, "виндовая"); got != "виндовая" {
		t.Errorf("получено %q", got)
	}
}

// Простаивающий файл не должен порождать ни событий, ни ошибок.
func TestTailQuietWhileIdle(t *testing.T) {
	path := tempFile(t, "одна\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, errs := Tail(ctx, path, WithInterval(testInterval))
	waitLine(t, lines, "одна")

	select {
	case line := <-lines:
		t.Errorf("на простое пришла строка %q", line)
	case err := <-errs:
		t.Errorf("на простое пришла ошибка %v", err)
	case <-time.After(20 * testInterval):
	}
}

// Отданная строка принадлежит получателю: тейлер не должен переиспользовать
// буфер под следующей строкой.
func TestTailLinesAreIndependentCopies(t *testing.T) {
	path := tempFile(t, "первая\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines, _ := Tail(ctx, path, WithInterval(testInterval))

	first := <-lines
	kept := string(first)
	appendTo(t, path, "вторая-длиннее\n")
	waitLine(t, lines, "вторая-длиннее")

	if string(first) != kept {
		t.Errorf("прежняя строка испортилась: %q против %q", first, kept)
	}
}
