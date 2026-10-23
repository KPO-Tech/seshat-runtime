package pdfsmart

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpumodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	gopdf "github.com/razvandimescu/gopdf/pdf"
)

// ErrPasswordRequired is returned for a PDF that is encrypted with a password the caller did not give, or gave
// wrongly. An encrypted PDF whose user password is empty (an emailed statement with an owner password that only
// limits printing and copying) is not that: it is opened.
var ErrPasswordRequired = errors.New("the PDF is protected by a password")

// Unlock returns the bytes of a PDF in a form the page reader can open. A PDF that is not encrypted, or that the
// reader already opens (RC4 and AES-128 with an empty user password), comes back unchanged. One it cannot open
// (AES-256, and anything encrypted with a user password) is decrypted into a plain copy.
//
// With no password the empty user password is tried, which is what every viewer does first. A password that is
// needed and not given, or wrong, gives ErrPasswordRequired.
func Unlock(ctx context.Context, data []byte, password string) ([]byte, error) {
	if opens(data) {
		return data, nil
	}
	if !bytes.Contains(data, []byte("/Encrypt")) {
		return data, nil // not an encryption problem; the caller reports what is wrong with the file
	}

	key := sha256.Sum256(append([]byte(password+"\x00"), data...))
	if plain, ok := unlocked.get(key); ok {
		return plain, nil
	}
	plain, err := decrypt(ctx, data, password)
	if err != nil {
		return nil, err
	}
	unlocked.put(key, plain)
	return plain, nil
}

// opens says whether the page reader can open the PDF as it is.
func opens(data []byte) bool {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	return err == nil && reader.NumPage() > 0
}

// decrypt tries the two decryptors that are available. gopdf opens a PDF whose user password is empty, whatever the
// algorithm and however the encryption dictionary is written (some producers write it inline in the trailer).
// pdfcpu takes a password, but only follows an encryption dictionary that is an indirect object, which is what
// most producers write.
func decrypt(ctx context.Context, data []byte, password string) ([]byte, error) {
	var lastErr error
	if password == "" {
		merged, err := gopdf.MergeBytes(data)
		if err == nil && opens(merged) {
			return merged, nil
		}
		lastErr = err
		if errors.Is(err, gopdf.ErrEncrypted) || errors.Is(err, gopdf.ErrWrongPassword) {
			return nil, ErrPasswordRequired
		}
	}

	conf := pdfcpumodel.NewDefaultConfiguration()
	conf.UserPW, conf.OwnerPW = password, password
	var out bytes.Buffer
	err := api.Decrypt(ctx, bytes.NewReader(data), &out, conf)
	if err == nil && opens(out.Bytes()) {
		return out.Bytes(), nil
	}
	if err != nil {
		lastErr = err
	}
	if lastErr != nil && strings.Contains(strings.ToLower(lastErr.Error()), "password") {
		return nil, ErrPasswordRequired
	}
	return nil, fmt.Errorf("pdfsmart: the encrypted PDF could not be decrypted: %w", lastErr)
}

// unlocked remembers the last few decrypted PDFs, because a long document is read in several calls and decrypting
// it is the slow part of each.
var unlocked = newUnlockCache(4)

type unlockCache struct {
	mu    sync.Mutex
	max   int
	order *list.List
	items map[[32]byte]*list.Element
}

type unlockEntry struct {
	key  [32]byte
	data []byte
}

func newUnlockCache(max int) *unlockCache {
	return &unlockCache{max: max, order: list.New(), items: map[[32]byte]*list.Element{}}
}

func (c *unlockCache) get(key [32]byte) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*unlockEntry).data, true
	}
	return nil, false
}

func (c *unlockCache) put(key [32]byte, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&unlockEntry{key, data})
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*unlockEntry).key)
	}
}
