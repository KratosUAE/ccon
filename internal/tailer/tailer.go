// Package tailer следит за одним растущим файлом и отдаёт из него целые
// строки. Фан-аут по многим файлам — забота вызывающего (слайс S5).
//
// Почему свой, а не библиотека: nxadm/tail зовёт os.Exit(1) из фоновой
// горутины на любой не-ENOENT сбой Stat — для TUI это мгновенная смерть без
// сообщения; hpcloud/tail мёртв, grafana/tail и go-faster/tail архивированы.
package tailer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultInterval — период опроса файла. Спека просит порядка 200 мс.
	DefaultInterval = 200 * time.Millisecond

	// DefaultMaxLine — потолок длины строки на случай, когда вызывающий его не
	// задал. Без потолка carry на файле без переводов строк растёт до OOM.
	// Живой режим ставит сюда parse.MaxLineBytes: архив и слежение обязаны
	// отбрасывать одни и те же строки, иначе цифры разойдутся необъяснимо.
	// Замеренный максимум живого корпуса — 9 993 440 байт, в файле субагента.
	DefaultMaxLine = 64 << 20

	// chunkSize — сколько байт забирается за одно чтение. Ограничение держит
	// пиковую память предсказуемой на файле, выросшем сразу на сотни мегабайт.
	chunkSize = 1 << 20

	// headSize — сколько байт начала файла запоминается, чтобы поймать
	// перезапись на месте: inode при ней тот же, а размер может совпасть.
	headSize = 512
)

// ErrLineTooLong — в файле встретилась строка длиннее допустимого лимита.
var ErrLineTooLong = errors.New("line exceeds the allowed limit")

// Option настраивает тейлер.
type Option func(*config)

type config struct {
	interval time.Duration
	maxLine  int
	caughtUp func()
}

// WithInterval задаёт период опроса.
func WithInterval(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.interval = d
		}
	}
}

// WithMaxLine задаёт потолок длины строки.
func WithMaxLine(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxLine = n
		}
	}
}

// WithCaughtUp задаёт колбэк, который зовётся один раз — когда тейлер дочитал
// файл до конца, то есть догон накопленного закончился и дальше идёт слежение.
func WithCaughtUp(f func()) Option {
	return func(c *config) { c.caughtUp = f }
}

// Tail читает файл с начала и продолжает следить за дозаписью.
//
// Наверх отдаются только строки, завершённые переводом строки: запись JSONL не
// атомарна, и опрос гарантированно попадёт в середину строки. Отсутствие файла
// не ошибка — субагент может появиться позже. Оба канала закрываются, когда
// работа завершена по отмене контекста; ошибки уходят в канал, а не в os.Exit.
func Tail(ctx context.Context, path string, opts ...Option) (<-chan []byte, <-chan error) {
	cfg := config{interval: DefaultInterval, maxLine: DefaultMaxLine}
	for _, opt := range opts {
		opt(&cfg)
	}

	lines := make(chan []byte)
	errs := make(chan error, 1)

	t := &tailer{path: path, cfg: cfg, lines: lines, errs: errs}
	go t.run(ctx)

	return lines, errs
}

type tailer struct {
	path  string
	cfg   config
	lines chan<- []byte
	errs  chan<- error

	off   int64  // единственный источник правды о прочитанном
	carry []byte // хвост незавершённой строки
	skip  bool   // пропускаем остаток слишком длинной строки

	caught  bool        // догон накопленного завершён
	prev    os.FileInfo // результат прошлого Stat: ловим подмену файла
	headSum uint64      // отпечаток начала файла: ловим перезапись на месте
	headLen int         // сколько байт вошло в отпечаток
	buf     []byte      // переиспользуемый буфер чтения
	head    [headSize]byte
}

