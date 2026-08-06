package parse

import (
	"strings"
	"time"
)

// maxPending — потолок карты незакрытых вызовов. Одновременно открытых вызовов
// в живом корпусе не бывает больше пяти, так что это страховка от патологии
// (обрезанный файл, ручная правка транскрипта), а не ответ на нагрузку.
//
// Потолка по ВРЕМЕНИ здесь нет намеренно: самый долгий честный вызов корпуса
// (AskUserQuestion) ждал ответа 15 ч 26 м, и любой TTL убил бы правильную
// сшивку ради экономии сотни байт.
const maxPending = 4096

// toolErrorTag — обёртка текста ошибки инструмента: <tool_use_error>…</…>.
// Ею обёрнуты 48 ошибок из 252, остальные приходят голым текстом.
const toolErrorTag = "tool_use_error"

// Result — блок tool_result записи транскрипта: чем кончился вызов.
//
// Поля источника («кем») здесь нет и быть не может: на записях с tool_result
// attributionAgent отсутствует всегда, и подстановка дала бы неверное имя.
// Источник знает только событие вызова.
type Result struct {
	ToolUseID string
	Time      time.Time
	IsError   bool
	// Denial — toolDenialKind записи: отказ правила или пользователя.
	// Пусто у обычного результата.
	Denial string
	// Text — содержимое результата, УЖЕ обрезанное при декоде: сырой content
	// доходит до 147 КБ, и держать его на строку окна нельзя.
	Text string
}

// Update — чем дополнить уже показанную строку вызова, когда пришёл его
// результат. Ключ адресации — ToolID: пока результат летел, голова буфера
// могла вытесниться, и индекс строки стал бы чужим.
type Update struct {
	ToolID string
	Status Status
	Dur    time.Duration
	Fail   string
}

// Linker сшивает вызовы инструментов с их результатами по id.
//
// Одна карта на сессию — это корректно, потому что toolu-идентификаторы
// глобально уникальны (на живом корпусе транскриптов ноль коллизий), а не
// потому что «так вышло». Состояния UI линкер не знает: он чист и таблично
// проверяем.
type Linker struct {
	// pending — время записи вызова по его id. Больше от вызова ничего не
	// нужно: само событие найдёт по ToolID буфер интерфейса.
	pending map[string]time.Time
	// order — порядок добавления для FIFO-вытеснения при переполнении.
	order []string
}

// NewLinker собирает пустой линкер.
func NewLinker() *Linker {
	return &Linker{pending: make(map[string]time.Time)}
}

// Track запоминает вызов. Событие без ключа сшивки (реплика, системная
// запись) — тихий no-op: отбор делает панель, а не линкер.
//
// Повторный Track того же id перезаписывает время: переигранная история даёт
// в корпусе такие дубли, и последняя запись вызова честнее первой.
func (l *Linker) Track(ev Event) {
	if ev.ToolID == "" {
		return
	}
	if _, seen := l.pending[ev.ToolID]; !seen {
		l.order = append(l.order, ev.ToolID)
	}
	l.pending[ev.ToolID] = ev.Time
	l.evict()
}

// Resolve сшивает результат с его вызовом.
//
// ok == false означает «сшивать не с чем»: результат-сирота либо дубль уже
// закрытого вызова (в корпусе есть файл с одной и той же записью результата
// дважды). Это штатный исход, а не сбой: паниковать и заводить фантомную
// запись вызова нельзя — её нечем показать.
//
// Идемпотентность обеспечена удалением закрытого вызова из карты: второй
// такой же результат уже не найдёт его.
func (l *Linker) Resolve(r Result) (Update, bool) {
	use, known := l.pending[r.ToolUseID]
	if !known {
		return Update{}, false
	}
	delete(l.pending, r.ToolUseID)

	return Update{
		ToolID: r.ToolUseID,
		Status: r.status(),
		Dur:    span(use, r.Time),
		Fail:   r.fail(),
	}, true
}

// open — сколько вызовов ждут результата. Нужен тестам потолка.
func (l *Linker) open() int { return len(l.pending) }

// evict держит карту в пределах потолка, выбрасывая самые ранние вызовы.
func (l *Linker) evict() {
	for len(l.pending) > maxPending && len(l.order) > 0 {
		oldest := l.order[0]
		l.order = l.order[1:]
		delete(l.pending, oldest)
	}
	// Закрытые вызовы оставляют в очереди свои ключи: без чистки она росла бы
	// вместе с длиной сессии, хотя карта остаётся крошечной.
	if len(l.order) > 2*maxPending {
		l.compact()
	}
}

// compact пересобирает очередь из ещё открытых вызовов, сохраняя их порядок.
func (l *Linker) compact() {
	kept := make([]string, 0, len(l.pending))
	for _, id := range l.order {
		if _, open := l.pending[id]; open {
			kept = append(kept, id)
		}
	}
	l.order = kept
}

// status — исход вызова по флагам результата.
//
// Отказ — ОТДЕЛЬНЫЙ исход, а не сорт ошибки: 85 ошибок корпуса из 252 это
// отказы правила разрешений или пользователя, и слитый счётчик врал бы втрое.
func (r Result) status() Status {
	switch {
	case !r.IsError:
		return StatusOK
	case r.Denial != "":
		return StatusDenied
	default:
		return StatusError
	}
}

// fail — текст ошибки для показа: обёртка снимается, длина обрезается.
// У успешного результата текста ошибки нет, каким бы ни было содержимое:
// вывод упавшей команды Bash — это исход команды, а не исход вызова.
func (r Result) fail() string {
	if !r.IsError {
		return ""
	}
	text := strings.TrimSpace(r.Text)
	if inner, wrapped := strings.CutPrefix(text, "<"+toolErrorTag+">"); wrapped {
		// Закрывающего тега может не быть: длинную ошибку обрезал декодер.
		text = strings.TrimSuffix(inner, "</"+toolErrorTag+">")
	}
	return Truncate(text, maxError)
}

// span — длительность вызова: разница отметок ЗАПИСЕЙ результата и вызова,
// готового поля длительности в транскрипте нет.
//
// Ноль означает «не известна» и в колонке не рисуется. Отрицательной разницы
// в корпусе нет, но архивный путь чинит битые метки времени только у событий,
// не у результатов — нулём тут честнее, чем минусом.
func span(use, done time.Time) time.Duration {
	if use.IsZero() || done.IsZero() {
		return 0
	}
	if d := done.Sub(use); d > 0 {
		return d
	}
	return 0
}
