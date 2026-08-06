package cost

import (
	"math"
	"sort"

	"github.com/KratosUAE/ccon/internal/parse"
)

// ModelCount — сколько запросов пришлось на модель.
type ModelCount struct {
	Model string
	Count int
}

// Totals — агрегат расхода по всем учтённым сэмплам.
type Totals struct {
	Input     int64
	Output    int64
	CacheRead int64
	Cache5m   int64
	Cache1h   int64
	WebSearch int64 // запросов веб-поиска, тарифицируются отдельно
	Requests  int   // число уникальных запросов после дедупликации
	// WebSearchUSD — доля веб-поиска в CostUSD, а не добавка к ней.
	WebSearchUSD float64
	// CacheUSD — доля кэша в CostUSD: чтение плюс обе записи. Больше половины
	// суммы обычно даёт именно кэш, и без этой доли цифра выглядит загадочной
	// рядом с оценками, где чтение кэша считают бесплатным.
	CacheUSD float64
	Models   []ModelCount // по убыванию числа запросов, затем по имени
	CostUSD  float64
	Unknown  bool // среди моделей была неизвестная тарифу
}

// CacheShare — какую долю цены дал кэш, в целых процентах.
// При нулевой цене доли нет: делить не на что и показывать нечего.
func (t Totals) CacheShare() int {
	if t.CostUSD <= 0 {
		return 0
	}
	return int(math.Round(t.CacheUSD / t.CostUSD * 100))
}

// CacheCreate — суммарная запись кэша, 5m плюс 1h.
func (t Totals) CacheCreate() int64 { return t.Cache5m + t.Cache1h }

// counted — победитель группы дедупликации и всё, что он внёс в суммы.
type counted struct {
	cacheCost float64
	total     int64 // сумма токенов сэмпла — основной критерий
	hasStop   bool  // непустой stop_reason — тай-брейк при равных суммах
	usage     parse.Usage
	cost      float64
	unknown   bool
}

// better — новый сэмпл вытесняет прежнего победителя группы.
//
// Основной критерий — максимум суммы токенов. stop_reason только вторичен:
// в живых данных встречаются группы, где промежуточная строка несёт
// stop_reason, а финальная с максимальным usage — null; главенство
// stop_reason занизило бы расход.
func (c counted) better(than counted) bool {
	if c.total != than.total {
		return c.total > than.total
	}
	return c.hasStop && !than.hasStop
}

// group — состояние одной группы дедупликации: победитель по токенам и
// максимум счётчика веб-поиска по всем строкам группы.
type group struct {
	winner    counted
	webSearch int64
}

// Accumulator суммирует расход, дедуплицируя сэмплы по ключу (requestId, message.id).
//
// Одна логическая реплика пишется несколькими JSONL-строками с одинаковым
// промежуточным usage; наивное суммирование даёт кратный переучёт. В группе
// побеждает сэмпл с максимальной суммой токенов, а замена делается дельтой
// (вычесть старое, прибавить новое), чтобы итог был верен в любой момент, а не
// только в конце файла: при живом тейлинге строки группы приходят по одной.
type Accumulator struct {
	seen    map[string]group
	models  map[string]int
	unknown int
	totals  Totals
}

// NewAccumulator создаёт пустой аккумулятор.
func NewAccumulator() *Accumulator {
	return &Accumulator{
		seen:   make(map[string]group),
		models: make(map[string]int),
	}
}

// Add учитывает сэмпл расхода.
func (a *Accumulator) Add(u parse.Usage) {
	rate := Price(u.Model, u.Time)
	fresh := counted{
		total:     u.Total(),
		hasStop:   u.StopReason != "",
		usage:     u,
		cost:      rate.Cost(u),
		cacheCost: rate.CacheCost(u),
		unknown:   rate.Unknown,
	}

	key := u.Key()
	g, exists := a.seen[key]
	if !exists {
		a.totals.Requests++
	}

	// Веб-поиск держится максимумом по группе и не зависит от победителя по
	// токенам: счётчик в строках группы растёт, а максимум токенов может
	// прийтись не на последнюю строку — иначе часть запросов не оплачена.
	if u.WebSearch > g.webSearch {
		delta := u.WebSearch - g.webSearch
		a.totals.WebSearch += delta
		a.totals.WebSearchUSD += float64(delta) * WebSearchPrice
		a.totals.CostUSD += float64(delta) * WebSearchPrice
		g.webSearch = u.WebSearch
	}

	if !exists || fresh.better(g.winner) {
		if exists {
			a.apply(g.winner, -1)
		}
		a.apply(fresh, 1)
		g.winner = fresh
	}
	a.seen[key] = g
}

// Totals отдаёт текущий агрегат.
func (a *Accumulator) Totals() Totals {
	t := a.totals
	t.Unknown = a.unknown > 0
	t.Models = a.modelCounts()
	return t
}

// apply прибавляет (sign=1) или вычитает (sign=-1) вклад победителя группы.
func (a *Accumulator) apply(c counted, sign int64) {
	u := c.usage
	a.totals.Input += sign * u.Input
	a.totals.Output += sign * u.Output
	a.totals.CacheRead += sign * u.CacheRead
	a.totals.Cache5m += sign * u.Cache5m
	a.totals.Cache1h += sign * u.Cache1h
	a.totals.CostUSD += float64(sign) * c.cost
	a.totals.CacheUSD += float64(sign) * c.cacheCost

	if u.Model != "" {
		a.models[u.Model] += int(sign)
		if a.models[u.Model] == 0 {
			delete(a.models, u.Model)
		}
	}
	if c.unknown {
		a.unknown += int(sign)
	}
}

func (a *Accumulator) modelCounts() []ModelCount {
	if len(a.models) == 0 {
		return nil
	}
	out := make([]ModelCount, 0, len(a.models))
	for m, n := range a.models {
		out = append(out, ModelCount{Model: m, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Model < out[j].Model
	})
	return out
}
