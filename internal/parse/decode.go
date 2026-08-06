package parse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Пределы длины деталей, унаследованы от bash-прототипа.
const (
	maxText    = 150
	maxCommand = 100
	maxPattern = 55
	maxDesc    = 50
	maxSkill   = 60
	maxJSON    = 75
	maxError   = 110
	maxName    = 60 // имена моделей, агентов и меток

	// maxToolName шире maxName и применяется ТОЛЬКО к имени инструмента:
	// имена MCP-ручек уже сегодня доходят до 60 рун
	// (mcp__some_server__a_rather_long_method_name), а общий
	// предел резал бы метод в тишине — разбор идёт по уже обрезанному имени.
	maxToolName = 120
	// maxPath — потолок полного пути файловой операции. Длина приходит
	// снаружи, потолок обязателен; наблюдаемый максимум по корпусу — 147.
	maxPath = 256
)

const (
	// sourceMain — имя источника главного потока сессии.
	sourceMain = "main"

	// syntheticModel — псевдомодель записей об ошибках API, не тарифицируется.
	syntheticModel = "<synthetic>"

	// speedFast — ускоренный режим выдачи, тарифицируется вдвое.
	speedFast = "fast"
)

// record — сырая JSONL-запись транскрипта, только нужные поля.
//
// Поля системных записей и toolDenialKind лежат на ВЕРХНЕМ уровне, а не
// внутри message — проверено по живому корпусу.
type record struct {
	Type             string   `json:"type"`
	Subtype          string   `json:"subtype"`
	Timestamp        string   `json:"timestamp"`
	RequestID        string   `json:"requestId"`
	SessionID        string   `json:"sessionId"`
	Effort           string   `json:"effort"` // плоское поле верхнего уровня
	Cwd              string   `json:"cwd"`    // рабочий каталог сессии
	AttributionAgent string   `json:"attributionAgent"`
	Message          *message `json:"message"`

	// Системные записи (type:"system"). Content — сырой JSON: у части типов
	// записей верхнеуровневое content не строка, и жёсткий тип уронил бы
	// разбор всей записи.
	Content            json.RawMessage `json:"content"`
	Level              string          `json:"level"`
	DurationMs         int64           `json:"durationMs"`
	MessageCount       int64           `json:"messageCount"`
	HookCount          int64           `json:"hookCount"`
	CompactMetadata    *compactMeta    `json:"compactMetadata"`
	OriginalModel      string          `json:"originalModel"`
	FallbackModel      string          `json:"fallbackModel"`
	APIRefusalCategory string          `json:"apiRefusalCategory"`
	ToolDenialKind     string          `json:"toolDenialKind"`
}

type compactMeta struct {
	Trigger    string `json:"trigger"`
	PreTokens  int64  `json:"preTokens"`
	PostTokens int64  `json:"postTokens"`
}

type message struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"` // top-level всегда null
	Content    json.RawMessage `json:"content"`
	Usage      *rawUsage       `json:"usage"`
}

type rawUsage struct {
	Input           int64        `json:"input_tokens"`
	Output          int64        `json:"output_tokens"`
	CacheRead       int64        `json:"cache_read_input_tokens"`
	CacheCreateFlat int64        `json:"cache_creation_input_tokens"`
	CacheCreate     *cacheDetail `json:"cache_creation"`
	Speed           string       `json:"speed"`
	ServerToolUse   *serverTools `json:"server_tool_use"`
}

type serverTools struct {
	WebSearch int64 `json:"web_search_requests"`
	WebFetch  int64 `json:"web_fetch_requests"`
}

