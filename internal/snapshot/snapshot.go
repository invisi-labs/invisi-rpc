// Package snapshot streams portable, deployment-bound index snapshots.
package snapshot

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/storage"
)

const (
	FormatVersion                     = 1
	SourceProfile                     = "invisi-protocol-v1-broadcaster-events"
	ABIHash                           = "62b33150b2f14f253080c10c48bc15399ba1fbf33f92e32ee0995b7b18a86c1f"
	DefaultMaxDecompressedBytes int64 = 8 << 30
	MaxRecordBytes                    = 64 << 20
)

// Header identifies the source contract and deployment. Checkpoint is populated
// by Inspect from the validated footer, not from an earlier database read.
type Header struct {
	Version       int              `json:"version"`
	SourceProfile string           `json:"sourceProfile"`
	ABIHash       string           `json:"abiSha256"`
	Deployment    model.Deployment `json:"deployment"`
	Checkpoint    *model.Block     `json:"checkpoint,omitempty"`
}
type record struct {
	Type       string       `json:"type"`
	Header     *Header      `json:"header,omitempty"`
	Batch      *model.Batch `json:"batch,omitempty"`
	Checkpoint *model.Block `json:"checkpoint,omitempty"`
}

// Manifest is published only after the snapshot and SHA256 sidecar are durable.
type Manifest struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Header Header `json:"header"`
}

func header(dep model.Deployment) Header {
	return Header{Version: FormatVersion, SourceProfile: SourceProfile, ABIHash: ABIHash, Deployment: dep}
}
func checkHeader(h Header, dep *model.Deployment) error {
	if h.Version != FormatVersion || h.SourceProfile != SourceProfile || h.ABIHash != ABIHash {
		return errors.New("unsupported snapshot version or source profile")
	}
	if h.Checkpoint != nil {
		return errors.New("checkpoint belongs in snapshot footer")
	}
	// All configured event sources bind indexed history and derived registry state.
	if dep != nil && (h.Deployment.ChainID != dep.ChainID || h.Deployment.Protocol != dep.Protocol || h.Deployment.FromBlock != dep.FromBlock || h.Deployment.RuntimeCodeHash != dep.RuntimeCodeHash || !reflect.DeepEqual(h.Deployment.Registry, dep.Registry) || !reflect.DeepEqual(h.Deployment.BroadcasterRegistry, dep.BroadcasterRegistry)) {
		return errors.New("snapshot deployment differs from configured chain, protocol, runtime code hash, or start block")
	}
	return nil
}
func checkBatch(dep model.Deployment, last *model.Block, b model.Block) error {
	if last == nil {
		if b.Number != dep.IndexFromBlock() {
			return errors.New("snapshot does not begin at deployment start block")
		}
	} else {
		if last.Number == math.MaxUint64 || b.Number != last.Number+1 {
			return errors.New("snapshot blocks are not contiguous")
		}
		if b.ParentHash != last.Hash {
			return errors.New("snapshot block parent hash mismatch")
		}
	}
	return nil
}
func checkCheckpoint(last, checkpoint *model.Block) error {
	if last == nil || checkpoint == nil {
		return errors.New("snapshot is empty or missing its checkpoint")
	}
	if *last != *checkpoint {
		return errors.New("snapshot checkpoint does not match final block")
	}
	return nil
}

