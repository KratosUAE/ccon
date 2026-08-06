package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KratosUAE/ccon/internal/session"
	"github.com/KratosUAE/ccon/internal/ui"
)

var listNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// fakeStore кладёт транскрипты в подменённый HOME и возвращает его.
func fakeStore(t *testing.T, files map[string]string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	for rel, body := range files {
		path := filepath.Join(home, ".claude", "projects", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestPrintListShowsDescription(t *testing.T) {
	groups := []session.Group{{Project: "альфа", Entries: []session.Entry{
		{SessionID: "aaa-111", Title: "разбор логов", Prompt: "продолжаем",
			Modified: listNow.Add(-time.Hour), Newest: true},
		{SessionID: "bbb-222", Modified: listNow.Add(-40 * time.Hour), Size: 4096},
	}}}

	var buf bytes.Buffer
	printList(&buf, groups, listNow)
	out := buf.String()

	for _, want := range []string{"альфа", "разбор логов", "aaa-111", "продолжаем", "11:00"} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет %q:\n%s", want, out)
		}
	}
	// Сессия без заголовка обязана сказать об этом, а не остаться безымянной.
	if !strings.Contains(out, "untitled") {
		t.Errorf("нет пометки об отсутствующем заголовке:\n%s", out)
	}
	// Самая свежая помечена: именно её откроет ccon без --session.
	if !strings.Contains(out, "* ") {
		t.Errorf("самая свежая сессия не помечена:\n%s", out)
	}
}

// Без терминала --list печатает список текстом и не пытается рисовать TUI.
func TestRunListPrintsWithoutTerminal(t *testing.T) {
	fakeStore(t, map[string]string{
		filepath.Join("-home-u-alpha", "aaa11111-2222-3333.jsonl"): `{"type":"user","cwd":"/home/u/alpha"}` + "\n" +
			`{"type":"ai-title","aiTitle":"тема альфы"}` + "\n" +
			`{"type":"last-prompt","lastPrompt":"дальше"}` + "\n",
	})

	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"--list"}, &out, &errBuf); code != exitOK {
		t.Fatalf("код возврата %d, ожидался %d (stderr: %s)", code, exitOK, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "тема альфы") || !strings.Contains(got, "aaa11111-2222-3333") {
		t.Errorf("список не напечатан:\n%s", got)
	}
}

// Пустое хранилище — отказ с объяснением, а не пустой вывод и код 0.
func TestRunListEmptyStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"--list"}, &out, &errBuf); code != exitFail {
		t.Fatalf("код возврата %d, ожидался %d", code, exitFail)
	}
	if !strings.Contains(errBuf.String(), "no sessions found") {
		t.Errorf("нет объяснения:\n%s", errBuf.String())
	}
}

// --list сам выбирает сессию: указывать её ещё и флагом бессмысленно, и
// молча игнорировать один из двух приказов нельзя.
func TestRunListConflicts(t *testing.T) {
	fakeStore(t, map[string]string{
		filepath.Join("-home-u-alpha", "aaa11111-2222-3333.jsonl"): `{"type":"user","cwd":"/home/u/alpha"}` + "\n",
	})

	cases := [][]string{
		{"--list", "--session", "aaa11111-2222-3333"},
		{"--list", "какой-то.jsonl"},
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := run(context.Background(), args, &out, &errBuf); code != exitUsage {
			t.Errorf("%v: код %d, ожидался %d", args, code, exitUsage)
		}
	}
}

// Выбранная сессия обязана открываться ровно так же, как открылась бы при
// прямом запуске: --dump печатает её события и сводку расхода.
func TestOpenChosenDumps(t *testing.T) {
	home := fakeStore(t, map[string]string{
		filepath.Join("-home-u-alpha", "aaa11111-2222-3333.jsonl"): `{"type":"user","cwd":"/home/u/alpha"}` + "\n" +
			`{"type":"assistant","timestamp":"2026-08-05T09:00:01Z","message":{"id":"m1","model":"claude-opus-5","content":[{"type":"text","text":"поехали"}],"usage":{"input_tokens":10,"output_tokens":20}}}` + "\n",
	})
	path := filepath.Join(home, ".claude", "projects", "-home-u-alpha", "aaa11111-2222-3333.jsonl")

	var out, errBuf bytes.Buffer
	code := openChosen(context.Background(),
		session.Entry{Path: path, SessionID: "aaa11111-2222-3333", Slug: "-home-u-alpha"},
		true, false, ui.ViewTranscript, &out, &errBuf)

	if code != exitOK {
		t.Fatalf("код возврата %d (stderr: %s)", code, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "поехали") {
		t.Errorf("событие не напечатано:\n%s", got)
	}
	if !strings.Contains(got, "TOTAL") {
		t.Errorf("нет сводки расхода:\n%s", got)
	}
}

// Нечитаемая цель — отказ с кодом ошибки, а не тихий успех.
func TestOpenChosenReportsFailure(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := openChosen(context.Background(),
		session.Entry{Path: filepath.Join(t.TempDir(), "нет.jsonl")},
		true, false, ui.ViewTranscript, &out, &errBuf)

	if code != exitFail {
		t.Fatalf("код возврата %d, ожидался %d", code, exitFail)
	}
	if errBuf.Len() == 0 {
		t.Error("отказ без объяснения")
	}
}

func TestClipRunes(t *testing.T) {
	// Обрезка по байтам порвала бы кириллицу посреди символа.
	if got := clipRunes("абвгде", 4); got != "абв…" {
		t.Errorf("получено %q, ожидалось «абв…»", got)
	}
	if got := clipRunes("коротко", 20); got != "коротко" {
		t.Errorf("короткая строка изменена: %q", got)
	}
}
