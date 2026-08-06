package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/KratosUAE/ccon/internal/session"
)

var pickNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func TestHumanTime(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"сегодня", pickNow.Add(-2 * time.Hour), "10:00"},
		{"вчера", pickNow.Add(-20 * time.Hour), "yesterday 16:00"},
		{"позавчера", pickNow.Add(-48 * time.Hour), "03.08"},
		{"нет времени", time.Time{}, "—"},
	}
	for _, c := range cases {
		if got := HumanTime(c.at, pickNow); got != c.want {
			t.Errorf("%s: %q, ожидалось %q", c.name, got, c.want)
		}
	}
}

// Колонка времени обязана вмещать самое длинное значение с зазором: иначе
// маркер свежести слипается со временем. Ровно это сломал перевод на
// английский — «вчера 13:18» умещалось, "yesterday 13:18" нет.
func TestTimeColumnFitsLongestValue(t *testing.T) {
	longest := 0
	for _, at := range []time.Time{
		pickNow, pickNow.Add(-20 * time.Hour), pickNow.Add(-48 * time.Hour), {},
	} {
		if n := len([]rune(HumanTime(at, pickNow))); n > longest {
			longest = n
		}
	}
	if longest >= timeCol {
		t.Errorf("самое длинное время %d рун при колонке %d — зазора нет", longest, timeCol)
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{512, "512 B"},
		{2048, "2 KB"},
		{5 << 20, "5 MB"},
	}
	for _, c := range cases {
		if got := HumanSize(c.n); got != c.want {
			t.Errorf("%d → %q, ожидалось %q", c.n, got, c.want)
		}
	}
}

func testGroups() []session.Group {
	return []session.Group{
		{Slug: "-home-u-alpha", Project: "alpha", Entries: []session.Entry{
			{SessionID: "a1", Title: "свежая альфа", Prompt: "продолжаем",
				Modified: pickNow.Add(-time.Hour), Newest: true},
			{SessionID: "a2", Title: "старая альфа", Modified: pickNow.Add(-40 * time.Hour), Size: 4096},
		}},
		{Slug: "-home-u-beta", Project: "beta", Entries: []session.Entry{
			{SessionID: "b1", Prompt: "почини сборку", Modified: pickNow.Add(-50 * time.Hour), Newest: true},
		}},
	}
}

func ready(p *Picker) *Picker {
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return p
}

// Выход без выбора обязан быть отличим от выбора: иначе Esc откроет случайную
// сессию вместо того, чтобы ничего не сделать.
func TestPickerQuitIsNotChoice(t *testing.T) {
	p := ready(NewPicker(testGroups(), pickNow))

	p.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if _, ok := p.Chosen(); ok {
		t.Error("после выхода Chosen сообщает о выборе")
	}
}

func TestPickerEnterChoosesUnderCursor(t *testing.T) {
	p := ready(NewPicker(testGroups(), pickNow))

	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	e, ok := p.Chosen()
	if !ok {
		t.Fatal("Enter не привёл к выбору")
	}
	if e.SessionID != "a2" {
		t.Errorf("выбрана %q, ожидалась a2", e.SessionID)
	}
}

// Курсор идёт сквозь границы проектов: список плоский, шапки его не режут.
func TestPickerCursorCrossesGroups(t *testing.T) {
	p := ready(NewPicker(testGroups(), pickNow))

	for range 2 {
		p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	e, _ := p.Chosen()
	if e.SessionID != "b1" {
		t.Errorf("выбрана %q, ожидалась b1 из соседнего проекта", e.SessionID)
	}
}

// Курсор упирается в границы, а не уезжает за список.
func TestPickerCursorStaysInRange(t *testing.T) {
	p := ready(NewPicker(testGroups(), pickNow))

	for range 10 {
		p.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if e, _ := p.Chosen(); e.SessionID != "a1" {
		t.Errorf("вверх: выбрана %q, ожидалась первая a1", e.SessionID)
	}

	p = ready(NewPicker(testGroups(), pickNow))
	for range 10 {
		p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if e, _ := p.Chosen(); e.SessionID != "b1" {
		t.Errorf("вниз: выбрана %q, ожидалась последняя b1", e.SessionID)
	}
}

func TestPickerViewShowsTitlesAndProjects(t *testing.T) {
	frame := ready(NewPicker(testGroups(), pickNow)).View().Content

	for _, want := range []string{"alpha", "beta", "свежая альфа", "продолжаем", "▸"} {
		if !strings.Contains(frame, want) {
			t.Errorf("в кадре нет %q:\n%s", want, frame)
		}
	}
}

// Сессия без заголовка обязана честно сказать об этом, а не показать обрывок
// реплики на месте названия.
func TestPickerViewMarksMissingTitle(t *testing.T) {
	frame := ready(NewPicker(testGroups(), pickNow)).View().Content

	if !strings.Contains(frame, noTitle) {
		t.Errorf("нет пометки об отсутствующем заголовке:\n%s", frame)
	}
}

// Кадр не имеет права быть выше окна: лишние строки прокручивают терминал и
// оставляют мусор после выхода.
func TestPickerViewFitsWindow(t *testing.T) {
	var groups []session.Group
	for i := range 20 {
		groups = append(groups, session.Group{
			Project: "проект",
			Entries: []session.Entry{{SessionID: "s", Title: "заголовок", Prompt: "реплика",
				Modified: pickNow.Add(-time.Duration(i) * time.Hour)}},
		})
	}

	p := NewPicker(groups, pickNow)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 12})

	if got := strings.Count(p.View().Content, "\n") + 1; got > 12 {
		t.Errorf("строк в кадре %d, окно 12", got)
	}
}

// До прихода размера окна рисовать нечего, но и падать нельзя.
func TestPickerViewBeforeSize(t *testing.T) {
	if got := NewPicker(testGroups(), pickNow).View().Content; got == "" {
		t.Error("пустой кадр до получения размера окна")
	}
}

func TestPickEmptyGroups(t *testing.T) {
	e, ok, err := Pick(nil, pickNow)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if ok || e.SessionID != "" {
		t.Errorf("пустой список дал выбор %+v", e)
	}
}
