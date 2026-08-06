package parse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// Дефолтный лимит bufio.Scanner — 64 КБ; на живых транскриптах он рвётся,
// поэтому чтение построено на bufio.Reader без такого потолка.
func TestScanReadsVeryLongLine(t *testing.T) {
	const payload = 900_000
	long := `{"type":"assistant","text":"` + strings.Repeat("x", payload) + `"}`

	var got [][]byte
	skipped, err := Scan(strings.NewReader(long+"\n"), func(line []byte) error {
		got = append(got, append([]byte(nil), line...))
		return nil
	})
	if err != nil {
		t.Fatalf("Scan вернул ошибку: %v", err)
	}
	if skipped != 0 {
		t.Errorf("пропущено %d строк, ожидалось 0", skipped)
	}

	if len(got) != 1 {
		t.Fatalf("строк прочитано %d, ожидалась 1", len(got))
	}
	if len(got[0]) != len(long) {
		t.Errorf("длина строки %d, ожидалось %d", len(got[0]), len(long))
	}
}

// Главное свойство: аномальная строка стоит самой себя, а не всего файла.
// Раньше строка сверх лимита останавливала разбор, и из файла не приходило
// ни одного события — ровно тот отказ, ради которого инструмент бесполезен.
func TestScanSkipsLineOverLimitAndKeepsReading(t *testing.T) {
	const limit = 1 << 10
	in := `{"a":1}` + "\n" +
		strings.Repeat("x", 4*limit) + "\n" +
		`{"b":2}` + "\n"

	var got []string
	skipped, err := scanLimit(strings.NewReader(in), limit, func(line []byte) error {
		got = append(got, string(line))
		return nil
	})

	if err != nil {
		t.Fatalf("Scan вернул ошибку %v, ожидался пропуск строки без ошибки", err)
	}
	if skipped != 1 {
		t.Errorf("пропущено %d строк, ожидалась 1", skipped)
	}

	want := []string{`{"a":1}`, `{"b":2}`}
	if len(got) != len(want) {
		t.Fatalf("строк %d (%q), ожидалось %d — строки вокруг длинной потеряны", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("строка %d = %q, ожидалась %q", i, got[i], want[i])
		}
	}
}

// Слишком длинная строка последняя и без перевода строки: пропуск должен
// быть учтён ровно один раз, а не потеряться и не задвоиться.
func TestScanSkipsOverLimitLineAtEOF(t *testing.T) {
	const limit = 1 << 10

	calls := 0
	skipped, err := scanLimit(strings.NewReader(strings.Repeat("x", 4*limit)), limit,
		func([]byte) error { calls++; return nil })

	if err != nil {
		t.Fatalf("Scan вернул ошибку: %v", err)
	}
	if skipped != 1 {
		t.Errorf("пропущено %d строк, ожидалась 1", skipped)
	}
	if calls != 0 {
		t.Errorf("колбэк вызван %d раз на слишком длинной строке", calls)
	}
}

// Строка ровно в лимит проходит: перевод строки в длину не входит.
func TestScanAcceptsLineExactlyAtLimit(t *testing.T) {
	const limit = 1 << 10
	line := strings.Repeat("x", limit)

	got := 0
	skipped, err := scanLimit(strings.NewReader(line+"\n"), limit, func(b []byte) error {
		got = len(b)
		return nil
	})
	if err != nil {
		t.Fatalf("Scan вернул ошибку: %v", err)
	}
	if skipped != 0 {
		t.Errorf("пропущено %d строк, ожидалось 0", skipped)
	}
	if got != limit {
		t.Errorf("прочитано %d байт, ожидалось %d", got, limit)
	}
}

func TestScanLimitCoversRealTranscripts(t *testing.T) {
	// Замеренный максимум живого корпуса — 9 993 440 байт, в файле субагента.
	const measuredMax = 9_993_440
	if MaxLineBytes < 2*measuredMax {
		t.Errorf("MaxLineBytes=%d даёт меньше двукратного запаса к замеренным %d байт",
			MaxLineBytes, measuredMax)
	}
}