// Export writes one consistent database snapshot beneath baseDir/chainID/protocol.
// It never publishes latest.json until gzip close, fsync, and checksum completion.
func Export(ctx context.Context, backend storage.Backend, dep model.Deployment, baseDir string) (path string, err error) {
	if dep.ChainID == "" || strings.IndexFunc(dep.ChainID, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return "", errors.New("snapshot directory requires a decimal chain ID")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	dir := filepath.Join(baseDir, dep.ChainID, strings.ToLower(dep.Protocol.Hex()))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return "", err
	}
	temp := f.Name()
	defer func() { f.Close(); os.Remove(temp) }()
	sum := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(f, sum))
	defer gz.Close()
	enc := json.NewEncoder(recordWriter{gz})
	h := header(dep)
	if err = enc.Encode(record{Type: "header", Header: &h}); err != nil {
		return "", err
	}
	var last *model.Block
	checkpoint, err := backend.Export(ctx, func(b model.Batch) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := checkBatch(dep, last, b.Block); e != nil {
			return e
		}
		b.BindBlockContext()
		if e := enc.Encode(record{Type: "block", Batch: &b}); e != nil {
			return e
		}
		v := b.Block
		last = &v
		return nil
	})
	if err != nil {
		return "", err
	}
	if err = checkCheckpoint(last, checkpoint); err != nil {
		return "", err
	}
	if err = enc.Encode(record{Type: "footer", Checkpoint: checkpoint}); err != nil {
		return "", err
	}
	if err = gz.Close(); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	name := fmt.Sprintf("%d-%s-%s.snapshot.json.gz", checkpoint.Number, strings.TrimPrefix(checkpoint.Hash.Hex(), "0x"), digest)
	path = filepath.Join(dir, name)
	if err = os.Rename(temp, path); err != nil {
		return "", err
	}
	if err = syncDir(dir); err != nil {
		return "", err
	}
	if err = atomicWrite(path+".sha256", []byte(digest+"  "+name+"\n")); err != nil {
		return "", err
	}
	h.Checkpoint = checkpoint
	manifest, err := json.MarshalIndent(Manifest{File: name, SHA256: digest, Header: h}, "", "  ")
	if err != nil {
		return "", err
	}
	if err = atomicWrite(filepath.Join(dir, "latest.json"), append(manifest, '\n')); err != nil {
		return "", err
	}
	return path, nil
}
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".publish-*")
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

type reader struct {
	gz      *gzip.Reader
	limited *io.LimitedReader
	lines   *bufio.Reader
	header  Header
	last    *model.Block
	done    bool
	verify  func() error
}

func openReader(f io.Reader, limit int64) (*reader, error) {
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, errors.New("snapshot decompressed limit must be positive and below MaxInt64")
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	limited := &io.LimitedReader{R: gz, N: limit + 1}
	r := &reader{gz: gz, limited: limited, lines: bufio.NewReader(limited)}
	rec, err := r.readRecord()
	if err != nil {
		gz.Close()
		return nil, err
	}
	if rec.Type != "header" || rec.Header == nil || rec.Batch != nil || rec.Checkpoint != nil {
		gz.Close()
		return nil, errors.New("snapshot must start with a header")
	}
	if err = checkHeader(*rec.Header, nil); err != nil {
		gz.Close()
		return nil, err
	}
	r.header = *rec.Header
	return r, nil
}
func (r *reader) readRecord() (record, error) {
	var line []byte
	for {
		piece, err := r.lines.ReadSlice('\n')
		if r.limited.N == 0 {
			return record{}, errors.New("snapshot exceeds decompressed byte limit")
		}
		if len(line)+len(piece) > MaxRecordBytes {
			return record{}, errors.New("snapshot record exceeds 64 MiB limit")
		}
		line = append(line, piece...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) == 0 {
					return record{}, io.EOF
				}
				return record{}, fmt.Errorf("truncated snapshot record: %w", io.ErrUnexpectedEOF)
			}
			return record{}, fmt.Errorf("truncated snapshot record: %w", err)
		}
		break
	}
	var rec record
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return record{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return record{}, errors.New("multiple values in snapshot record")
	}
	return rec, nil
}
func (r *reader) next(ctx context.Context) (model.Batch, error) {
	if err := ctx.Err(); err != nil {
		return model.Batch{}, err
	}
	if r.done {
		return model.Batch{}, io.EOF
	}
	rec, err := r.readRecord()
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("snapshot missing footer")
		}
		return model.Batch{}, err
	}
	switch rec.Type {
	case "block":
		if rec.Batch == nil || rec.Header != nil || rec.Checkpoint != nil {
			return model.Batch{}, errors.New("invalid snapshot block record")
		}
		b := *rec.Batch
		if err = checkBatch(r.header.Deployment, r.last, b.Block); err != nil {
			return model.Batch{}, err
		}
		v := b.Block
		r.last = &v
		return b, nil
	case "footer":
		if rec.Header != nil || rec.Batch != nil {
			return model.Batch{}, errors.New("invalid snapshot footer record")
		}
		if err = checkCheckpoint(r.last, rec.Checkpoint); err != nil {
			return model.Batch{}, err
		}
		if _, err = r.readRecord(); !errors.Is(err, io.EOF) {
			if err != nil {
				return model.Batch{}, err
			}
			return model.Batch{}, errors.New("snapshot contains records after footer")
		}
		if r.verify != nil {
			if err = r.verify(); err != nil {
				return model.Batch{}, err
			}
		}
		r.header.Checkpoint = rec.Checkpoint
		r.done = true
		return model.Batch{}, io.EOF
	default:
		return model.Batch{}, fmt.Errorf("unexpected snapshot record type %q", rec.Type)
	}
}

