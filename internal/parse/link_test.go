package parse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// at — отметка времени вызова с точностью до миллисекунды.
func at(ms int) time.Time {
	return time.Date(2026, 8, 4, 10, 0, 0, ms*int(time.Millisecond), time.UTC)
}

// Исходы сшивки: успех во всех трёх видах is_error, сбой инструмента, отказ
// правила, сирота и дубль. Отказ отделён от ошибки намеренно: 85 из 252
// ошибок живого корпуса — это отказы, и слитый исход врал бы втрое.
func TestLinkerResolve(t *testing.T) {
	tests := []struct {
		name       string
		track      bool // ставить ли вызов на учёт до результата
		result     Result
		wantOK     bool
		wantStatus Status
		wantDur    time.Duration
		wantFail   string
	}{
		{
			name: "is_error отсутствует — успех", track: true,
			result: Result{ToolUseID: "t1", Time: at(400), Text: "ok"},
			wantOK: true, wantStatus: StatusOK, wantDur: 400 * time.Millisecond,
		},
		{
			name: "is_error false — успех", track: true,
			result: Result{ToolUseID: "t1", Time: at(94), IsError: false, Text: "80\tfunc x() {"},
			wantOK: true, wantStatus: StatusOK, wantDur: 94 * time.Millisecond,
		},
		{
			name: "is_error true без отказа — сбой инструмента", track: true,
			result: Result{ToolUseID: "t1", Time: at(644), IsError: true, Text: "upstream: status 429"},
			wantOK: true, wantStatus: StatusError, wantDur: 644 * time.Millisecond,
			wantFail: "upstream: status 429",
		},
		{
			name: "is_error true с permission-rule — отказ, а не сбой", track: true,
			result: Result{ToolUseID: "t1", Time: at(120), IsError: true, Denial: "permission-rule",
				Text: "[Policy Gate]"},
			wantOK: true, wantStatus: StatusDenied, wantDur: 120 * time.Millisecond,
			wantFail: "[Policy Gate]",
		},
		{
			name: "user-rejected — тоже отказ", track: true,
			result: Result{ToolUseID: "t1", Time: at(3), IsError: true, Denial: "user-rejected",
				Text: "The user doesn't want to proceed with this tool use"},
			wantOK: true, wantStatus: StatusDenied, wantDur: 3 * time.Millisecond,
			wantFail: "The user doesn't want to proceed with this tool use",
		},
		{
			name: "успех с отказом в записи — всё равно успех", track: true,
			result: Result{ToolUseID: "t1", Time: at(10), Denial: "permission-rule", Text: "готово"},
			wantOK: true, wantStatus: StatusOK, wantDur: 10 * time.Millisecond,
		},
		{
			name: "обёртка tool_use_error снимается", track: true,
			result: Result{ToolUseID: "t1", Time: at(36), IsError: true,
				Text: "<tool_use_error>File has not been read yet.</tool_use_error>"},
			wantOK: true, wantStatus: StatusError, wantDur: 36 * time.Millisecond,
			wantFail: "File has not been read yet.",
		},
		{
			name: "сирота — сшивать не с чем", track: false,
			result: Result{ToolUseID: "t1", Time: at(400), Text: "ok"},
			wantOK: false,
		},
		{
			name: "результат без ключа сшивки", track: true,
			result: Result{ToolUseID: "", Time: at(400), Text: "ok"},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLinker()
			if tt.track {
				l.Track(Event{ToolID: "t1", Time: at(0)})
			}

			u, ok := l.Resolve(tt.result)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, ожидалось %v (update %+v)", ok, tt.wantOK, u)
			}
			if !ok {
				if u != (Update{}) {
					t.Errorf("несшитый результат вернул %+v, ожидалось пустое обновление", u)
				}
				return
			}
			if u.ToolID != tt.result.ToolUseID {
				t.Errorf("ToolID=%q, ожидался %q", u.ToolID, tt.result.ToolUseID)
			}
			if u.Status != tt.wantStatus {
				t.Errorf("Status=%v, ожидался %v", u.Status, tt.wantStatus)
			}
			if u.Dur != tt.wantDur {
				t.Errorf("Dur=%v, ожидалась %v", u.Dur, tt.wantDur)
			}
			if u.Fail != tt.wantFail {
				t.Errorf("Fail=%q, ожидалось %q", u.Fail, tt.wantFail)
			}
		})
	}
}

