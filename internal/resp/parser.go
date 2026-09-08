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
	b, err := d.readByte()
	if err != nil {
		return 0, err
	}
	if b != marker {
		return 0, fmt.Errorf("expected %q", marker)
	}
	n, digits := 0, 0
	for {
		b, err = d.readByte()
		if err != nil {
			return 0, err
		}
		if b == '\r' {
			b, err = d.readByte()
			if err != nil {
				return 0, err
			}
			if b != '\n' || digits == 0 {
				return 0, errors.New("invalid length terminator")
			}
			return n, nil
		}
		if b < '0' || b > '9' || digits >= 20 {
			return 0, errors.New("invalid length")
		}
		digit := int(b - '0')
		if n > max/10 || n == max/10 && digit > max%10 {
			return 0, errors.New("declared length exceeds limit")
		}
		n = n*10 + digit
		digits++
	}
}
func (d *Decoder) bulk() ([]byte, error) {
	n, err := d.length('$', d.limits.MaxBulkBytes)
	if err != nil {
		return nil, err
	}
	if d.remaining < 2 || n > d.remaining-2 {
		return nil, errors.New("request exceeds byte limit")
	}
	// Grow only as bytes actually arrive, rather than allocating the declared
	// length for a client that never sends its payload.
	var out bytes.Buffer
	if _, err = io.CopyN(&out, d.reader, int64(n)); err != nil {
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
	payload := out.Bytes()
	if payload == nil {
		payload = []byte{}
	}
	return payload, nil
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
	for i := 0; i < n; i++ {
		var value []byte
		value, err = d.bulk()
		if err != nil {
			return nil, err
		}
		args = append(args, value)
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