// Inspect verifies the stream structure, gzip checksum, and canonical continuity.
func Inspect(path string) (Header, error) { return InspectWithLimit(path, DefaultMaxDecompressedBytes) }
func InspectWithLimit(path string, maxDecompressedBytes int64) (Header, error) {
	return InspectContext(context.Background(), path, maxDecompressedBytes)
}

// InspectContext allows cancellation while scanning a large compressed snapshot.
func InspectContext(ctx context.Context, path string, maxDecompressedBytes int64) (Header, error) {
	if err := ctx.Err(); err != nil {
		return Header{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()
	r, err := openReader(contextReader{ctx, f}, maxDecompressedBytes)
	if err != nil {
		return Header{}, err
	}
	defer r.gz.Close()
	for {
		_, err = r.next(ctx)
		if errors.Is(err, io.EOF) {
			return r.header, nil
		}
		if err != nil {
			return Header{}, err
		}
	}
}

// Restore verifies the optional whole-file checksum before beginning a database
// transaction. Import replays records atomically and reconstructs stored trees.
// A checksum authenticates a file only when obtained from a trusted source.
func Restore(ctx context.Context, backend storage.Backend, dep model.Deployment, path, expectedSHA256 string) error {
	return RestoreWithLimit(ctx, backend, dep, path, expectedSHA256, DefaultMaxDecompressedBytes)
}
func RestoreWithLimit(ctx context.Context, backend storage.Backend, dep model.Deployment, path, expectedSHA256 string, maxDecompressedBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var want []byte
	if expectedSHA256 != "" {
		expected := strings.TrimPrefix(strings.TrimSpace(expectedSHA256), "0x")
		var e error
		want, e = hex.DecodeString(expected)
		if e != nil || len(want) != sha256.Size {
			return errors.New("expected SHA256 must contain 64 hexadecimal digits")
		}
		sum := sha256.New()
		buf := make([]byte, 128<<10)
		for {
			if e = ctx.Err(); e != nil {
				return e
			}
			n, readErr := f.Read(buf)
			if n > 0 {
				sum.Write(buf[:n])
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		if !bytes.Equal(want, sum.Sum(nil)) {
			return errors.New("snapshot SHA256 mismatch")
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	streamSum := sha256.New()
	var source io.Reader = contextReader{ctx, f}
	if want != nil {
		source = io.TeeReader(source, streamSum)
	}
	r, err := openReader(source, maxDecompressedBytes)
	if err != nil {
		return err
	}
	if want != nil {
		r.verify = func() error {
			if !bytes.Equal(want, streamSum.Sum(nil)) {
				return errors.New("snapshot changed after SHA256 verification")
			}
			return nil
		}
	}
	defer r.gz.Close()
	if err = checkHeader(r.header, &dep); err != nil {
		return err
	}
	return backend.Import(ctx, func() (model.Batch, error) { return r.next(ctx) })
}

// json.Encoder emits one complete record per Write. Bound records on export
// using the same limit enforced by the streaming reader.
type recordWriter struct{ io.Writer }

func (w recordWriter) Write(p []byte) (int, error) {
	if len(p) > MaxRecordBytes {
		return 0, errors.New("snapshot record exceeds 64 MiB limit")
	}
	return w.Writer.Write(p)
}

// contextReader checks cancellation between compressed reads, including while
// gzip is reading a large header or a single large block record.
type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