// Дубль результата в корпусе есть: одна и та же запись лежит в файле дважды.
// Второй раз он не должен ни сшиваться, ни считаться сиротой-новостью.
func TestLinkerResolveIsIdempotent(t *testing.T) {
	l := NewLinker()
	l.Track(Event{ToolID: "toolu_dup", Time: at(0)})
	r := Result{ToolUseID: "toolu_dup", Time: at(32), Text: "готово"}

	first, ok := l.Resolve(r)
	if !ok {
		t.Fatalf("первый результат не сшился")
	}
	second, ok := l.Resolve(r)
	if ok {
		t.Errorf("дубль результата сшился второй раз: %+v", second)
	}
	if second != (Update{}) {
		t.Errorf("дубль вернул %+v, ожидалось пустое обновление", second)
	}
	if first.Status != StatusOK || first.Dur != 32*time.Millisecond {
		t.Errorf("первое обновление испорчено: %+v", first)
	}
	if l.open() != 0 {
		t.Errorf("закрытый вызов остался в карте: открыто %d", l.open())
	}
}

// Событие без ключа сшивки на учёт не встаёт: реплики и системные записи
// результата не получают, и место в карте им ни к чему.
func TestLinkerTrackIgnoresEventsWithoutToolID(t *testing.T) {
	l := NewLinker()
	l.Track(Event{Kind: KindText, Detail: "реплика", Time: at(0)})
	l.Track(Event{Kind: KindSystem, Detail: "система", Time: at(0)})

	if l.open() != 0 {
		t.Errorf("на учёт встало %d событий без ключа сшивки", l.open())
	}
	// Пустой ключ не должен сшиваться сам с собой.
	if u, ok := l.Resolve(Result{ToolUseID: "", Time: at(1)}); ok {
		t.Errorf("результат без ключа сшился: %+v", u)
	}
}

// Переигранная история даёт второй вызов с тем же ключом: время берётся от
// последней записи, а карта не раздувается дублем.
func TestLinkerTrackTwiceKeepsLastTime(t *testing.T) {
	l := NewLinker()
	l.Track(Event{ToolID: "t1", Time: at(0)})
	l.Track(Event{ToolID: "t1", Time: at(1000)})

	if l.open() != 1 {
		t.Fatalf("в карте %d записей, ожидалась одна", l.open())
	}
	u, ok := l.Resolve(Result{ToolUseID: "t1", Time: at(1500)})
	if !ok {
		t.Fatalf("вызов не сшился")
	}
	if u.Dur != 500*time.Millisecond {
		t.Errorf("Dur=%v, ожидалось 500ms от ПОСЛЕДНЕЙ записи вызова", u.Dur)
	}
}

// Длительность считается по отметкам записей; кривые отметки дают ноль
// («не известна»), а не отрицательное число и не панику.
func TestLinkerDuration(t *testing.T) {
	tests := []struct {
		name     string
		use      time.Time
		done     time.Time
		wantDur  time.Duration
		wantZero bool
	}{
		{name: "обычная", use: at(0), done: at(378), wantDur: 378 * time.Millisecond},
		{name: "долгий вызов человека", use: at(0),
			done: at(0).Add(15*time.Hour + 26*time.Minute), wantDur: 15*time.Hour + 26*time.Minute},
		{name: "время вызова не известно", use: time.Time{}, done: at(378), wantZero: true},
		{name: "время результата не известно", use: at(0), done: time.Time{}, wantZero: true},
		{name: "отметки в обратном порядке", use: at(500), done: at(100), wantZero: true},
		{name: "отметки совпали", use: at(100), done: at(100), wantZero: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLinker()
			l.Track(Event{ToolID: "t1", Time: tt.use})

			u, ok := l.Resolve(Result{ToolUseID: "t1", Time: tt.done})
			if !ok {
				t.Fatalf("вызов не сшился")
			}
			want := tt.wantDur
			if tt.wantZero {
				want = 0
			}
			if u.Dur != want {
				t.Errorf("Dur=%v, ожидалось %v", u.Dur, want)
			}
		})
	}
}