func (t *tailer) run(ctx context.Context) {
	defer close(t.lines)
	defer close(t.errs)

	ticker := time.NewTicker(t.cfg.interval)
	defer ticker.Stop()

	for {
		// Читаем, пока файл отдаёт данные: всплеск в сотню мегабайт не должен
		// растягиваться на сотню тактов опроса.
		for {
			// Отмену проверяем здесь, а не только в send: поток из пустых строк
			// и пропускаемый хвост длинной строки send не зовут вовсе, и
			// горутина дожёвывала бы файл, когда её уже никто не ждёт.
			select {
			case <-ctx.Done():
				return
			default:
			}

			more, err := t.poll(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				t.report(err)
			}
			if !more {
				break
			}
		}

		// Дочитали всё, что было в файле: догон закончен. Отсутствующий файл
		// тоже считается дочитанным — пустой сессии нечего догонять.
		if !t.caught {
			t.caught = true
			if t.cfg.caughtUp != nil {
				t.cfg.caughtUp()
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reset возвращает тейлер к чтению файла с начала.
func (t *tailer) reset() {
	t.off, t.carry, t.skip, t.headLen, t.headSum = 0, nil, false, 0, 0
}

// poll вычитывает очередной кусок файла. Возвращает true, если данные ещё
// остались и стоит прочитать сразу же, не дожидаясь следующего такта.
func (t *tailer) poll(ctx context.Context) (bool, error) {
	fi, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Файла ещё (или уже) нет — штатная ситуация; когда появится,
			// читать его надо с начала.
			t.prev = nil
			t.reset()
			return false, nil
		}
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", t.path)
	}

	switch {
	case t.prev != nil && !os.SameFile(t.prev, fi):
		t.reset() // файл подменили по rename: inode другой
	case fi.Size() < t.off:
		t.reset() // усечение ловим по размеру, а не по времени изменения
	case fi.Size() == t.off && t.prev != nil && !fi.ModTime().Equal(t.prev.ModTime()):
		// Перезапись на месте до того же размера: по размеру её не отличить
		// от простоя, поэтому здесь верим времени изменения.
		t.reset()
	}
	t.prev = fi

	if fi.Size() == t.off {
		return false, nil // простой: файл даже не открываем
	}

	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // файл исчез между Stat и Open — нормальная гонка
		}
		return false, err
	}
	// Чтение: содержимое уже в буфере, ошибка закрытия на него не влияет.
	defer func() { _ = f.Close() }()

	// Перезапись на больший размер ни inode, ни размером не выдаёт:
	// сверяем начало файла с запомненным отпечатком.
	if t.headChanged(f, fi.Size()) {
		t.reset()
	}

	want := min(fi.Size()-t.off, chunkSize)
	if int64(cap(t.buf)) < want {
		t.buf = make([]byte, chunkSize)
	}
	buf := t.buf[:want]

	// Частичное чтение с io.EOF — норма: файл дописывается прямо сейчас.
	n, err := f.ReadAt(buf, t.off)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	t.off += int64(n)

	if err := t.consume(ctx, buf[:n]); err != nil {
		return false, err
	}
	return t.off < fi.Size(), nil
}

// headChanged сравнивает начало файла с отпечатком, снятым в прошлый раз.
// Дозапись начало файла не меняет, перезапись — почти всегда меняет.
func (t *tailer) headChanged(f *os.File, size int64) bool {
	n := min(size, int64(headSize))
	got, err := f.ReadAt(t.head[:n], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false // сверить не вышло — выдумывать перезапись не будем
	}

	changed := t.headLen > 0 &&
		(got < t.headLen || sum64(t.head[:t.headLen]) != t.headSum)

	t.headSum, t.headLen = sum64(t.head[:got]), got
	return changed
}

func sum64(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

// consume разбирает прочитанный кусок на целые строки.
func (t *tailer) consume(ctx context.Context, chunk []byte) error {
	for len(chunk) > 0 {
		if t.skip {
			// Дочитываем и выбрасываем остаток слишком длинной строки.
			i := bytes.IndexByte(chunk, '\n')
			if i < 0 {
				return nil
			}
			chunk = chunk[i+1:]
			t.skip = false
			continue
		}

		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			t.carry = append(t.carry, chunk...)
			t.checkCarry()
			return nil
		}

		line := chunk[:i]
		chunk = chunk[i+1:]

		if len(t.carry) > 0 {
			t.carry = append(t.carry, line...)
			line = t.carry
		}
		out := bytes.TrimRight(line, "\r")
		t.carry = nil

		// Лимит проверяется и здесь: слишком длинная строка может целиком
		// уместиться в один кусок чтения и до carry не дойти вовсе.
		if len(out) > t.cfg.maxLine {
			t.reportTooLong()
			continue
		}
		if len(bytes.TrimSpace(out)) == 0 {
			continue // пустые строки парсер всё равно отбрасывает
		}
		if err := t.send(ctx, out); err != nil {
			return err
		}
	}
	return nil
}

// checkCarry следит, чтобы незавершённая строка не росла бесконечно.
func (t *tailer) checkCarry() {
	if len(t.carry) <= t.cfg.maxLine {
		return
	}
	t.carry, t.skip = nil, true
	t.reportTooLong()
}

func (t *tailer) reportTooLong() {
	t.report(fmt.Errorf("%s: %w (%d bytes)",
		filepath.Base(t.path), ErrLineTooLong, t.cfg.maxLine))
}

// send отдаёт строку получателю. Копия обязательна: буфер чтения переиспользуется.
func (t *tailer) send(ctx context.Context, line []byte) error {
	out := make([]byte, len(line))
	copy(out, line)

	select {
	case t.lines <- out:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// report доставляет ошибку, не блокируя тейлер, если её не читают:
// продолжать читать файл важнее, чем дождаться получателя ошибки.
func (t *tailer) report(err error) {
	select {
	case t.errs <- err:
	default:
	}
}
