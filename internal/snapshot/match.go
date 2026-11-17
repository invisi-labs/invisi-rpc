package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/invisi-lab/invisi-rpc/internal/model"
	"github.com/invisi-lab/invisi-rpc/internal/storage"
)

// Matches compares an already restored deployment with a snapshot without
// modifying the database. A consistent export is compared in order, and the
// complete source stream is validated even after a content mismatch is found.
func Matches(ctx context.Context, backend storage.Backend, dep model.Deployment, path, expectedSHA256 string, maxBytes int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var want []byte
	if expectedSHA256 != "" {
		var err error
		want, err = hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(expectedSHA256), "0x"))
		if err != nil || len(want) != sha256.Size {
			return false, errors.New("expected SHA256 must contain 64 hexadecimal digits")
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sum := sha256.New()
	r, err := openReader(io.TeeReader(contextReader{ctx, f}, sum), maxBytes)
	if err != nil {
		return false, err
	}
	defer r.gz.Close()
	if err = checkHeader(r.header, &dep); err != nil {
		return false, err
	}
	r.verify = func() error {
		if want != nil && !bytes.Equal(want, sum.Sum(nil)) {
			return errors.New("snapshot SHA256 mismatch")
		}
		return ctx.Err()
	}
	matched := true
	cp, err := backend.Export(ctx, func(stored model.Batch) error {
		source, e := r.next(ctx)
		if errors.Is(e, io.EOF) {
			matched = false
			return nil
		}
		if e != nil {
			return e
		}
		same, e := sameBatchJSON(stored, source)
		if e != nil {
			return e
		}
		if !same {
			matched = false
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	// Consume remaining records to verify footer, gzip EOF, limits and checksum.
	for {
		_, err = r.next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, err
		}
		matched = false
	}
	if cp == nil || r.header.Checkpoint == nil || *cp != *r.header.Checkpoint {
		matched = false
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	return matched, nil
}

func sameBatchJSON(a, b model.Batch) (bool, error) {
	normalize := func(batch model.Batch) (any, error) {
		raw, err := json.Marshal(batch)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var v any
		if err = decoder.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	}
	left, err := normalize(a)
	if err != nil {
		return false, err
	}
	right, err := normalize(b)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(left, right), nil
}
