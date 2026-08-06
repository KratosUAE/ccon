package parse

import "strings"

// mcpPrefix — приставка имён инструментов MCP: mcp__<сервер>__<метод>.
const mcpPrefix = "mcp__"

// MCPParts разбирает имя MCP-инструмента на сервер и метод.
//
// Режется ПЕРВОЕ вхождение "__" после приставки: сервер — до него, метод —
// весь остаток вместе с любыми "__" внутри. Имён с "__" внутри половин в
// живом корпусе нет, но «остаток целиком в метод» переживёт их появление, а
// «последнее вхождение» — нет.
//
// ok == false, если приставки нет, второго "__" нет либо одна из половин
// пуста: "mcp__claude-in-chrome__" — это обрезок правила разрешений, а не имя
// вызванного инструмента. Аллокаций нет: возвращаются подстроки входа.
func MCPParts(tool string) (server, method string, ok bool) {
	rest, found := strings.CutPrefix(tool, mcpPrefix)
	if !found {
		return "", "", false
	}
	server, method, found = strings.Cut(rest, "__")
	if !found || server == "" || method == "" {
		return "", "", false
	}
	return server, method, true
}

// fileOps — закрытый список файловых инструментов, один на весь проект:
// расширяется одной правкой, и окно файлов не разъезжается с фильтром.
//
// Artifact сюда не входит намеренно: file_path у него есть, но это «показать
// пользователю», а не чтение или запись. NotebookEdit и MultiEdit в живом
// корпусе не встретились ни разу — ветки стоят дёшево, но форма их input
// живыми данными не проверена.
var fileOps = map[string]rune{
	"Read":         'R',
	"Edit":         'E',
	"Write":        'W',
	"NotebookEdit": 'N',
	"MultiEdit":    'E',
}

// FileOp отдаёт букву файловой операции по имени инструмента.
func FileOp(tool string) (rune, bool) {
	op, ok := fileOps[tool]
	return op, ok
}

// Haystack — то, что видит фильтр: ровно то, что показано в строке события,
// на любом из табов. Одна функция на все виды, иначе поиск разъедется с
// показом. Ярлык берётся через Label — тот же источник, что и у колонки
// ярлыка (PartsFor), иначе "/system"/"/ERROR"/"/swap" не находят строк, где
// эти ярлыки видны.
//
// Исход вызова (Status) в стог не входит: у него свой тумблер, и иначе
// "/err" начал бы спорить с ним за одни и те же строки.
func Haystack(ev Event) string {
	var b strings.Builder
	for _, part := range []string{ev.Source, Label(ev), ev.Path, ev.Detail, ev.Fail} {
		if part == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(part)
	}
	return b.String()
}
