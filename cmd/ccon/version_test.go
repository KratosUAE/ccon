package main

import (
	"runtime"
	"strings"
	"testing"
)

// Вшитое при сборке значение главнее всего: релизный бинарь обязан назвать
// свой номер, а не ревизию.
func TestVersionStringFromLdflags(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	version = "v1.0.0"
	got := versionString()

	if !strings.HasPrefix(got, "ccon v1.0.0") {
		t.Errorf("версия %q не начинается с номера", got)
	}
	if strings.Contains(got, "dev") {
		t.Errorf("релизная сборка представилась как dev: %q", got)
	}
	if !strings.Contains(got, runtime.Version()) {
		t.Errorf("нет версии Go: %q", got)
	}
}

// Без вшитого значения версия собирается из данных сборки. В go test данные
// VCS отсутствуют, поэтому проверяется устойчивость, а не конкретный текст:
// подгонять тест под окружение бессмысленно, в бинаре оно другое.
func TestVersionStringFallback(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	version = ""
	got := versionString()

	if !strings.HasPrefix(got, "ccon dev") {
		t.Errorf("версия %q не помечена как dev", got)
	}
	if !strings.Contains(got, runtime.Version()) {
		t.Errorf("нет версии Go: %q", got)
	}
	if !strings.Contains(got, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("нет платформы: %q", got)
	}
	if strings.Contains(got, "  ") || strings.HasSuffix(got, "·") {
		t.Errorf("строка собрана криво: %q", got)
	}
}

// --version печатает версию и выходит, не трогая ни сессию, ни транскрипт:
// это первое, что запускает человек, у которого что-то не работает.
func TestRunVersionFlag(t *testing.T) {
	// Ни домашнего каталога, ни хранилища транскриптов может не быть вовсе.
	t.Setenv("HOME", "")

	code, out, errOut := runArgs(t, "--version")
	if code != 0 {
		t.Fatalf("код возврата %d, stderr: %s", code, errOut)
	}
	if !strings.HasPrefix(out, "ccon ") {
		t.Errorf("stdout=%q", out)
	}
	if strings.TrimSpace(errOut) != "" {
		t.Errorf("stderr не пуст: %q", errOut)
	}
}

// Остальные флаги при --version игнорируются, в том числе заведомо битые.
func TestRunVersionIgnoresRest(t *testing.T) {
	t.Setenv("HOME", "")

	for _, args := range [][]string{
		{"--version", "--dump"},
		{"--dump", "--version", "нет-такого-файла.jsonl"},
		{"--version", "--project", "/нет/каталога"},
		{"--version", "--follow"},
	} {
		code, out, errOut := runArgs(t, args...)
		if code != 0 {
			t.Errorf("%v: код возврата %d, stderr: %s", args, code, errOut)
		}
		if !strings.HasPrefix(out, "ccon ") {
			t.Errorf("%v: stdout=%q", args, out)
		}
	}
}