func TestScanSkipsEmptyLines(t *testing.T) {
	in := "\n{\"a\":1}\n\n   \n{\"b\":2}\n"

	var got []string
	if _, err := Scan(strings.NewReader(in), func(line []byte) error {
		got = append(got, string(line))
		return nil
	}); err != nil {
		t.Fatalf("Scan вернул ошибку: %v", err)
	}

	want := []string{`{"a":1}`, `{"b":2}`}
	if len(got) != len(want) {
		t.Fatalf("строк %d (%q), ожидалось %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("строка %d = %q, ожидалась %q", i, got[i], want[i])
		}
	}
}

func TestScanReadsLastLineWithoutNewline(t *testing.T) {
	var got []string
	if _, err := Scan(strings.NewReader("{\"a\":1}\n{\"b\":2}"), func(line []byte) error {
		got = append(got, string(line))
		return nil
	}); err != nil {
		t.Fatalf("Scan вернул ошибку: %v", err)
	}
	if len(got) != 2 || got[1] != `{"b":2}` {
		t.Fatalf("получено %q, ожидались обе строки", got)
	}
}

// Строка, не влезающая в буфер чтения, склеивается из кусков без потери байт.
func TestScanJoinsChunksAcrossBufferBoundary(t *testing.T) {
	const limit = 1 << 20
	first := strings.Repeat("a", 3*readBuf/2)
	in := first + "\n" + `{"b":2}` + "\n"

	var got []string
	skipped, err := scanLimit(strings.NewReader(in), limit, func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("Scan вернул ошибку: %v", err)
	}
	if skipped != 0 {
		t.Errorf("пропущено %d строк, ожидалось 0", skipped)
	}
	if len(got) != 2 {
		t.Fatalf("строк %d, ожидалось 2", len(got))
	}
	if got[0] != first {
		t.Errorf("длинная строка склеена неверно: %d байт вместо %d", len(got[0]), len(first))
	}
	if got[1] != `{"b":2}` {
		t.Errorf("вторая строка %q", got[1])
	}
}

func TestScanEmptyInput(t *testing.T) {
	calls := 0
	if _, err := Scan(strings.NewReader(""), func([]byte) error { calls++; return nil }); err != nil {
		t.Fatalf("Scan вернул ошибку: %v", err)
	}
	if calls != 0 {
		t.Errorf("колбэк вызван %d раз, ожидалось 0", calls)
	}
}

// Сбой чтения обязан дойти наверх. Принять его за конец файла — значит
// показать усечённую сессию как полную, не сказав об этом ни слова.
func TestScanPropagatesReadError(t *testing.T) {
	boom := errors.New("диск отвалился")

	var got []string
	skipped, err := Scan(io.MultiReader(strings.NewReader(`{"a":1}`+"\n"), errReader{boom}),
		func(line []byte) error {
			got = append(got, string(line))
			return nil
		})

	if !errors.Is(err, boom) {
		t.Fatalf("ошибка %v, ожидалась %v", err, boom)
	}
	if len(got) != 1 || got[0] != `{"a":1}` {
		t.Errorf("прочитано %q, ожидалась одна строка до сбоя", got)
	}
	if skipped != 0 {
		t.Errorf("пропущено %d строк, ожидалось 0", skipped)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestScanStopsOnCallbackError(t *testing.T) {
	boom := errors.New("стоп")
	calls := 0

	_, err := Scan(strings.NewReader("{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n"), func([]byte) error {
		calls++
		return boom
	})

	if !errors.Is(err, boom) {
		t.Fatalf("ошибка %v, ожидалась %v", err, boom)
	}
	if calls != 1 {
		t.Errorf("колбэк вызван %d раз, ожидался 1", calls)
	}
}
