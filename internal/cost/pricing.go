// Package cost считает расход по транскрипту: таблица тарифов с датовыми
// интервалами и аккумулятор с дедупликацией сэмплов usage.
package cost

import (
	"strings"
	"time"

	"github.com/KratosUAE/ccon/internal/parse"
)

// Множители к цене входа, подтверждены разведкой.
const (
	multCacheRead   = 0.1
	multCacheWrite5 = 1.25
	multCacheWrite1 = 2.0

	// fastMultiplier — ускоренный режим: Opus 5 тарифицируется как 10/50.
	fastMultiplier = 2.0
)

// WebSearchPrice — цена одного запроса веб-поиска в долларах. Поштучная,
// ускоренным режимом не удваивается.
const WebSearchPrice = 0.01

// Тариф по умолчанию для неизвестной модели — самый дорогой из обычных, opus.
var fallbackRate = Rate{In: 5, Out: 25, Unknown: true}

// Rate — тариф модели в долларах за миллион токенов.
type Rate struct {
	In      float64
	Out     float64
	Unknown bool // модели нет в таблице, взят тариф opus
}

// window — тариф модели на интервале [from, to). Нулевая граница — открытая.
type window struct {
	prefix   string
	from, to time.Time
	in, out  float64
}

// sonnet5NewPrice — момент, с которого интро-тариф sonnet-5 сменяется обычным.
var sonnet5NewPrice = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// farFuture — подстановка вместо неизвестного времени события.
var farFuture = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// table — тарифы. Матч по префиксу: имя модели приходит и с датовым
// суффиксом (claude-haiku-4-5-20251001).
var table = []window{
	{prefix: "claude-opus-5", in: 5, out: 25},
	{prefix: "claude-opus-4-8", in: 5, out: 25},
	{prefix: "claude-opus-4-7", in: 5, out: 25},
	{prefix: "claude-opus-4-6", in: 5, out: 25},
	{prefix: "claude-sonnet-5", to: sonnet5NewPrice, in: 2, out: 10}, // интро-тариф
	{prefix: "claude-sonnet-5", from: sonnet5NewPrice, in: 3, out: 15},
	{prefix: "claude-sonnet-4-6", in: 3, out: 15},
	{prefix: "claude-haiku-4-5", in: 1, out: 5},
	{prefix: "claude-fable-5", in: 10, out: 50},
}

// matchModel — совпал ли префикс тарифа по границе компонента имени.
//
// Голый strings.HasPrefix здесь неверен: claude-opus-50 молча уехал бы в тариф
// claude-opus-5 и считался бы по чужой цене без единого признака ошибки.
// Границей считается конец строки, дефис (датовый суффикс
// claude-haiku-4-5-20251001) и скобка: суффикс окна контекста вида
// claude-opus-5[1m] в корпусе встречается, и ронять его в тариф «неизвестной
// модели» — тоже молчаливая ошибка, только в другую сторону.
func matchModel(model, prefix string) bool {
	rest, ok := strings.CutPrefix(model, prefix)
	if !ok {
		return false
	}
	return rest == "" || rest[0] == '-' || rest[0] == '['
}

// Price выбирает тариф по имени модели и времени события.
// Матч по самому длинному подходящему префиксу: claude-haiku-4-5-20251001
// обязан попасть в claude-haiku-4-5, а не в ветку «неизвестная модель».
// Интервал выбирается по времени события, а не по «сейчас».
func Price(model string, at time.Time) Rate {
	// Битый или отсутствующий timestamp не должен молча дарить интро-тариф:
	// без даты берём позднее окно, то есть более дорогое и более актуальное.
	if at.IsZero() {
		at = farFuture
	}

	best := -1
	for i, w := range table {
		if !matchModel(model, w.prefix) {
			continue
		}
		if !w.from.IsZero() && at.Before(w.from) {
			continue
		}
		if !w.to.IsZero() && !at.Before(w.to) {
			continue
		}
		if best < 0 || len(w.prefix) > len(table[best].prefix) {
			best = i
		}
	}
	if best < 0 {
		return fallbackRate
	}
	return Rate{In: table[best].in, Out: table[best].out}
}

// Cost — стоимость токенов сэмпла по заданному тарифу. Множители применяются
// к цене входа: чтение кэша 0.1x, запись 5m 1.25x, запись 1h 2.0x.
// Ускоренный режим (usage.speed == "fast") удваивает обе цены, множители кэша
// ложатся поверх удвоенной цены входа.
//
// Веб-поиск сюда не входит: он считается по группе дедупликации, а не по
// победившему сэмплу (см. Accumulator.Add).
func (r Rate) Cost(u parse.Usage) float64 {
	in, out := r.In, r.Out
	if u.Fast {
		in *= fastMultiplier
		out *= fastMultiplier
	}

	billedIn := float64(u.Input) +
		float64(u.CacheRead)*multCacheRead +
		float64(u.Cache5m)*multCacheWrite5 +
		float64(u.Cache1h)*multCacheWrite1

	return billedIn*in/1e6 + float64(u.Output)*out/1e6
}

// CacheCost — та часть стоимости сэмпла, которую дал кэш: чтение и обе записи.
// Нужна, чтобы сумма в футере не выглядела загадочной рядом с оценками, где
// чтение кэша считают бесплатным.
func (r Rate) CacheCost(u parse.Usage) float64 {
	in := r.In
	if u.Fast {
		in *= fastMultiplier
	}

	billed := float64(u.CacheRead)*multCacheRead +
		float64(u.Cache5m)*multCacheWrite5 +
		float64(u.Cache1h)*multCacheWrite1
	return billed * in / 1e6
}
