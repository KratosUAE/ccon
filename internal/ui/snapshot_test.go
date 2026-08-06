package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/KratosUAE/ccon/internal/cost"
	"github.com/KratosUAE/ccon/internal/parse"
)

// loadFixture собирает события и расход из транскрипта-фикстуры.
func loadFixture(t *testing.T) ([]parse.Event, cost.Totals, []Agent) {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "testdata", "tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var events []parse.Event
	acc := cost.NewAccumulator()
	counts := map[string]int{}

	_, err = parse.Scan(f, func(line []byte) error {
		d, ok := parse.Decode(line)
		if !ok {
			return nil
		}
		for _, ev := range d.Events {
			events = append(events, ev)
			counts[ev.Source]++
		}
		if d.Usage != nil {
			acc.Add(*d.Usage)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	agents := make([]Agent, 0, len(counts))
	for name, n := range counts {
		agents = append(agents, Agent{Name: name, Count: n})
	}
	return events, acc.Totals(), agents
}

// TestSnapshotFrame печатает кадр целиком: это ровно то, что модель отдаёт
// терминалу, до того как рендерер заменит пробелы движением курсора.
func TestSnapshotFrame(t *testing.T) {
	events, totals, agents := loadFixture(t)

	for _, size := range []struct{ w, h int }{{80, 20}, {40, 16}, {80, 6}} {
		m := New(Options{
			Project: "claude_con_ecc", Model: "claude-opus-5", Effort: "high",
			Mode: ModeArchive, Events: events, Totals: totals, Agents: agents,
		})
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})

		frame := m.View().Content
		lines := strings.Split(frame, "\n")

		if len(lines) > size.h {
			t.Errorf("%dx%d: кадр занял %d строк", size.w, size.h, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got > size.w {
				t.Errorf("%dx%d: строка %d шире окна: %d колонок", size.w, size.h, i, got)
			}
		}

		t.Logf("\n=== КАДР %dx%d ===\n%s\n=== конец кадра ===",
			size.w, size.h, stripANSI(frame))
	}
}

// stripANSI убирает стили, оставляя раскладку: снимок должен читаться в отчёте.
func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
