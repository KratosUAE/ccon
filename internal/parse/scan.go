package parse

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

const (
	// readBuf — размер буфера чтения. Строка, которая в него не влезла,
	// дочитывается кусками и собирается в отдельный накопитель.
	readBuf = 256 << 10

	// MaxLineBytes — потолок длины одной строки. Он существует не ради живых
	// транскриптов, а против враждебного или нерегулярного входа: файл без
	// единого перевода строки иначе раздувает память до out of memory.
	//
	// Замеренный максимум живого корпуса — 9 993 440 байт, и лежит он в файле
	// субагента (agent-<id>.jsonl), а не в главном транскрипте.
	// Замер по одним лишь верхнеуровневым файлам даёт 872 753 байта — в
	// одиннадцать раз меньше и ровно мимо того класса файлов, ради которого
	// инструмент писался. Запас к реальному максимуму — около 6.7x.
	MaxLineBytes = 64 << 20
)

// Scan читает r построчно и зовёт fn на каждой непустой строке.
//
// Строка длиннее MaxLineBytes не роняет разбор: она пропускается целиком,
// учитывается в возвращаемом счётчике, а чтение продолжается со следующей.
// Потерять из-за одной аномальной строки весь файл — цена несопоставимо выше,
// чем потерять саму строку. Возврат ошибки из fn останавливает чтение.
//
// Срез, отданный в fn, живёт до следующей строки: нужен дольше — копируйте.
func Scan(r io.Reader, fn func(line []byte) error) (skipped int, err error) {
	return scanLimit(r, MaxLineBytes, fn)
}

func scanLimit(r io.Reader, limit int, fn func(line []byte) error) (int, error) {
	br := bufio.NewReaderSize(r, min(readBuf, limit))

	skipped := 0
	for {
		line, tooLong, err := readLine(br, limit)
		if errors.Is(err, io.EOF) {
			return skipped, nil
		}
		if err != nil {
			return skipped, err
		}
		if tooLong {
			skipped++
			continue
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return skipped, err
		}
	}
}

// readLine отдаёт очередную строку без завершающего перевода.
//
// tooLong == true означает, что строка переросла лимит: её содержимое
// выброшено, а файл доведён до следующего перевода строки — так лимит
// ограничивает пиковую память, не обрывая разбор. io.EOF возвращается один
// раз, когда данных больше нет.
func readLine(br *bufio.Reader, limit int) ([]byte, bool, error) {
	var (
		acc     []byte // накопитель; нужен только строкам длиннее буфера чтения
		tooLong bool
	)

	for {
		chunk, err := br.ReadSlice('\n')
		partial := errors.Is(err, bufio.ErrBufferFull)
		if err != nil && !partial && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		// Перевод строки в длину не входит: лимит меряет содержимое.
		if !partial && len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			chunk = chunk[:len(chunk)-1]
		}

		switch {
		case tooLong:
			// Содержимое уже выброшено — дочитываем хвост до перевода строки.
		case !partial && acc == nil:
			// Быстрый путь: строка целиком лежит в буфере чтения, копия не нужна.
			if len(chunk) > limit {
				return nil, true, nil
			}
			if len(chunk) == 0 && errors.Is(err, io.EOF) {
				return nil, false, io.EOF
			}
			return chunk, false, nil
		case len(acc)+len(chunk) > limit:
			acc, tooLong = nil, true
		default:
			acc = append(acc, chunk...)
		}

		if !partial {
			return acc, tooLong, nil
		}
	}
}
