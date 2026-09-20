package resp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Limits bound a single command, including all framing bytes.
type Limits struct{ MaxRequestBytes, MaxBulkBytes, MaxArguments int }

func DefaultLimits() Limits { return Limits{64 << 20, 32 << 20, 1024} }
func (l Limits) Validate() error {
	if l.MaxRequestBytes < 4 || l.MaxBulkBytes < 0 || l.MaxBulkBytes > l.MaxRequestBytes || l.MaxArguments < 1 {
		return errors.New("invalid RESP limits")
	}
	return nil
}

// Decoder consumes one flat array of bulk strings at a time. It does not own
// the reader; callers retain it across requests to preserve pipelined bytes.
type Decoder struct {
	reader    *bufio.Reader
	limits    Limits
	remaining int
}

func NewDecoder(reader *bufio.Reader, limits Limits) (*Decoder, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &Decoder{reader: reader, limits: limits}, nil
}

func (d *Decoder) readByte() (byte, error) {
	if d.remaining == 0 {
		return 0, errors.New("request exceeds byte limit")
	}
	b, err := d.reader.ReadByte()
	if err == nil {
		d.remaining--
	}
	return b, err
}
func (d *Decoder) length(marker byte, max int) (int, error) {
	line, err := d.reader.ReadSlice('\n')
	if err != nil {
		return 0, err
	}
	if len(line) > d.remaining {
		return 0, errors.New("request exceeds byte limit")
	}
	d.remaining -= len(line)

	if len(line) < 4 || line[0] != marker {
		if len(line) > 0 && line[0] != marker {
			return 0, fmt.Errorf("expected %q", marker)
		}
		return 0, errors.New("invalid length terminator")
	}
	if line[len(line)-2] != '\r' {
		return 0, errors.New("invalid length terminator")
	}

	digits := line[1 : len(line)-2]
	if len(digits) == 0 {
		return 0, errors.New("invalid length terminator")
	}
	if len(digits) > 20 {
		return 0, errors.New("invalid length")
	}

	n := 0
	for _, b := range digits {
		if b < '0' || b > '9' {
			return 0, errors.New("invalid length")
		}
		digit := int(b - '0')
		if n > max/10 || n == max/10 && digit > max%10 {
			return 0, errors.New("declared length exceeds limit")
		}
		n = n*10 + digit
	}
	return n, nil
}
func (d *Decoder) bulk() ([]byte, error) {
	n, err := d.length('$', d.limits.MaxBulkBytes)
	if err != nil {
		return nil, err
	}
	if d.remaining < 2 || n > d.remaining-2 {
		return nil, errors.New("request exceeds byte limit")
	}
	// The declared bulk length has already been validated against the
	// per-request and per-bulk limits. Allocate the payload once and read
	// directly into it instead of growing a bytes.Buffer incrementally.
	payload := make([]byte, n)
	if _, err = io.ReadFull(d.reader, payload); err != nil {
		return nil, err
	}
	d.remaining -= n
	a, err := d.readByte()
	if err != nil {
		return nil, err
	}
	b, err := d.readByte()
	if err != nil {
		return nil, err
	}
	if a != '\r' || b != '\n' {
		return nil, errors.New("invalid bulk terminator")
	}
	return payload, nil
}