type cacheDetail struct {
	Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`          // id блока tool_use
	ToolUseID string          `json:"tool_use_id"` // ключ сшивки блока tool_result
	Input     json.RawMessage `json:"input"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// Decoded — всё, что дала одна строка транскрипта.
//
// Структура, а не набор возвращаемых значений: разбор строки будет отдавать
// не только события и расход, и новое поле не должно стоить правки каждого
// вызывающего.
type Decoded struct {
	Events []Event
	Usage  *Usage
	// Results — блоки tool_result строки: корм линкера, а не события лога.
	// В логе они не показываются (ошибки из них уже приезжают отдельным
	// KindError), поэтому текстовый дамп их просто игнорирует.
	Results []Result
}

// Decode разбирает одну строку JSONL.
//
// Одна строка порождает 0..N событий и не более одного сэмпла расхода.
// ok == false означает, что строка не разобралась как JSON и должна быть
// молча пропущена; неизвестный тип записи даёт ok == true и ноль событий.
func Decode(line []byte) (Decoded, bool) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return Decoded{}, false
	}

	// Битый timestamp не повод ронять запись: остаётся нулевое время.
	ts, _ := time.Parse(time.RFC3339, rec.Timestamp)

	// Все текстовые поля события чистятся при разборе: содержимое приходит
	// извне (MCP-серверы, конфиги агентов), управляющие символы в лог не идут.
	base := Event{
		Time:   ts,
		Source: sourceMain,
		Effort: Truncate(rec.Effort, maxName),
		Level:  Truncate(rec.Level, maxName),
	}
	if rec.Message != nil {
		base.Model = Truncate(rec.Message.Model, maxName)
	}
	if rec.AttributionAgent != "" {
		base.Source = Truncate(rec.AttributionAgent, maxName)
	}

	var d Decoded
	switch rec.Type {
	case "system":
		// system/<subtype> — это пара полей, а не литерал типа записи.
		d.Events = systemEvents(base, &rec)
	case "assistant":
		if rec.Message == nil {
			return Decoded{}, true
		}
		d.Events = assistantEvents(base, rec.Message.Content)
		d.Usage = sampleUsage(&rec, ts)
	case "user":
		if rec.Message == nil {
			return Decoded{}, true
		}
		base.Denial = Truncate(rec.ToolDenialKind, maxName)
		d.Events, d.Results = userEvents(base, rec.Message.Content)
	}
	return d, true
}

// systemEvents разбирает системную запись по её subtype. Неизвестный subtype
// не теряется молча: он попадает в лог вместе со своим содержимым.
func systemEvents(base Event, rec *record) []Event {
	ev := base
	ev.Kind = KindSystem
	// Только строковый content: объект или массив в лог сырым JSON не идут.
	content := plainText(rec.Content)

	switch rec.Subtype {
	case "turn_duration":
		ev.Detail = fmt.Sprintf("turn %s · messages %d", humanMillis(rec.DurationMs), rec.MessageCount)
	case "compact_boundary":
		trigger, pre, post := "", int64(0), int64(0)
		if m := rec.CompactMetadata; m != nil {
			trigger, pre, post = m.Trigger, m.PreTokens, m.PostTokens
		}
		ev.Detail = Truncate(fmt.Sprintf("context compaction: %d → %d tokens · %s", pre, post, trigger), maxText)
	case "stop_hook_summary":
		// Тела хуков — многокилобайтные скрипты, в лог идёт только их число.
		ev.Detail = fmt.Sprintf("stop hooks: %d", rec.HookCount)
	case "local_command":
		ev.Detail = Truncate(orSubtype(commandName(content), rec.Subtype), maxText)
	case "model_refusal_fallback":
		ev.Kind = KindFallback
		detail := fmt.Sprintf("%s → %s", orUnknown(rec.OriginalModel), orUnknown(rec.FallbackModel))
		if rec.APIRefusalCategory != "" {
			detail += " · " + rec.APIRefusalCategory
		}
		ev.Detail = Truncate(detail, maxText)
	case "away_summary", "informational":
		ev.Detail = Truncate(orSubtype(content, rec.Subtype), maxText)
	default:
		ev.Detail = Truncate(orSubtype(strings.TrimSpace(rec.Subtype+" "+content), "system"), maxText)
	}
	return []Event{ev}
}

// orSubtype не даёт событию остаться без текста: если содержимого нет или оно
// не строковое, в логе остаётся хотя бы подтип записи.
func orSubtype(detail, subtype string) string {
	if strings.TrimSpace(detail) != "" {
		return detail
	}
	if subtype != "" {
		return subtype
	}
	return "system"
}

