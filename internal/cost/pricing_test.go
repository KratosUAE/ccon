package cost

import (
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPrice(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		at      time.Time
		in, out float64
		unknown bool
	}{
		{name: "opus-5", model: "claude-opus-5", at: ts("2026-08-03T10:00:00Z"), in: 5, out: 25},
		{name: "opus-4-8", model: "claude-opus-4-8", at: ts("2026-08-03T10:00:00Z"), in: 5, out: 25},
		{name: "opus-4-7", model: "claude-opus-4-7", at: ts("2026-08-03T10:00:00Z"), in: 5, out: 25},
		{name: "opus-4-6", model: "claude-opus-4-6", at: ts("2026-08-03T10:00:00Z"), in: 5, out: 25},
		{name: "sonnet-4-6", model: "claude-sonnet-4-6", at: ts("2026-08-03T10:00:00Z"), in: 3, out: 15},
		{name: "haiku-4-5", model: "claude-haiku-4-5", at: ts("2026-08-03T10:00:00Z"), in: 1, out: 5},
		{name: "fable-5", model: "claude-fable-5", at: ts("2026-08-03T10:00:00Z"), in: 10, out: 50},

		// Датовый суффикс обязан матчиться префиксом, иначе модель падает
		// в ветку «неизвестная → тариф opus» и цена завышается впятеро.
		{name: "haiku с датовым суффиксом", model: "claude-haiku-4-5-20251001", at: ts("2026-08-03T10:00:00Z"), in: 1, out: 5},
		{name: "opus с датовым суффиксом", model: "claude-opus-5-20260101", at: ts("2026-08-03T10:00:00Z"), in: 5, out: 25},

		// Интро-тариф sonnet-5 действует до конца 2026-08-31.
		{name: "sonnet-5 в интро-период", model: "claude-sonnet-5", at: ts("2026-08-15T00:00:00Z"), in: 2, out: 10},
		{name: "sonnet-5 на последней секунде интро", model: "claude-sonnet-5", at: ts("2026-08-31T23:59:59Z"), in: 2, out: 10},
		{name: "sonnet-5 после смены тарифа", model: "claude-sonnet-5", at: ts("2026-09-01T00:00:00Z"), in: 3, out: 15},
		{name: "sonnet-5 в сентябре", model: "claude-sonnet-5", at: ts("2026-09-15T12:00:00Z"), in: 3, out: 15},
		{name: "sonnet-5 с датовым суффиксом в интро", model: "claude-sonnet-5-20260601", at: ts("2026-08-15T00:00:00Z"), in: 2, out: 10},

		{name: "неизвестная модель считается по opus", model: "gpt-9-turbo", at: ts("2026-08-03T10:00:00Z"), in: 5, out: 25, unknown: true},
		{name: "пустое имя модели", model: "", at: ts("2026-08-03T10:00:00Z"), in: 5, out: 25, unknown: true},
		{name: "нулевое время не роняет выбор", model: "claude-opus-5", at: time.Time{}, in: 5, out: 25},
		// Битый timestamp не должен молча дарить интро-тариф: берём позднее окно.
		{name: "нулевое время даёт поздний тариф sonnet-5", model: "claude-sonnet-5", at: time.Time{}, in: 3, out: 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Price(tt.model, tt.at)
			if got.In != tt.in || got.Out != tt.out {
				t.Errorf("Price(%q)= %v/%v, ожидалось %v/%v", tt.model, got.In, got.Out, tt.in, tt.out)
			}
			if got.Unknown != tt.unknown {
				t.Errorf("Unknown=%v, ожидалось %v", got.Unknown, tt.unknown)
			}
		})
	}
}

// Матч должен выбирать самый длинный подходящий префикс, а не первый попавшийся.
func TestPriceLongestPrefixWins(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")

	if got := Price("claude-sonnet-4-6", at); got.In != 3 || got.Out != 15 {
		t.Errorf("claude-sonnet-4-6 → %v/%v, ожидалось 3/15", got.In, got.Out)
	}
	if got := Price("claude-opus-4-8-20260101", at); got.Unknown {
		t.Errorf("claude-opus-4-8-20260101 не должна считаться неизвестной")
	}
	// Похожее, но чужое имя не должно съедаться префиксом таблицы.
	if got := Price("claude-opus", at); !got.Unknown {
		t.Errorf("claude-opus без версии — неизвестная модель, а не opus-5")
	}
}

// Префикс обязан совпадать по границе компонента имени, а не по буквам:
// иначе будущая claude-opus-50 молча считалась бы по тарифу claude-opus-5.
func TestPriceMatchesOnComponentBoundary(t *testing.T) {
	at := ts("2026-08-03T10:00:00Z")

	cases := []struct {
		model   string
		unknown bool
		in, out float64
	}{
		{model: "claude-opus-5", in: 5, out: 25},
		{model: "claude-opus-5-20260101", in: 5, out: 25},
		// Суффикс окна контекста: имя семейства то же, тариф тот же.
		{model: "claude-sonnet-5[1m]", in: 2, out: 10},
		// Цифра, приросшая к версии, — другая модель, а не та же самая.
		{model: "claude-opus-50", unknown: true},
		{model: "claude-haiku-4-55", unknown: true},
		{model: "claude-sonnet-5x", unknown: true},
	}

	for _, c := range cases {
		got := Price(c.model, at)
		if got.Unknown != c.unknown {
			t.Errorf("%s: Unknown=%v, ожидалось %v", c.model, got.Unknown, c.unknown)
			continue
		}
		if c.unknown {
			continue
		}
		if got.In != c.in || got.Out != c.out {
			t.Errorf("%s → %v/%v, ожидалось %v/%v", c.model, got.In, got.Out, c.in, c.out)
		}
	}
}