// Текст ошибки обрезается: содержимое результата доходит до сотен килобайт,
// а в строке окна ему места нет.
func TestLinkerFailIsBounded(t *testing.T) {
	l := NewLinker()
	l.Track(Event{ToolID: "t1", Time: at(0)})

	long := "<tool_use_error>" + strings.Repeat("ошибка ", 200) + "</tool_use_error>"
	u, ok := l.Resolve(Result{ToolUseID: "t1", Time: at(10), IsError: true, Text: long})
	if !ok {
		t.Fatalf("вызов не сшился")
	}
	if n := len([]rune(u.Fail)); n > maxError+1 {
		t.Errorf("длина текста ошибки %d рун, потолок %d", n, maxError)
	}
	if !strings.HasPrefix(u.Fail, "ошибка") {
		t.Errorf("обёртка не снята: %q", u.Fail)
	}

	// Успешный результат текста ошибки не получает, каким бы ни было
	// содержимое: вывод упавшей команды Bash — исход команды, а не вызова.
	l.Track(Event{ToolID: "t2", Time: at(0)})
	u, _ = l.Resolve(Result{ToolUseID: "t2", Time: at(10), Text: "Error: unknown column code: A"})
	if u.Fail != "" {
		t.Errorf("успех получил текст ошибки: %q", u.Fail)
	}
}

// Потолок карты страхует от патологии (обрезанный файл, ручная правка):
// вытесняются самые ранние вызовы, остальные сшиваются как ни в чём не бывало.
func TestLinkerPendingCeiling(t *testing.T) {
	l := NewLinker()
	const extra = 3
	for i := range maxPending + extra {
		l.Track(Event{ToolID: fmt.Sprintf("toolu_%05d", i), Time: at(i)})
	}

	if l.open() != maxPending {
		t.Errorf("в карте %d вызовов, потолок %d", l.open(), maxPending)
	}
	for i := range extra {
		id := fmt.Sprintf("toolu_%05d", i)
		if _, ok := l.Resolve(Result{ToolUseID: id, Time: at(maxPending + extra)}); ok {
			t.Errorf("самый ранний вызов %s пережил вытеснение", id)
		}
	}
	// Всё, что моложе вытесненных, сшивается.
	for _, i := range []int{extra, maxPending / 2, maxPending + extra - 1} {
		id := fmt.Sprintf("toolu_%05d", i)
		if _, ok := l.Resolve(Result{ToolUseID: id, Time: at(maxPending + extra)}); !ok {
			t.Errorf("вызов %s не сшился, хотя вытеснению не подлежал", id)
		}
	}
}

// Очередь вытеснения не должна расти вместе с длиной сессии: закрытые вызовы
// оставляют в ней свои ключи, и без чистки она копила бы их годами.
func TestLinkerQueueDoesNotGrowWithSession(t *testing.T) {
	l := NewLinker()
	for i := range 5 * maxPending {
		id := fmt.Sprintf("toolu_%05d", i)
		l.Track(Event{ToolID: id, Time: at(i)})
		if _, ok := l.Resolve(Result{ToolUseID: id, Time: at(i + 1)}); !ok {
			t.Fatalf("вызов %s не сшился", id)
		}
	}

	if l.open() != 0 {
		t.Errorf("открытых вызовов %d, ожидалось 0", l.open())
	}
	if got := len(l.order); got > 2*maxPending {
		t.Errorf("очередь вытеснения выросла до %d ключей при нуле открытых вызовов", got)
	}
}

// Результат раньше своего вызова обязан не падать и не заводить фантомную
// запись: показать её всё равно нечем. Инвариант «Track всей пачки до Resolve»
// держит модель интерфейса, а линкер обязан пережить и его нарушение.
func TestLinkerResultBeforeUse(t *testing.T) {
	l := NewLinker()
	r := Result{ToolUseID: "t1", Time: at(100), Text: "готово"}

	if u, ok := l.Resolve(r); ok {
		t.Fatalf("результат сшился до вызова: %+v", u)
	}
	if l.open() != 0 {
		t.Errorf("несшитый результат завёл фантомную запись: открыто %d", l.open())
	}

	// Порядок восстановлен — сшивка работает.
	l.Track(Event{ToolID: "t1", Time: at(0)})
	if _, ok := l.Resolve(r); !ok {
		t.Errorf("вызов не сшился после Track")
	}
}