// commandName вытаскивает имя слэш-команды из разметки local_command.
// Вложенный или незакрытый тег деградирует до пригодного к показу текста.
func commandName(content string) string {
	_, rest, found := strings.Cut(content, "<command-name>")
	if !found {
		return content
	}
	name, _, _ := strings.Cut(rest, "</command-name>")
	if inner, _, nested := strings.Cut(name, "<"); nested {
		name = inner
	}
	return name
}

// plainText отдаёт содержимое, только если это JSON-строка: объект или массив
// иначе утекли бы в лог сырым дампом.
func plainText(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// humanMillis переводит длительность в короткую запись для лога.
// Ветка выбирается по УЖЕ округлённому значению, иначе 59 950 мс печатается
// как «60.0с»; от часа идёт своя ветка, чтобы не получать «1440м0с».
func humanMillis(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	if tenths := math.Round(float64(ms)/100) / 10; tenths < 60 {
		return fmt.Sprintf("%.1fs", tenths)
	}

	sec := int64(math.Round(float64(ms) / 1000))
	if sec < 3600 {
		return fmt.Sprintf("%dm%ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh%dm", sec/3600, sec%3600/60)
}

// RecordCwd отдаёт рабочий каталог, записанный в строке транскрипта.
// По нему определяется имя проекта, когда путь к сессии задан напрямую:
// каталог хранилища назван слагом и для показа не годится.
func RecordCwd(line []byte) string {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return ""
	}
	return Truncate(rec.Cwd, maxName)
}

// blocks разбирает message.content; content бывает строкой или null —
// в этом случае блоков просто нет.
func blocks(raw json.RawMessage) []block {
	if len(raw) == 0 {
		return nil
	}
	var bs []block
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil
	}
	return bs
}

func assistantEvents(base Event, raw json.RawMessage) []Event {
	var out []Event
	for _, b := range blocks(raw) {
		switch b.Type {
		case "text":
			detail := Truncate(b.Text, maxText)
			if detail == "" {
				continue
			}
			ev := base
			ev.Kind = KindText
			ev.Detail = detail
			out = append(out, ev)
		case "tool_use":
			ev := base
			ev.Kind, ev.Detail = toolDetail(b.Name, b.Input)
			ev.Tool = Truncate(b.Name, maxToolName)
			// Ключ сшивки кладётся ЛЮБОМУ вызову, а не только файловому и
			// MCP: отбор — дело панели, а хранение ключа ничего не стоит.
			ev.ToolID = b.ID
			ev.Path = filePath(b.Name, b.Input)
			out = append(out, ev)
		}
	}
	return out
}

// userEvents разбирает запись с результатами вызовов. Событие рождает только
// ошибка (транскрипт не должен потерять красные строки), а результатом линкера
// становится КАЖДЫЙ блок tool_result: успешный вызов тоже обязан получить свой
// исход, иначе он навсегда останется «ещё выполняется».
func userEvents(base Event, raw json.RawMessage) ([]Event, []Result) {
	var events []Event
	var results []Result

	for _, b := range blocks(raw) {
		if b.Type != "tool_result" {
			continue
		}
		// Обрезка здесь, до попадания в структуру: содержимое результата
		// доходит до 147 КБ, а в памяти держится по строке на событие окна.
		text := Truncate(contentText(b.Content), maxError)
		results = append(results, Result{
			ToolUseID: b.ToolUseID,
			Time:      base.Time,
			IsError:   b.IsError,
			Denial:    base.Denial,
			Text:      text,
		})

		if !b.IsError {
			continue
		}
		ev := base
		ev.Kind = KindError
		ev.Detail = text
		events = append(events, ev)
	}
	return events, results
}

