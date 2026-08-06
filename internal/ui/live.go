package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/KratosUAE/ccon/internal/cost"
	"github.com/KratosUAE/ccon/internal/parse"
	"github.com/KratosUAE/ccon/internal/session"
)

// ModeLive — индикатор режима для сессии, которая пишется прямо сейчас.
const ModeLive = "live"

// Batch — порция живых событий вместе с пересчитанным агрегатом.
//
// Батч, а не событие: обновление viewport стоит дороже вставки, и всплеск в
// тысячу строк при старте живой сессии по одному событию занимает секунду
// против тридцати миллисекунд пачкой.
type Batch struct {
	Events []parse.Event
	// Results — результаты вызовов, приехавшие в этой же порции. Сшиваются
	// ПОСЛЕ того, как на учёт встали все события порции: тогда порядок внутри
	// неё перестаёт что-либо значить.
	Results []parse.Result
	Totals  cost.Totals
	Agents  []Agent
	// Model — модель сессии для шапки. Считается по всему потоку, а не по
	// последнему событию: в сессии с чередованием моделей шапка иначе мигает
	// каждые сто миллисекунд.
	Model string
	// Status — что показать в строке состояния: например, ход догона.
	Status string
	// Skipped — сколько строк транскрипта не разобралось. Молчать о них
	// нельзя: при смене формата лог просто опустеет без объяснений.
	Skipped int
}

// Feed — источник живых данных. Разбор строк и подсчёт расхода остаются
// снаружи: интерфейсу достаётся готовое.
type Feed struct {
	Batches <-chan Batch
	Renames <-chan session.Rename
	Errs    <-chan error

	// Names — сверка подписей субагентов на случай, если сигнал о
	// переименовании не поместился в канал.
	Names func() map[string]string
	// Cancel останавливает наблюдение при выходе из интерфейса.
	Cancel func()
}

// Сообщения живого режима.
type (
	batchMsg  Batch
	renameMsg session.Rename
	errMsg    struct{ err error }
	// doneMsg помечен источником: в гардинге видно, какой канал иссяк.
	doneMsg struct{ source string }
)

// listen собирает команды ожидания всех каналов источника.
func (m *Model) listen() tea.Cmd {
	if m.feed == nil {
		return nil
	}
	return tea.Batch(m.waitBatch(), m.waitRename(), m.waitErr())
}

func (m *Model) waitBatch() tea.Cmd {
	return func() tea.Msg {
		batch, ok := <-m.feed.Batches
		if !ok {
			return doneMsg{"batches"}
		}
		return batchMsg(batch)
	}
}

func (m *Model) waitRename() tea.Cmd {
	return func() tea.Msg {
		r, ok := <-m.feed.Renames
		if !ok {
			return doneMsg{"renames"}
		}
		return renameMsg(r)
	}
}

func (m *Model) waitErr() tea.Cmd {
	return func() tea.Msg {
		err, ok := <-m.feed.Errs
		if !ok {
			return doneMsg{"errs"}
		}
		return errMsg{err}
	}
}

// applyBatch кладёт порцию событий в лог и обновляет футер. Фильтр на
// счётчики не влияет: они считаются по всему потоку, а не по видимому.
func (m *Model) applyBatch(b Batch) {
	if b.Model != "" {
		m.opts.Model = b.Model
	}
	m.skipped = b.Skipped
	m.opts.Totals = b.Totals
	m.progress = b.Status
	if len(b.Agents) > 0 {
		m.opts.Agents = b.Agents
	}
	m.PushBatch(b.Events)
	m.applyResults(b.Results)
}

// applyRename переподписывает уже показанные строки и сверяется со снимком
// имён: сигнал мог не поместиться в канал и потеряться.
func (m *Model) applyRename(r session.Rename) {
	names := map[string]string{r.ID: r.Name}
	if m.feed != nil && m.feed.Names != nil {
		// Снимок отдаётся копией, мутировать его нельзя — дополняем свой.
		for id, name := range m.feed.Names() {
			names[id] = name
		}
	}

	// Одна перестройка панелей на всю пачку имён, а не на каждое имя.
	if !m.log.renameAll(names) {
		return
	}
	m.refresh()
}

// stop гасит наблюдение: тейлеры обязаны остановиться вместе с интерфейсом.
func (m *Model) stop() {
	if m.feed != nil && m.feed.Cancel != nil {
		m.feed.Cancel()
	}
}
