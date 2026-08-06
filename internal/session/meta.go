package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// metaExt — расширение файла-спутника с описанием субагента.
	metaExt = ".meta.json"

	// maxMetaBytes — описание субагента крошечное; больше читать незачем.
	maxMetaBytes = 64 << 10
)

// Meta — содержимое agent-<id>.meta.json. Состав подтверждён по живому корпусу
// буквально: agentType, description, toolUseId, spawnDepth.
type Meta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	ToolUseID   string `json:"toolUseId"`
	SpawnDepth  int    `json:"spawnDepth"`
}

// ErrNoMeta — описания ещё нет либо оно нечитаемо. Это штатная ситуация:
// .meta.json может быть не дописан, когда первые строки агента уже пошли.
var ErrNoMeta = errors.New("subagent metadata unavailable")

// MetaPath — путь к описанию рядом с транскриптом субагента.
func MetaPath(transcript string) string {
	return strings.TrimSuffix(transcript, transcriptExt) + metaExt
}

// ReadMeta читает описание субагента по пути его транскрипта.
// Любая неудача — ErrNoMeta: вызывающий обязан уметь работать без описания.
func ReadMeta(transcript string) (Meta, error) {
	f, err := os.Open(MetaPath(transcript))
	if err != nil {
		return Meta{}, fmt.Errorf("%w: %v", ErrNoMeta, err)
	}
	// Открыт на чтение: описание уже разобрано, закрытие ничего не решает.
	defer func() { _ = f.Close() }()

	var m Meta
	if err := json.NewDecoder(io.LimitReader(f, maxMetaBytes)).Decode(&m); err != nil {
		return Meta{}, fmt.Errorf("%w: %v", ErrNoMeta, err)
	}
	if m.AgentType == "" {
		return Meta{}, fmt.Errorf("%w: empty agentType", ErrNoMeta)
	}
	return m, nil
}