// toolDetail выбирает род события и деталь показа по имени инструмента.
func toolDetail(name string, input json.RawMessage) (Kind, string) {
	switch name {
	case "Bash":
		return KindTool, Truncate(strField(input, "command"), maxCommand)
	case "Read", "Edit", "Write":
		return KindTool, lastTwo(strField(input, "file_path"))
	case "Grep":
		return KindTool, Truncate(strField(input, "pattern"), maxPattern)
	case "Skill":
		return KindSkill, Truncate(strField(input, "skill"), maxSkill)
	case "Agent", "Task":
		sub := strField(input, "subagent_type")
		if sub == "" {
			sub = "?"
		}
		desc := Truncate(strField(input, "description"), maxDesc)
		return KindDelegate, strings.TrimSpace(sub + " " + desc)
	default:
		return KindTool, Truncate(compactJSON(input), maxJSON)
	}
}

// filePath отдаёт полный путь файловой операции; у прочих инструментов пути
// нет и быть не должно — MCP-ручки, работающие с файлами, кладут его в свои
// ключи (relative_path у serena, path у Grep) и в окно файлов не попадают.
//
// Ключ input один для всех файловых инструментов. Для NotebookEdit и
// MultiEdit это допущение: в живом корпусе они не встречались ни разу, и
// проверить форму их input не на чем. Read с битым JSON аргументов
// (__unparsedToolInput) честно остаётся без пути — вылавливать его регуляркой
// из обрезка дороже, чем нарисовать «?».
func filePath(name string, input json.RawMessage) string {
	if _, ok := FileOp(name); !ok {
		return ""
	}
	return Truncate(strField(input, "file_path"), maxPath)
}

// strField достаёт строковое поле объекта input; кривой вход даёт пустую строку.
func strField(raw json.RawMessage, field string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(m[field], &s); err != nil {
		return ""
	}
	return s
}

// contentText разворачивает content блока tool_result: он бывает и строкой,
// и массивом блоков — обе формы должны давать один и тот же текст.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var bs []block
	if err := json.Unmarshal(raw, &bs); err == nil {
		parts := make([]string, 0, len(bs))
		for _, b := range bs {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return compactJSON(raw)
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	return buf.String()
}

// lastTwo оставляет от пути две последние компоненты: путь из одной
// компоненты отдаётся целиком, пустой — пустым. Границу компонент задаёт
// PathSep: у виндового пути её ставит «\», и без этого от «D:\Work\x.ps1» не
// отрезалось бы ничего. Собирается результат всегда через «/» — колонка
// показывает две компоненты, а не путь, по которому куда-то ходят.
func lastTwo(path string) string {
	win := WindowsPath(path)
	parts := strings.FieldsFunc(path, func(r rune) bool { return PathSep(r, win) })
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

func sampleUsage(rec *record, ts time.Time) *Usage {
	ru := rec.Message.Usage
	// Записи API-ошибок приходят с моделью "<synthetic>", нулевым usage и
	// пустым requestId: событие они порождают, расхода за ними нет.
	if ru == nil || rec.Message.Model == syntheticModel {
		return nil
	}

	u := &Usage{
		RequestID:  rec.RequestID,
		MessageID:  rec.Message.ID,
		Model:      rec.Message.Model,
		StopReason: rec.Message.StopReason,
		Time:       ts,
		Input:      clampTokens(ru.Input),
		Output:     clampTokens(ru.Output),
		CacheRead:  clampTokens(ru.CacheRead),
		Fast:       ru.Speed == speedFast,
	}
	if ru.ServerToolUse != nil {
		u.WebSearch = clampTokens(ru.ServerToolUse.WebSearch)
	}
	// Вложенный cache_creation — основной путь; плоское поле — запасной,
	// тогда вся запись кэша считается пятиминутной.
	if ru.CacheCreate != nil {
		u.Cache5m = clampTokens(ru.CacheCreate.Ephemeral5m)
		u.Cache1h = clampTokens(ru.CacheCreate.Ephemeral1h)
	} else {
		u.Cache5m = clampTokens(ru.CacheCreateFlat)
	}

	// Без requestId ключ дедупликации вырождается в один на всю сессию,
	// а нулевой расход всё равно ничего не добавляет к сводке.
	if u.RequestID == "" && u.Total() == 0 {
		return nil
	}
	return u
}
