// Package persistence implements versioned, checksummed logical mutation frames.
package persistence

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const magic = "MCLOG001"
const MaxFrameBytes = 128 << 20
const plainSetFrameTag byte = 0
const plainSetBatchFrameTag byte = 1

type ReplicationCheckpoint struct {
	MasterHost  string `json:"master_host,omitempty"`
	MasterPort  int    `json:"master_port,omitempty"`
	MasterRunID string `json:"master_run_id,omitempty"`
	Offset      int64  `json:"offset,omitempty"`
	RedisStream bool   `json:"redis_stream,omitempty"`
	Clear       bool   `json:"clear,omitempty"`
}

// Key is bytes rather than string so JSON does not normalize invalid UTF-8 keys.
type Record struct {
	Reset       bool                   `json:"reset,omitempty"`
	Key         []byte                 `json:"key"`
	Value       []byte                 `json:"value,omitempty"`
	ExpiresAtMS int64                  `json:"expires_at_ms,omitempty"`
	Deleted     bool                   `json:"deleted,omitempty"`
	ValueType   uint8                  `json:"value_type,omitempty"`
	Replication *ReplicationCheckpoint `json:"replication,omitempty"`
}

// RecoverReplicationCheckpoint advances crash-recovery replication metadata in
// the same order as logical persistence frames. Only an explicit replication
// clear marker invalidates an earlier continuation tuple; ordinary keyspace
// reset records are also used by AOF compaction and do not imply a topology
// change.
func RecoverReplicationCheckpoint(current *ReplicationCheckpoint, records []Record) *ReplicationCheckpoint {
	for _, record := range records {
		if record.Replication == nil {
			continue
		}
		if record.Replication.Clear {
			current = &ReplicationCheckpoint{Clear: true}
			continue
		}
		checkpoint := *record.Replication
		current = &checkpoint
	}
	return current
}
type Log struct {
	lock        *os.File
	mu          sync.Mutex
	file        *os.File
	writer      *bufio.Writer
	policy      string
	failed      error
	appendedSeq uint64
	syncedSeq   uint64
	syncChanged chan struct{}
	stop        chan struct{}
	done        chan struct{}
	once        sync.Once
}

func Open(path, policy string) (*Log, error) {
	if !validFsyncPolicy(policy) {
		return nil, errors.New("invalid fsync policy")
	}
	lock, err := lockFile(path)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			lock.Close()
		}
	}()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.Size() == 0 {
		if err = writeAll(f, []byte(magic)); err != nil {
			f.Close()
			return nil, err
		}
	} else {
		offset, err := Read(f, func([]Record) error { return nil })
		if err != nil {
			f.Close()
			return nil, err
		}
		if err = f.Truncate(offset); err != nil {
			f.Close()
			return nil, err
		}
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	success = true
	l := &Log{
		lock:        lock,
		file:        f,
		writer:      bufio.NewWriterSize(f, 256<<10),
		policy:      policy,
		syncChanged: make(chan struct{}),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}

	// Keep one lightweight sync loop alive for the lifetime of the log.
	// It only performs fsync work while the current runtime policy is
	// "everysec". This allows CONFIG SET appendfsync to switch policies
	// without creating/stopping goroutines or replacing channels.
	go l.syncLoop()

	return l, nil
}
func (l *Log) syncLoop() {
	defer close(l.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.mu.Lock()

			if l.failed == nil &&
				l.policy == "everysec" {

				if err := l.writer.Flush(); err != nil {
					l.failed = err
				} else {
					l.failed = l.file.Sync()
				}
				if l.failed == nil {
					l.markSyncedLocked()
				}
			}

			l.mu.Unlock()
		}
	}
}

func validFsyncPolicy(policy string) bool {
	return policy == "always" ||
		policy == "everysec" ||
		policy == "no"
}

// Policy returns the current runtime fsync policy.
func (l *Log) Policy() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.policy
}

func (l *Log) markSyncedLocked() {
	if l.syncedSeq >= l.appendedSeq {
		return
	}
	l.syncedSeq = l.appendedSeq
	close(l.syncChanged)
	l.syncChanged = make(chan struct{})
}

// DurabilitySnapshot reports the latest successful append sequence, the latest
// sequence known to have crossed a real fsync boundary, and a channel closed
// whenever synced progress advances.
func (l *Log) DurabilitySnapshot() (appended, synced uint64, changed <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendedSeq, l.syncedSeq, l.syncChanged
}