// Сшивка на записях живого корпуса: пары успех/ошибка,
// отказ, незакрытый вызов и дубль результата — одной фикстурой на все слои.
func TestLinkerOnFixture(t *testing.T) {
	events, results := decodeToolsFixture(t)

	l := NewLinker()
	// Инвариант порядка: сначала на учёт встают ВСЕ вызовы, потом сшиваются
	// результаты. Тогда пересортировка архива ничего не ломает.
	for _, ev := range events {
		l.Track(ev)
	}

	got := make(map[string]Update, len(results))
	resolved := 0
	for _, r := range results {
		u, ok := l.Resolve(r)
		if !ok {
			continue
		}
		if _, twice := got[u.ToolID]; twice {
			t.Errorf("вызов %s сшился дважды", u.ToolID)
		}
		got[u.ToolID] = u
		resolved++
	}

	// Результатов в фикстуре шесть, но один из них — дубль (строки 2 и 13).
	if resolved != 5 {
		t.Errorf("сшито %d вызовов, ожидалось 5 (шестой результат — дубль)", resolved)
	}

	want := []struct {
		id     string
		status Status
		dur    time.Duration
		fail   string
	}{
		{"toolu_01Aw6e6LSYvQvwsVL2ngbZ1y", StatusOK, 32 * time.Millisecond, ""},
		{"toolu_01VXZugnrTEX6ZRYAZhVXueY", StatusError, 644 * time.Millisecond,
			"netscan: roaming path upstream query: upstream: status 429 Too Many Requests: too many outstanding requests"},
		{"toolu_016XrbPAAqTjo9UMTWnfPQLJ", StatusError, 36 * time.Millisecond,
			"File has not been read yet. Read it first before writing to it."},
		{"toolu_01Read0000000000000001", StatusOK, 94 * time.Millisecond, ""},
		{"toolu_01Deny0000000000000001", StatusDenied, 120 * time.Millisecond,
			"[Policy Gate] Before creating /home/user/Devs/proj/README.md, present these facts:"},
	}
	for _, w := range want {
		u, ok := got[w.id]
		if !ok {
			t.Errorf("вызов %s не сшился вовсе", w.id)
			continue
		}
		if u.Status != w.status {
			t.Errorf("%s: Status=%v, ожидался %v", w.id, u.Status, w.status)
		}
		if u.Dur != w.dur {
			t.Errorf("%s: Dur=%v, ожидалась %v", w.id, u.Dur, w.dur)
		}
		if u.Fail != w.fail {
			t.Errorf("%s: Fail=%q, ожидалось %q", w.id, u.Fail, w.fail)
		}
	}

	// Незакрытые вызовы фикстуры так и остаются без исхода: прерванный
	// AskUserQuestion и Read с битыми аргументами результата не получили.
	for _, id := range []string{"toolu_01LxKLTGtabXBsSbWfzjGg3F", "toolu_01CFJBuxvu6u73HMc3xkib2J"} {
		if u, ok := got[id]; ok {
			t.Errorf("незакрытый вызов %s получил исход %+v", id, u)
		}
	}
	if l.open() != 2 {
		t.Errorf("открытых вызовов %d, ожидалось 2 (незакрытые фикстуры)", l.open())
	}
}

// decodeToolsFixture разбирает testdata/tools.jsonl целиком: события и
// результаты в порядке чтения файла.
func decodeToolsFixture(t *testing.T) ([]Event, []Result) {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "testdata", "tools.jsonl"))
	if err != nil {
		t.Fatalf("фикстура не открылась: %v", err)
	}
	defer func() { _ = f.Close() }()

	var events []Event
	var results []Result
	skipped, err := Scan(f, func(line []byte) error {
		d, ok := Decode(line)
		if !ok {
			t.Errorf("строка фикстуры не разобралась: %s", line)
			return nil
		}
		events = append(events, d.Events...)
		results = append(results, d.Results...)
		return nil
	})
	if err != nil {
		t.Fatalf("чтение фикстуры: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("пропущено строк: %d", skipped)
	}
	return events, results
}
