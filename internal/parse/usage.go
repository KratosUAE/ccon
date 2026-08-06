package parse

import "time"

// MaxTokens — потолок правдоподобного значения счётчика токенов. Всё, что
// выше, и всё отрицательное — мусор или враждебный вход: такие значения
// обнуляются, иначе сумма переполняет int64 и ломает и ранг, и сводку.
const MaxTokens = int64(1) << 40

// Usage — сэмпл расхода одной JSONL-записи.
//
// Одна логическая реплика модели пишется несколькими строками с одинаковыми
// RequestID и MessageID и одинаковым промежуточным usage, поэтому сэмплы
// обязаны дедуплицироваться по ключу Key перед суммированием.
type Usage struct {
	RequestID  string
	MessageID  string
	Model      string
	StopReason string // message.stop_reason, top-level всегда null
	Time       time.Time
	Input      int64
	Output     int64
	CacheRead  int64
	Cache5m    int64
	Cache1h    int64

	// Fast — usage.speed == "fast": ускоренный режим тарифицируется вдвое.
	Fast bool
	// WebSearch — server_tool_use.web_search_requests, оплачивается отдельно.
	WebSearch int64
}

// Key — ключ дедупликации: пара (requestId, message.id).
func (u Usage) Key() string { return u.RequestID + "\x00" + u.MessageID }

// clampTokens отбрасывает отрицательные и абсурдные значения счётчиков.
func clampTokens(v int64) int64 {
	if v < 0 || v > MaxTokens {
		return 0
	}
	return v
}

// Total — сумма всех токенов сэмпла, основной критерий дедупликации.
func (u Usage) Total() int64 {
	return u.Input + u.Output + u.CacheRead + u.Cache5m + u.Cache1h
}
