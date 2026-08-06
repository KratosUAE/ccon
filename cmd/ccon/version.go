package main

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// version подставляется при сборке релиза:
//
//	go build -ldflags "-X main.version=v1.0.0" ./cmd/ccon
//
// Пусто при обычной сборке — тогда версия собирается из данных VCS, вшитых
// компилятором. Смысл двухуровневой схемы в честности: релизный бинарь
// называет номер, а собранный наспех из грязного дерева говорит ревизию и
// признаётся, что дерево грязное, вместо того чтобы врать номером.
var version string

// versionString — как программа представляется человеку.
func versionString() string {
	head := "ccon " + version
	if version == "" {
		head = "ccon dev" + buildStamp()
	}
	return head + " · " + runtime.Version() + " · " + runtime.GOOS + "/" + runtime.GOARCH
}

// buildStamp достаёт ревизию, время коммита и признак незакоммиченных правок.
// Данных VCS может не быть вовсе (сборка вне репозитория, go test) — тогда
// пометка пуста, и это не ошибка.
func buildStamp() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	var revision, committed string
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			committed = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	parts := make([]string, 0, 3)
	if revision != "" {
		if len(revision) > shortRevisionLen {
			revision = revision[:shortRevisionLen]
		}
		parts = append(parts, revision)
	}
	if committed != "" {
		parts = append(parts, committed)
	}
	if dirty {
		parts = append(parts, "dirty tree")
	}

	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// shortRevisionLen — сколько знаков хеша показывать.
const shortRevisionLen = 7
