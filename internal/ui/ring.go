package ui

import (
	"strings"

	"github.com/KratosUAE/ccon/internal/parse"
)

// DefaultCapacity — сколько событий держит кольцевой буфер. Длинная сессия не
// должна съедать память. Стоимость и токены (Totals) копятся отдельно от
// буфера и вытеснения не замечают; сводка таба (paneStats) считается проходом
// по буферу и после вытеснения головы честно уменьшается.
const DefaultCapacity = 10_000

// ring — кольцевой буфер событий, один на все табы.
//
// Строк здесь нет: у трёх видов над одним потоком строки разные, и держать их
// рядом с событием значило бы хранить сессию трижды. Строки живут в панелях,
// а панель пересобирается проходом по этому буферу.
type ring struct {
	capacity int
	events   []parse.Event

	// base — сквозной номер события, лежащего в голове буфера; byTool —
	// ключ сшивки в этот сквозной номер. Адресация по индексу не годится:
	// пока результат вызова летит из тейлера, голова могла вытесниться, и
	// индекс стал бы указывать на чужую строку.
	base   int64
	byTool map[string]int64
}

func newRing(capacity int) *ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &ring{capacity: capacity, byTool: make(map[string]int64)}
}

// push добавляет событие. Возвращает сквозной номер добавленного и признак
// вытеснения головы: по номеру панель адресует свою строку, по признаку
// вызывающий сбрасывает её кэши.
//
// Вытеснение амортизированное — четвертью буфера разом, а не по одному
// событию. Сдвиг на каждое событие после десятитысячного стоил бы memmove
// всего буфера, то есть заявленная O(1) вставка была бы O(ёмкости).
func (r *ring) push(ev parse.Event) (int64, bool) {
	seq := r.base + int64(len(r.events))
	if ev.ToolID != "" {
		r.byTool[ev.ToolID] = seq
	}
	r.events = append(r.events, ev)

	if len(r.events) <= r.capacity+r.capacity/4 {
		return seq, false
	}

	drop := len(r.events) - r.capacity
	newBase := r.base + int64(drop)
	for _, old := range r.events[:drop] {
		// Переигранная история даёт второе событие с тем же ключом: снимаем
		// запись, только если она всё ещё указывает на вытесняемое. Иначе
		// вытеснение старого дубля обесточило бы живой, ещё показанный вызов.
		if old.ToolID == "" {
			continue
		}
		if seq, known := r.byTool[old.ToolID]; known && seq < newBase {
			delete(r.byTool, old.ToolID)
		}
	}

	r.events = append(r.events[:0], r.events[drop:]...)
	r.base = newBase
	return seq, true
}

// applyUpdate дописывает исход вызова в уже лежащее в буфере событие и
// возвращает его обновлённым вместе со сквозным номером: по событию
// вызывающий решает, каким панелям это важно, а по номеру панель находит свою
// строку — не бегая по буферу второй раз.
//
// ok == false — событие вытеснено или ключ неизвестен. Это норма живого
// режима (результат мог приехать к строке, ушедшей за край буфера), а не сбой:
// молчаливый отказ честнее фантомной строки.
func (r *ring) applyUpdate(u parse.Update) (parse.Event, int64, bool) {
	seq, known := r.byTool[u.ToolID]
	if !known {
		return parse.Event{}, 0, false
	}
	i := seq - r.base
	if i < 0 || i >= int64(len(r.events)) {
		return parse.Event{}, 0, false
	}

	ev := &r.events[i]
	ev.Status, ev.Dur, ev.Fail = u.Status, u.Dur, u.Fail
	return *ev, seq, true
}

func (r *ring) len() int { return len(r.events) }

// at отдаёт событие по сквозному номеру. false означает «вытеснено»: номер
// живёт в кэшах панелей дольше самого события.
func (r *ring) at(seq int64) (parse.Event, bool) {
	i := seq - r.base
	if i < 0 || i >= int64(len(r.events)) {
		return parse.Event{}, false
	}
	return r.events[i], true
}

// each прогоняет события в порядке поступления вместе с их сквозными
// номерами. Потоковый проход, а не копия среза: панели строят по нему строки и
// самих событий не держат.
func (r *ring) each(fn func(seq int64, ev parse.Event)) {
	for i, ev := range r.events {
		fn(r.base+int64(i), ev)
	}
}

// rename меняет подпись источника у уже показанных событий. Возвращает,
// нашлось ли что менять — иначе пересобирать панели незачем.
func (r *ring) rename(from, to string) bool {
	changed := false
	for i := range r.events {
		if r.events[i].Source == from {
			r.events[i].Source = to
			changed = true
		}
	}
	return changed
}

// renameAll применяет карту подписей одним проходом. Возвращает, менялось ли
// что-нибудь: при старте сессии с полусотней субагентов имена приезжают
// пачкой, и перестраивать панели на каждое имя — та же болезнь, что и вставка
// событий по одному.
func (r *ring) renameAll(names map[string]string) bool {
	if len(names) == 0 {
		return false
	}

	changed := false
	for i := range r.events {
		if to, ok := names[r.events[i].Source]; ok && to != r.events[i].Source {
			r.events[i].Source = to
			changed = true
		}
	}
	return changed
}

// matches — событие проходит фильтр. Поиск широкий: parse.Haystack видит
// источник, инструмент, путь, деталь И текст ошибки (ev.Fail дописывается
// задним числом, слайс 2) — иначе фильтр разъедется с показом. Фильтр влияет
// только на видимое: сводка таба считается по всему буферу (не по потоку —
// после вытеснения головы она уменьшается, см. DefaultCapacity выше).
func matches(ev parse.Event, filter string) bool {
	if filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(parse.Haystack(ev)), strings.ToLower(filter))
}
