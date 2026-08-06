package parse

import "strings"

// Ширины колонок plain-строки лога. labelWidth вмещает самое длинное имя
// инструмента ("MultiEdit", 9 рун) и любой из служебных ярлыков: колонка
// деталей обязана стоять на месте, чем бы ярлык ни оказался.
const (
	sourceWidth = 18
	labelWidth  = 9
)

// Узкие окна: фиксированные зоны съедают 39 колонок из 40, и деталь события
// не видна вовсе. Колонка источника сжимается — имя агента угадывается по
// началу, а деталь без сжатия не видна никак.
const (
	narrowWidth      = 70
	verynarrowWidth  = 55
	narrowSource     = 10
	veryNarrowSource = 8
)

// LabelWidth — ширина колонки ярлыка; нужна TUI для расчёта отступов.
func LabelWidth() int { return labelWidth }

// SourceWidth — ширина колонки источника для окна заданной ширины.
// width <= 0 означает «ширина неизвестна», берётся полная колонка.
func SourceWidth(width int) int {
	switch {
	case width <= 0 || width >= narrowWidth:
		return sourceWidth
	case width >= verynarrowWidth:
		return narrowSource
	default:
		return veryNarrowSource
	}
}

// Line — plain-представление события для режима --dump и для тестов:
// "HH:MM:SS  <источник:18>  <ярлык:7>  <деталь>". Время локальное.
func Line(ev Event) string {
	ts, source, label, detail := Parts(ev)
	return strings.TrimRight(ts+"  "+source+"  "+label+"  "+detail, " ")
}

// Parts отдаёт колонки строки лога при полной ширине окна.
func Parts(ev Event) (ts, source, label, detail string) {
	return PartsFor(ev, 0)
}

// PartsFor отдаёт колонки строки лога по отдельности и уже подогнанными по
// ширине: и plain-режим, и TUI обязаны раскладывать их одинаково, а раскраска
// поверх — дело вызывающего. width — ширина окна, 0 означает «не ограничено».
func PartsFor(ev Event, width int) (ts, source, label, detail string) {
	return ev.Time.Local().Format("15:04:05"),
		fit(ev.Source, SourceWidth(width)),
		fit(Label(ev), labelWidth),
		dropControl(ev.Detail)
}

// Label — ярлык колонки: имя инструмента у KindTool (и родственных), у
// text/error/system/fallback — служебная метка. Общий источник для PartsFor
// и Haystack: иначе поиск не находит строки, где ярлык виден глазом
// ("/system", "/ERROR", "/swap").
func Label(ev Event) string {
	switch ev.Kind {
	case KindText:
		return "│"
	case KindError:
		return "✗ ERROR"
	case KindSystem:
		return "system"
	case KindFallback:
		return "⚠ swap"
	default:
		return ev.Tool
	}
}

// Fit подгоняет значение под колонку заданной ширины, Clean вычищает
// управляющие символы. Открыты наружу ради табов mcp и files: они складывают
// строку из своих колонок, но правила показа обязаны остаться общими с
// транскриптом, иначе таблицы разъедутся между собой.
func Fit(s string, n int) string { return fit(s, n) }

// Clean — см. Fit.
func Clean(s string) string { return dropControl(s) }

// HumanMillis — короткая запись длительности («0.4s», «2m3s», «15h26m»).
// Открыта наружу ради колонки длительности табов: длительности растянуты на
// семь порядков (3 мс … 15 ч 26 м), и вторая такая функция неизбежно
// разошлась бы с той, которой уже пользуется лог.
func HumanMillis(ms int64) string { return humanMillis(ms) }

// WindowsPath — путь записан по-виндовому, и только у такого «\» разделяет
// компоненты. Открыта наружу ради обрезки пути в табе файлов: правило «где у
// этого пути границы» обязано быть общим с транскриптом.
//
// Считать «\» разделителем безусловно нельзя: на Linux это ЛЕГАЛЬНЫЙ символ
// имени файла, и «/home/u/weird\name.txt» — ОДИН файл, а не две компоненты;
// разрезав его, показ соврал бы про имя. runtime.GOOS тоже не признак:
// транскрипт мог быть записан на другой машине, а решать надо по самому пути.
//
// Признаков ровно два, и оба невозможны в абсолютном пути Linux:
//   - буква диска: «D:\Work», «D:/Work» (двоеточие вторым символом);
//   - UNC: «\\server\share».
//
// Относительный виндовый путь («scripts\admin.ps1») от Linux-имени с
// бэкслешем неотличим и признан НЕ виндовым: file_path в транскрипте всегда
// абсолютный, а цена ошибки в другую сторону — соврать про имя файла.
func WindowsPath(path string) bool {
	if strings.HasPrefix(path, `\\`) {
		return true
	}
	if len(path) < 3 || path[1] != ':' || (path[2] != '\\' && path[2] != '/') {
		return false
	}
	c := path[0]
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// PathSep — руна разделяет компоненты пути, у которого WindowsPath == win.
// У виндового пути разделителями считаются ОБА символа: «/» в имени файла на
// Windows невозможен, а смешанная запись («D:/Work/scripts\admin.ps1») в живых
// путях встречается.
func PathSep(r rune, win bool) bool { return r == '/' || win && r == '\\' }

// Truncate схлопывает любые пробельные последовательности в один пробел,
// вычищает управляющие символы и обрезает результат до n рун, ставя
// многоточие, если что-то отрезано.
func Truncate(s string, n int) string {
	s = strings.Join(strings.Fields(dropControl(s)), " ")
	if n < 0 {
		n = 0
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// fit приводит строку ровно к n рунам: короткую дополняет пробелами,
// длинную режет с многоточием. Колонки не должны разъезжаться.
//
// Санитайз здесь обязателен, а не только в Truncate: имя инструмента приходит
// от MCP-сервера, имя источника — из конфига агента, оба управляются извне.
func fit(s string, n int) string {
	s = dropControl(s)
	r := []rune(s)
	switch {
	case len(r) == n:
		return s
	case len(r) < n:
		return s + strings.Repeat(" ", n-len(r))
	case n <= 1:
		return string(r[:n])
	default:
		return string(r[:n-1]) + "…"
	}
}

// dropControl заменяет управляющие символы C0/C1 пробелом: escape-последовательности
// из вывода инструментов не должны доезжать до терминала и портить экран.
func dropControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
}
