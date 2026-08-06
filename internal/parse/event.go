// Package parse превращает строки JSONL-транскрипта Claude Code в доменные
// события для лога и в сэмплы расхода токенов для аккумулятора стоимости.
package parse

import "time"

// Kind — род события в логе.
type Kind int

// Роды событий.
const (
	KindUnknown  Kind = iota
	KindText          // реплика ассистента
	KindTool          // вызов инструмента
	KindDelegate      // делегирование субагенту (Agent/Task)
	KindSkill         // вызов скилла
	KindError         // ошибка инструмента (tool_result с is_error)
	KindSystem        // системная запись сессии (type:"system" + subtype)
	KindFallback      // подмена модели после отказа (model_refusal_fallback)
)

// Event — одно наблюдаемое действие сессии.
type Event struct {
	Time   time.Time
	Source string // main либо имя агента
	Kind   Kind
	Tool   string // имя инструмента для KindTool/KindDelegate/KindSkill
	Detail string // готовая к показу деталь, пробелы схлопнуты, длина обрезана
	Model  string // модель записи, если известна
	Effort string // плоское поле effort записи, если есть

	// Level — уровень системной записи (info/warning), если он задан.
	Level string
	// Denial — toolDenialKind записи: ошибка от локального правила или
	// отказа пользователя, а не от самого инструмента. В UI показывать тише.
	Denial string

	// ToolID — id блока tool_use, ключ сшивки вызова с его результатом.
	// Пуст у всего, что вызовом инструмента не является.
	ToolID string
	// Path — полный file_path файловой операции. Хранится отдельно от Detail
	// намеренно: в логе деталь остаётся парой последних компонент пути, а
	// окно файлов показывает путь целиком.
	Path string
	// Status — исход вызова. Ставит линкер по результату, а не декодер: в
	// самой записи вызова исхода ещё нет.
	Status Status
	// Dur — разница отметок ЗАПИСЕЙ результата и вызова, а не время работы
	// инструмента. У Agent/Task результат приходит как async_launched, то
	// есть это время запуска делегирования; поэтому длительность показывают
	// только табы mcp и files, где таких событий не бывает.
	Dur time.Duration
	// Fail — текст ошибки результата, обрезанный ещё при декоде: содержимое
	// tool_result доходит до сотен килобайт, и держать его на строку окна нельзя.
	Fail string
}

// Status — исход вызова инструмента.
type Status int

// Исходы вызова. Нулевое значение — «результата ещё нет»: незакрытый вызов
// живой сессии не должен притворяться успешным, а поле is_error записи
// трёхзначно (true / false / отсутствует) и одним bool не выражается.
const (
	StatusPending Status = iota
	StatusOK             // is_error != true
	StatusError          // is_error == true: сбой самого инструмента
	StatusDenied         // is_error == true + toolDenialKind: отказ, а не сбой
)

// String возвращает короткое имя исхода.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusError:
		return "err"
	case StatusDenied:
		return "denied"
	default:
		return "pending"
	}
}

// String возвращает короткое имя рода события.
func (k Kind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindTool:
		return "tool"
	case KindDelegate:
		return "delegate"
	case KindSkill:
		return "skill"
	case KindError:
		return "error"
	case KindSystem:
		return "system"
	case KindFallback:
		return "fallback"
	default:
		return "unknown"
	}
}