// SetPolicy changes the runtime AOF fsync policy.
//
// Switching to "always" performs a sync before publishing the new policy,
// so once CONFIG SET returns OK the file is already durable through all
// writes completed before the policy transition.
func (l *Log) SetPolicy(policy string) error {
	if !validFsyncPolicy(policy) {
		return errors.New("invalid fsync policy")
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.failed != nil {
		return l.failed
	}

	if policy == l.policy {
		return nil
	}

	if policy == "always" {
		if err := l.writer.Flush(); err != nil {
			l.failed = err
			return err
		}
		if err := l.file.Sync(); err != nil {
			l.failed = err
			return err
		}
		l.markSyncedLocked()
	}

	l.policy = policy

	return nil
}

func (l *Log) finishAppendLocked() error {
	l.appendedSeq++
	if l.policy == "no" {
		if err := l.writer.Flush(); err != nil {
			l.failed = err
			return err
		}
	}
	if l.policy == "always" {
		if err := l.writer.Flush(); err != nil {
			l.failed = err
			return err
		}
		l.failed = l.file.Sync()
		if l.failed == nil {
			l.markSyncedLocked()
		}
	}
	return l.failed
}

func (l *Log) Append(records []Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil {
		return l.failed
	}
	if err := WriteFrame(l.writer, records); err != nil {
		l.failed = err
		return err
	}
	return l.finishAppendLocked()
}

// AppendPlainSet writes the common SET key value mutation without JSON/base64
// encoding. The payload starts with a reserved binary tag so Read can replay
// these frames alongside older JSON frames in the same AOF.
func (l *Log) AppendPlainSet(key, value []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil {
		return l.failed
	}
	if err := WritePlainSetFrame(l.writer, key, value); err != nil {
		l.failed = err
		return err
	}
	return l.finishAppendLocked()
}


func (l *Log) AppendPlainSetBatch(keys, values [][]byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil {
		return l.failed
	}
	if err := WritePlainSetBatchFrame(l.writer, keys, values); err != nil {
		l.failed = err
		return err
	}
	l.appendedSeq += uint64(len(keys))
	if l.policy == "no" {
		if err := l.writer.Flush(); err != nil {
			l.failed = err
			return err
		}
	}
	if l.policy == "always" {
		if err := l.writer.Flush(); err != nil {
			l.failed = err
			return err
		}
		l.failed = l.file.Sync()
		if l.failed == nil {
			l.markSyncedLocked()
		}
	}
	return l.failed
}
func (l *Log) Close() error {
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.failed == nil {
			if err := l.writer.Flush(); err != nil {
				l.failed = err
			} else {
				l.failed = l.file.Sync()
			}
			if l.failed == nil {
				l.markSyncedLocked()
			}
		}
		defer l.lock.Close()
		if err := l.file.Close(); l.failed == nil {
			l.failed = err
		}
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failed
}
func WriteFrame(w io.Writer, records []Record) error {
	payload, err := json.Marshal(records)
	if err != nil {
		return err
	}
	if len(payload) > MaxFrameBytes {
		return errors.New("persistence frame exceeds limit")
	}
	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[4:], crc32.ChecksumIEEE(payload))
	if err = writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func WritePlainSetFrame(w io.Writer, key, value []byte) error {
	payloadLen := 5 + len(key) + len(value)
	if payloadLen > MaxFrameBytes {
		return errors.New("persistence frame exceeds limit")
	}
	var prefix [5]byte
	prefix[0] = plainSetFrameTag
	binary.LittleEndian.PutUint32(prefix[1:], uint32(len(key)))

	checksum := crc32.Update(0, crc32.IEEETable, prefix[:])
	checksum = crc32.Update(checksum, crc32.IEEETable, key)
	checksum = crc32.Update(checksum, crc32.IEEETable, value)

	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(payloadLen))
	binary.LittleEndian.PutUint32(header[4:], checksum)
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	if err := writeAll(w, prefix[:]); err != nil {
		return err
	}
	if err := writeAll(w, key); err != nil {
		return err
	}
	return writeAll(w, value)
}


func WritePlainSetBatchFrame(w io.Writer, keys, values [][]byte) error {
	if len(keys) == 0 || len(keys) != len(values) {
		return errors.New("invalid plain SET batch")
	}
	payloadLen := 5
	for i := range keys {
		payloadLen += 8 + len(keys[i]) + len(values[i])
	}
	if payloadLen > MaxFrameBytes {
		return errors.New("persistence frame exceeds limit")
	}

	payload := make([]byte, 5, payloadLen)
	payload[0] = plainSetBatchFrameTag
	binary.LittleEndian.PutUint32(payload[1:5], uint32(len(keys)))
	for i := range keys {
		start := len(payload)
		payload = append(payload, make([]byte, 8)...)
		binary.LittleEndian.PutUint32(payload[start:start+4], uint32(len(keys[i])))
		binary.LittleEndian.PutUint32(payload[start+4:start+8], uint32(len(values[i])))
		payload = append(payload, keys[i]...)
		payload = append(payload, values[i]...)
	}

	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[4:], crc32.ChecksumIEEE(payload))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// Read returns the last complete frame offset. A truncated final frame is