// ReadBufferedGET copies a complete two-argument GET key from the current
// bufio.Reader buffer into caller-owned scratch. It only activates when the
// full frame is already buffered; otherwise it consumes nothing and ReadCommand
// remains the fallback.
//
// Reusing scratch makes steady-state pipelined GET decoding allocation-free
// without exposing bufio.Reader-owned memory past Discard.
func (d *Decoder) ReadBufferedGET(scratch []byte) (key []byte, ok bool, err error) {
	buffered := d.reader.Buffered()
	if buffered < len("*2\r\n$3\r\nGET\r\n$0\r\n\r\n") {
		return scratch[:0], false, nil
	}
	if buffered > d.limits.MaxRequestBytes {
		buffered = d.limits.MaxRequestBytes
	}
	buf, err := d.reader.Peek(buffered)
	if err != nil {
		return scratch[:0], false, nil
	}

	if len(buf) < 17 ||
		buf[0] != '*' || buf[1] != '2' || buf[2] != '\r' || buf[3] != '\n' ||
		buf[4] != '$' || buf[5] != '3' || buf[6] != '\r' || buf[7] != '\n' ||
		!((buf[8] == 'G' || buf[8] == 'g') &&
			(buf[9] == 'E' || buf[9] == 'e') &&
			(buf[10] == 'T' || buf[10] == 't')) ||
		buf[11] != '\r' || buf[12] != '\n' || buf[13] != '$' {
		return scratch[:0], false, nil
	}

	i := 14
	keyLen := 0
	digits := 0
	for i < len(buf) {
		b := buf[i]
		if b == '\r' {
			if digits == 0 || i+1 >= len(buf) || buf[i+1] != '\n' {
				return scratch[:0], false, nil
			}
			i += 2
			break
		}
		if b < '0' || b > '9' || digits >= 20 {
			return scratch[:0], false, nil
		}
		digit := int(b - '0')
		if keyLen > d.limits.MaxBulkBytes/10 ||
			keyLen == d.limits.MaxBulkBytes/10 && digit > d.limits.MaxBulkBytes%10 {
			return scratch[:0], false, nil
		}
		keyLen = keyLen*10 + digit
		digits++
		i++
	}
	if digits == 0 || i > len(buf) {
		return scratch[:0], false, nil
	}

	frameLen := i + keyLen + 2
	if frameLen > len(buf) || frameLen > d.limits.MaxRequestBytes {
		return scratch[:0], false, nil
	}
	if buf[i+keyLen] != '\r' || buf[i+keyLen+1] != '\n' {
		return scratch[:0], false, nil
	}
	if d.limits.MaxArguments < 2 {
		return scratch[:0], false, nil
	}

	if cap(scratch) < keyLen {
		scratch = make([]byte, keyLen)
	} else {
		scratch = scratch[:keyLen]
	}
	copy(scratch, buf[i:i+keyLen])
	if _, err := d.reader.Discard(frameLen); err != nil {
		return scratch[:0], false, err
	}
	return scratch, true, nil
}

// ReadCommand accepts only nonempty, flat arrays of non-null bulk strings.
// io.EOF means clean end of stream; truncated requests return io.ErrUnexpectedEOF.
// After any other error the stream must be closed, not resynchronized.
func (d *Decoder) ReadCommand() (args [][]byte, err error) {
	d.remaining = d.limits.MaxRequestBytes
	if _, err = d.reader.Peek(1); err != nil {
		return nil, err
	}
	defer func() {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
	}()
	n, err := d.length('*', d.limits.MaxArguments)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("empty command array")
	}
	// Even an empty bulk needs six framing bytes.
	if n > d.remaining/6 {
		return nil, errors.New("request exceeds byte limit")
	}
	args = make([][]byte, n)
	for i := 0; i < n; i++ {
		args[i], err = d.bulk()
		if err != nil {
			return nil, err
		}
	}
	return args, nil
}

// Parse accepts a complete command or a standalone bulk string for callers
// parsing values. TCP command decoding always requires an array.
func Parse(data []byte) ([][]byte, error) {
	limits := DefaultLimits()
	if len(data) > limits.MaxRequestBytes {
		return nil, errors.New("request exceeds byte limit")
	}
	reader := bufio.NewReader(bytes.NewReader(data))
	d, _ := NewDecoder(reader, limits)
	var args [][]byte
	var err error
	if len(data) > 0 && data[0] == '$' {
		d.remaining = limits.MaxRequestBytes
		var value []byte
		value, err = d.bulk()
		args = [][]byte{value}
	} else {
		args, err = d.ReadCommand()
	}
	if err != nil {
		return nil, err
	}
	if _, err = reader.Peek(1); err != io.EOF {
		return nil, errors.New("trailing bytes")
	}
	return args, nil
}
