package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/KratosUAE/ccon/internal/session"
	"github.com/KratosUAE/ccon/internal/ui"
)

// runList показывает сессии всех проектов и открывает выбранную.
//
// Без терминала выбирать нечем — тогда список просто печатается: он остаётся
// полезен как справочник идентификаторов для --session, и это лучше, чем
// отказ «нужен терминал» в ответ на осмысленный вопрос «что у меня есть».
func runList(ctx context.Context, stdout, stderr io.Writer, dump, follow bool, view ui.View) int {
	groups, err := session.List(session.ListOptions{})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ccon: %v\n", err)
		return exitFail
	}
	if len(groups) == 0 {
		_, _ = fmt.Fprintln(stderr, "ccon: no sessions found")
		return exitFail
	}

	now := time.Now()
	if !isTerminal(stdout) {
		printList(stdout, groups, now)
		return exitOK
	}

	entry, ok, err := ui.Pick(groups, now)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ccon: %v\n", err)
		return exitFail
	}
	if !ok {
		return exitOK // вышел, ничего не выбрав, — это не ошибка
	}

	return openChosen(ctx, entry, dump, follow, view, stdout, stderr)
}

// openChosen открывает выбранную сессию тем же способом, каким её открыл бы
// прямой запуск: --dump печатает, --dump --follow следит, без флагов идёт TUI.
// Вынесено из runList отдельно, потому что всё до него требует терминала, а
// это — нет, и проверять его надо без терминала.
func openChosen(ctx context.Context, e session.Entry, dump, follow bool, view ui.View, stdout, stderr io.Writer) int {
	target := session.TargetFor(e)

	var err error
	switch {
	case dump && follow:
		err = followSession(ctx, target, stdout, stderr)
	case dump:
		err = dumpSession(target, stdout)
	default:
		err = showTUI(ctx, e.Path, target, view)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ccon: %v\n", err)
		return exitFail
	}
	return exitOK
}

// printList печатает список текстом. Идентификатор показывается целиком:
// без терминала единственный способ воспользоваться списком — скопировать
// его в --session.
func printList(w io.Writer, groups []session.Group, now time.Time) {
	for i, g := range groups {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintln(w, g.Project)

		for _, e := range g.Entries {
			mark := " "
			if e.Newest {
				mark = "*" // самая свежая: её и выберет ccon без --session
			}

			title := e.Title
			if title == "" {
				title = "— untitled —"
			}
			_, _ = fmt.Fprintf(w, "  %s %-12s %s\n", mark, ui.HumanTime(e.Modified, now), title)
			_, _ = fmt.Fprintf(w, "    %-12s %s\n", "", e.SessionID)
			if e.Prompt != "" {
				_, _ = fmt.Fprintf(w, "    %-12s └ %s\n", "", clipRunes(e.Prompt, 96))
			}
		}
	}
}

// clipRunes режет строку по рунам: обрезка по байтам ломает кириллицу.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
