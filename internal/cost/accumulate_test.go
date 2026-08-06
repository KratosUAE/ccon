package cost

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KratosUAE/ccon/internal/parse"
)

func loadUsage(t *testing.T, name string) []parse.Usage {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("фикстура не открылась: %v", err)
	}
	defer func() { _ = f.Close() }()

	var out []parse.Usage
	_, err = parse.Scan(f, func(line []byte) error {
		if d, ok := parse.Decode(line); ok && d.Usage != nil {
			out = append(out, *d.Usage)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("чтение фикстуры: %v", err)
	}
	return out
}

func sum(t *testing.T, samples []parse.Usage) Totals {
	t.Helper()
	acc := NewAccumulator()
	for _, u := range samples {
		acc.Add(u)
	}
	return acc.Totals()
}

func checkTotals(t *testing.T, got Totals, in, out, cr, c5m, c1h int64, requests int) {
	t.Helper()
	if got.Input != in {
		t.Errorf("Input=%d, ожидалось %d", got.Input, in)
	}
	if got.Output != out {
		t.Errorf("Output=%d, ожидалось %d", got.Output, out)
	}
	if got.CacheRead != cr {
		t.Errorf("CacheRead=%d, ожидалось %d", got.CacheRead, cr)
	}
	if got.Cache5m != c5m {
		t.Errorf("Cache5m=%d, ожидалось %d", got.Cache5m, c5m)
	}
	if got.Cache1h != c1h {
		t.Errorf("Cache1h=%d, ожидалось %d", got.Cache1h, c1h)
	}
	if got.CacheCreate() != c5m+c1h {
		t.Errorf("CacheCreate()=%d, ожидалось %d", got.CacheCreate(), c5m+c1h)
	}
	if got.Requests != requests {
		t.Errorf("Requests=%d, ожидалось %d", got.Requests, requests)
	}
}

// Главный тест слайса: строки одной логической реплики несут одинаковый
// промежуточный usage, суммировать их нельзя.
func TestAccumulatorDedupSynthetic(t *testing.T) {
	got := sum(t, loadUsage(t, "dup-usage.jsonl"))

	// Группа req_A: 4 строки, победитель — последняя (779 выходных токенов).
	// Группа req_B: одна строка. Наивная сумма дала бы output 909, input 17.
	checkTotals(t, got, 8, 879, 1020, 500, 0, 2)

	if len(got.Models) != 1 || got.Models[0].Model != "claude-opus-5" || got.Models[0].Count != 2 {
		t.Errorf("Models=%+v, ожидалось [claude-opus-5 ×2]", got.Models)
	}
	if got.Unknown {
		t.Errorf("Unknown=true при известной модели")
	}

	wantCost := (8 + 1020*0.1 + 500*1.25) * 5 / 1e6
	wantCost += 879 * 25 / 1e6
	if math.Abs(got.CostUSD-wantCost) > 1e-9 {
		t.Errorf("CostUSD=%.10f, ожидалось %.10f", got.CostUSD, wantCost)
	}
}

// Контрпример из живых данных: промежуточная строка группы несёт непустой
// stop_reason, а финальная с максимальным usage — null. Победить обязана
// строка с максимумом токенов, иначе расход занижается.
func TestAccumulatorRankPrefersMaxTokens(t *testing.T) {
	samples := loadUsage(t, "dup-rank.jsonl")

	checkTotals(t, sum(t, samples), 2, 779, 5000, 0, 0, 1)

	// И в обратном порядке поступления — тот же победитель.
	reversed := []parse.Usage{samples[1], samples[0]}
	checkTotals(t, sum(t, reversed), 2, 779, 5000, 0, 0, 1)
}

// stop_reason — только вторичный признак: он решает лишь при равных суммах.
func TestAccumulatorStopReasonBreaksTieOnEqualTotals(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")
	noStop := parse.Usage{RequestID: "r", MessageID: "m", Model: "claude-haiku-4-5", Time: at, Output: 100}
	withStop := parse.Usage{RequestID: "r", MessageID: "m", Model: "claude-opus-5", Time: at, Output: 100, StopReason: "end_turn"}

	for _, order := range [][]parse.Usage{{noStop, withStop}, {withStop, noStop}} {
		got := sum(t, order)
		if got.Output != 100 || got.Requests != 1 {
			t.Fatalf("Output=%d, Requests=%d, ожидалось 100 и 1", got.Output, got.Requests)
		}
		if len(got.Models) != 1 || got.Models[0].Model != "claude-opus-5" {
			t.Errorf("порядок %v: победил %+v, ожидалась запись со stop_reason", order[0].Model, got.Models)
		}
	}
}

// В живом корпусе все строки группы несут одинаковый непустой stop_reason,
// поэтому правило «берём строку со stop_reason» группу не разрешает —
// тай-брейк по максимуму суммы токенов обязателен.
func TestAccumulatorTieBreakByTokens(t *testing.T) {
	got := sum(t, loadUsage(t, "dup-stopreason.jsonl"))

	// Победитель — средняя строка (500), а не последняя (120) и не сумма (630).
	checkTotals(t, got, 2, 500, 1000, 0, 0, 1)
}

// При живом тейлинге строки приходят по одной и в любом порядке —
// итог обязан быть верен в любой момент.
func TestAccumulatorOrderIndependent(t *testing.T) {
	samples := loadUsage(t, "dup-usage.jsonl")
	want := sum(t, samples)

	rng := rand.New(rand.NewSource(42))
	for i := range 50 {
		shuffled := make([]parse.Usage, len(samples))
		copy(shuffled, samples)
		rng.Shuffle(len(shuffled), func(a, b int) {
			shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
		})

		got := sum(t, shuffled)
		if got.Input != want.Input || got.Output != want.Output ||
			got.CacheRead != want.CacheRead || got.Cache5m != want.Cache5m ||
			got.Cache1h != want.Cache1h || got.Requests != want.Requests {
			t.Fatalf("перестановка %d дала %+v, ожидалось %+v", i, got, want)
		}
	}
}

// Итог верен не только в конце файла: после трёх строк группы
// он равен промежуточному сэмплу, а не их сумме.
func TestAccumulatorPartialGroup(t *testing.T) {
	samples := loadUsage(t, "dup-usage.jsonl")

	acc := NewAccumulator()
	for _, u := range samples[:3] {
		acc.Add(u)
	}
	got := acc.Totals()
	checkTotals(t, got, 3, 10, 1000, 500, 0, 1)

	acc.Add(samples[3])
	checkTotals(t, acc.Totals(), 3, 779, 1000, 500, 0, 1)
}

func TestAccumulatorEmpty(t *testing.T) {
	got := NewAccumulator().Totals()

	checkTotals(t, got, 0, 0, 0, 0, 0, 0)
	if got.CostUSD != 0 {
		t.Errorf("CostUSD=%v, ожидался 0", got.CostUSD)
	}
	if len(got.Models) != 0 {
		t.Errorf("Models=%+v, ожидался пустой список", got.Models)
	}
	if got.Unknown {
		t.Errorf("Unknown=true на пустом аккумуляторе")
	}
}

func TestAccumulatorUnknownModel(t *testing.T) {
	acc := NewAccumulator()
	acc.Add(parse.Usage{
		RequestID: "r", MessageID: "m", Model: "gpt-9",
		Time: ts("2026-08-03T10:00:00Z"), Input: 1_000_000, Output: 1_000_000,
	})
	got := acc.Totals()

	if !got.Unknown {
		t.Errorf("Unknown=false, ожидался флаг неизвестной модели")
	}
	if math.Abs(got.CostUSD-30) > 1e-9 {
		t.Errorf("CostUSD=%v, ожидалось 30 (тариф opus)", got.CostUSD)
	}
}

// Тариф выбирается по времени события, а не по «сейчас».
func TestAccumulatorPricesByEventTime(t *testing.T) {
	mk := func(id string, at time.Time) parse.Usage {
		return parse.Usage{RequestID: id, MessageID: id, Model: "claude-sonnet-5", Time: at, Output: 1_000_000}
	}

	intro := NewAccumulator()
	intro.Add(mk("a", ts("2026-08-15T00:00:00Z")))
	if math.Abs(intro.Totals().CostUSD-10) > 1e-9 {
		t.Errorf("интро-тариф: CostUSD=%v, ожидалось 10", intro.Totals().CostUSD)
	}

	later := NewAccumulator()
	later.Add(mk("b", ts("2026-09-15T00:00:00Z")))
	if math.Abs(later.Totals().CostUSD-15) > 1e-9 {
		t.Errorf("тариф после смены: CostUSD=%v, ожидалось 15", later.Totals().CostUSD)
	}
}

// Ускоренный режим тарифицируется вдвое: Opus 5 становится 10/50.
// В живых данных speed всегда "standard", проверка синтетическая.
func TestAccumulatorFastSpeedDoublesRate(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")

	slow := NewAccumulator()
	slow.Add(parse.Usage{RequestID: "a", MessageID: "a", Model: "claude-opus-5", Time: at, Output: 1_000_000})
	if got := slow.Totals().CostUSD; math.Abs(got-25) > 1e-9 {
		t.Fatalf("обычный режим: CostUSD=%v, ожидалось 25", got)
	}

	fast := NewAccumulator()
	fast.Add(parse.Usage{RequestID: "a", MessageID: "a", Model: "claude-opus-5", Time: at, Output: 1_000_000, Fast: true})
	if got := fast.Totals().CostUSD; math.Abs(got-50) > 1e-9 {
		t.Errorf("ускоренный режим: CostUSD=%v, ожидалось 50", got)
	}

	fastIn := NewAccumulator()
	fastIn.Add(parse.Usage{RequestID: "b", MessageID: "b", Model: "claude-opus-5", Time: at, Input: 1_000_000, Fast: true})
	if got := fastIn.Totals().CostUSD; math.Abs(got-10) > 1e-9 {
		t.Errorf("вход в ускоренном режиме: CostUSD=%v, ожидалось 10", got)
	}
}

// Веб-поиск оплачивается отдельно: $0.01 за запрос.
func TestAccumulatorWebSearchCost(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")

	acc := NewAccumulator()
	acc.Add(parse.Usage{RequestID: "a", MessageID: "a", Model: "claude-opus-5", Time: at, WebSearch: 7})

	got := acc.Totals()
	if got.WebSearch != 7 {
		t.Errorf("WebSearch=%d, ожидалось 7", got.WebSearch)
	}
	if math.Abs(got.CostUSD-0.07) > 1e-9 {
		t.Errorf("CostUSD=%v, ожидалось 0.07", got.CostUSD)
	}

	if math.Abs(got.WebSearchUSD-0.07) > 1e-9 {
		t.Errorf("WebSearchUSD=%v, ожидалось 0.07", got.WebSearchUSD)
	}
}

// Счётчик веб-поиска берётся максимумом по группе, а не у победителя по
// токенам: иначе часть оплаченных запросов пропадает, если максимум токенов
// пришёлся не на строку с максимальным счётчиком поиска.
func TestAccumulatorWebSearchIsGroupMax(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")
	big := parse.Usage{RequestID: "a", MessageID: "a", Model: "claude-opus-5", Time: at, WebSearch: 7}
	winner := parse.Usage{RequestID: "a", MessageID: "a", Model: "claude-opus-5", Time: at, Output: 100, WebSearch: 2}

	for _, order := range [][]parse.Usage{{big, winner}, {winner, big}} {
		got := sum(t, order)
		if got.WebSearch != 7 {
			t.Errorf("порядок %v: WebSearch=%d, ожидалось 7", []int64{order[0].WebSearch, order[1].WebSearch}, got.WebSearch)
		}
		if got.Output != 100 {
			t.Errorf("Output=%d, ожидалось 100: победитель по токенам не изменился", got.Output)
		}
		wantCost := 100*25/1e6 + 7*0.01
		if math.Abs(got.CostUSD-wantCost) > 1e-9 {
			t.Errorf("CostUSD=%v, ожидалось %v", got.CostUSD, wantCost)
		}
	}
}

// Кэш в ускоренном режиме: множитель кэша применяется к уже удвоенной цене
// входа. Поведение фиксируется тестом, чтобы перестать быть неявным.
func TestAccumulatorFastCacheRate(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")

	acc := NewAccumulator()
	acc.Add(parse.Usage{RequestID: "a", MessageID: "a", Model: "claude-opus-5", Time: at, Cache1h: 1_000_000, Fast: true})
	// 1M токенов записи 1h × 2.0 (множитель) × $10/M (удвоенный вход opus) = $20
	if got := acc.Totals().CostUSD; math.Abs(got-20) > 1e-9 {
		t.Errorf("запись 1h в ускоренном режиме: CostUSD=%v, ожидалось 20", got)
	}

	slow := NewAccumulator()
	slow.Add(parse.Usage{RequestID: "b", MessageID: "b", Model: "claude-opus-5", Time: at, Cache1h: 1_000_000})
	if got := slow.Totals().CostUSD; math.Abs(got-10) > 1e-9 {
		t.Errorf("запись 1h в обычном режиме: CostUSD=%v, ожидалось 10", got)
	}

	read := NewAccumulator()
	read.Add(parse.Usage{RequestID: "c", MessageID: "c", Model: "claude-opus-5", Time: at, CacheRead: 1_000_000, Fast: true})
	// 1M чтения × 0.1 × $10/M = $1
	if got := read.Totals().CostUSD; math.Abs(got-1) > 1e-9 {
		t.Errorf("чтение кэша в ускоренном режиме: CostUSD=%v, ожидалось 1", got)
	}
}

// Веб-поиск поштучный: ускоренный режим его не удваивает.
func TestAccumulatorFastDoesNotDoubleWebSearch(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")

	acc := NewAccumulator()
	acc.Add(parse.Usage{RequestID: "a", MessageID: "a", Model: "claude-opus-5", Time: at, WebSearch: 5, Fast: true})
	if got := acc.Totals().CostUSD; math.Abs(got-0.05) > 1e-9 {
		t.Errorf("CostUSD=%v, ожидалось 0.05", got)
	}
}

func TestAccumulatorModelsSortedByCount(t *testing.T) {
	acc := NewAccumulator()
	at := ts("2026-08-03T10:00:00Z")
	add := func(id, model string) {
		acc.Add(parse.Usage{RequestID: id, MessageID: id, Model: model, Time: at, Output: 1})
	}
	add("1", "claude-haiku-4-5")
	add("2", "claude-opus-5")
	add("3", "claude-opus-5")
	add("4", "claude-opus-5")
	add("5", "claude-haiku-4-5")
	add("6", "claude-fable-5")

	got := acc.Totals().Models
	want := []ModelCount{
		{Model: "claude-opus-5", Count: 3},
		{Model: "claude-haiku-4-5", Count: 2},
		{Model: "claude-fable-5", Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("Models=%+v, ожидалось %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Models[%d]=%+v, ожидалось %+v", i, got[i], want[i])
		}
	}
}

// Замена победителя группы обязана переносить и счётчик модели.
func TestAccumulatorReplacementMovesModelCount(t *testing.T) {
	acc := NewAccumulator()
	at := ts("2026-08-03T10:00:00Z")
	acc.Add(parse.Usage{RequestID: "r", MessageID: "m", Model: "claude-haiku-4-5", Time: at, Output: 10})
	acc.Add(parse.Usage{RequestID: "r", MessageID: "m", Model: "claude-opus-5", Time: at, Output: 20, StopReason: "end_turn"})

	got := acc.Totals()
	if got.Requests != 1 {
		t.Fatalf("Requests=%d, ожидался 1", got.Requests)
	}
	if len(got.Models) != 1 || got.Models[0].Model != "claude-opus-5" || got.Models[0].Count != 1 {
		t.Errorf("Models=%+v, ожидалось [claude-opus-5 ×1]", got.Models)
	}
}

// Доля кэша считается по всему кэшу — чтению и обеим записям.
func TestCacheShare(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")

	acc := NewAccumulator()
	// 1M чтения ×0.1×$5 = $0.5; 1M выхода ×$25 = $25; итого $25.5.
	acc.Add(parse.Usage{RequestID: "a", MessageID: "a", Model: "claude-opus-5", Time: at,
		CacheRead: 1_000_000, Output: 1_000_000})

	got := acc.Totals()
	if math.Abs(got.CacheUSD-0.5) > 1e-9 {
		t.Errorf("CacheUSD=%v, ожидалось 0.5", got.CacheUSD)
	}
	if got.CacheShare() != 2 {
		t.Errorf("CacheShare()=%d%%, ожидалось 2%%", got.CacheShare())
	}

	// Кэш-тяжёлая сессия: запись 1h ×2.0 и чтение дают почти всю сумму.
	heavy := NewAccumulator()
	heavy.Add(parse.Usage{RequestID: "b", MessageID: "b", Model: "claude-opus-5", Time: at,
		CacheRead: 100_000_000, Cache1h: 1_000_000, Output: 10_000})
	if share := heavy.Totals().CacheShare(); share < 90 {
		t.Errorf("CacheShare()=%d%%, ожидалось больше 90%%", share)
	}

	// Пустая сессия: делить не на что, доли нет.
	if share := NewAccumulator().Totals().CacheShare(); share != 0 {
		t.Errorf("CacheShare()=%d%% на пустом аккумуляторе", share)
	}
}