// ignored; checksum failures, unknown versions and malformed data are fatal.
func Read(r io.Reader, apply func([]Record) error) (int64, error) {
	header := make([]byte, len(magic))
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, err
	}
	if string(header) != magic {
		return 0, errors.New("unknown persistence format")
	}
	offset := int64(len(magic))
	for {
		var h [8]byte
		_, err := io.ReadFull(r, h[:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return offset, nil
		}
		if err != nil {
			return offset, err
		}
		length := binary.LittleEndian.Uint32(h[:4])
		if length > MaxFrameBytes {
			return offset, errors.New("persistence frame exceeds limit")
		}
		var payload bytes.Buffer
		if _, err = io.CopyN(&payload, r, int64(length)); err == io.EOF || err == io.ErrUnexpectedEOF {
			return offset, nil
		} else if err != nil {
			return offset, err
		}
		if crc32.ChecksumIEEE(payload.Bytes()) != binary.LittleEndian.Uint32(h[4:]) {
			return offset, errors.New("persistence checksum mismatch")
		}
		var records []Record
		payloadBytes := payload.Bytes()
		if len(payloadBytes) > 0 && payloadBytes[0] == plainSetFrameTag {
			if len(payloadBytes) < 5 {
				return offset, errors.New("invalid plain SET persistence frame")
			}
			keyLen := int(binary.LittleEndian.Uint32(payloadBytes[1:5]))
			if keyLen > len(payloadBytes)-5 {
				return offset, errors.New("invalid plain SET persistence frame")
			}
			records = []Record{{
				Key:   append([]byte(nil), payloadBytes[5:5+keyLen]...),
				Value: append([]byte(nil), payloadBytes[5+keyLen:]...),
			}}
		} else if len(payloadBytes) > 0 && payloadBytes[0] == plainSetBatchFrameTag {
			if len(payloadBytes) < 5 {
				return offset, errors.New("invalid plain SET batch persistence frame")
			}
			count := int(binary.LittleEndian.Uint32(payloadBytes[1:5]))
			pos := 5
			records = make([]Record, 0, count)
			for i := 0; i < count; i++ {
				if pos+8 > len(payloadBytes) {
					return offset, errors.New("invalid plain SET batch persistence frame")
				}
				keyLen := int(binary.LittleEndian.Uint32(payloadBytes[pos : pos+4]))
				valueLen := int(binary.LittleEndian.Uint32(payloadBytes[pos+4 : pos+8]))
				pos += 8
				if keyLen < 0 || valueLen < 0 || pos+keyLen+valueLen > len(payloadBytes) {
					return offset, errors.New("invalid plain SET batch persistence frame")
				}
				records = append(records, Record{
					Key:   append([]byte(nil), payloadBytes[pos:pos+keyLen]...),
					Value: append([]byte(nil), payloadBytes[pos+keyLen:pos+keyLen+valueLen]...),
				})
				pos += keyLen + valueLen
			}
			if pos != len(payloadBytes) {
				return offset, errors.New("trailing plain SET batch persistence data")
			}
		} else {
			d := json.NewDecoder(bytes.NewReader(payloadBytes))
			d.DisallowUnknownFields()
			if err = d.Decode(&records); err != nil {
				return offset, err
			}
			var extra interface{}
			if d.Decode(&extra) != io.EOF {
				return offset, errors.New("trailing frame data")
			}
		}
		if err = apply(records); err != nil {
			return offset, err
		}
		offset += 8 + int64(length)
	}
}
func Replay(path string, apply func([]Record) error) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = Read(f, apply)
	return err
}
func Snapshot(path string, records []Record) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".morph-snapshot-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = writeAll(f, []byte(magic)); err == nil {
		for _, record := range records {
			if err = WriteFrame(f, []Record{record}); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = WriteFrame(f, []Record{})
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return fmt.Errorf("sync snapshot directory: %w", err)
	}
	return nil
}

// ReplaySnapshot requires a complete footer and rejects all truncation.
func ReplaySnapshot(path string, apply func([]Record) error) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	complete := false
	offset, err := Read(f, func(records []Record) error {
		if complete {
			return errors.New("data follows snapshot footer")
		}
		if len(records) == 0 {
			complete = true
			return nil
		}
		return apply(records)
	})
	if err != nil {
		return err
	}
	if !complete || offset != st.Size() {
		return errors.New("truncated snapshot")
	}
	return nil
}

// Rewrite atomically replaces history with a reset marker and current logical
// records. Reset makes replay safe even over a snapshot from an older keyspace.
func (l *Log) Rewrite(records []Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil {
		return l.failed
	}
	path := l.file.Name()
	full := make([]Record, 0, len(records)+1)
	full = append(full, Record{Reset: true})
	full = append(full, records...)
	if err := Snapshot(path, full); err != nil {
		l.failed = err
		return err
	}
	next, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		l.failed = err
		return err
	}
	previous := l.file
	l.file = next
	l.writer.Reset(next)
	if err = previous.Close(); err != nil {
		l.failed = err
	}
	return l.failed
}
