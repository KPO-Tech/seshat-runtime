package titlemodel

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// State is what is recorded of an installation, in <runtime root>/title-model/state.json.
type State struct {
	// Enabled says whether sessions are named with the local model. Installing turns it on; `disable` turns it off
	// without deleting anything.
	Enabled    bool   `json:"enabled"`
	ModelID    string `json:"model_id"`
	ModelPath  string `json:"model_path"`
	ServerPath string `json:"server_path"`
	LlamaTag   string `json:"llama_tag"`
}

// Ready reports whether both files an installation records are still on disk.
func (s State) Ready() bool {
	if s.ModelPath == "" || s.ServerPath == "" {
		return false
	}
	for _, p := range []string{s.ModelPath, s.ServerPath} {
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

// Progress is called while a file downloads.
type Progress func(what string, done, total int64)

// Installer downloads, checks and records the server and the model.
type Installer struct {
	// Root is the runtime root; everything lives in Root/title-model.
	Root string
	// LlamaBase and HFBase are the download locations (tests point them at a local server).
	LlamaBase string
	HFBase    string
	HTTP      *http.Client
	// Archives and Models override the catalog (tests).
	Archives map[string]Archive
	Models   []Model
	// GOOS and GOARCH override the platform (tests).
	GOOS, GOARCH string
	Progress     Progress
}

// NewInstaller returns an installer for the official locations and the real platform.
func NewInstaller(root string) *Installer {
	return &Installer{Root: root, LlamaBase: LlamaBaseURL, HFBase: HFBaseURL, HTTP: &http.Client{Timeout: 45 * time.Minute}}
}

func (in *Installer) dir() string { return filepath.Join(in.Root, "title-model") }

// StatePath is where the installation is recorded.
func StatePath(root string) string { return filepath.Join(root, "title-model", "state.json") }

// LoadState reads the recorded installation; a missing or damaged file is an empty state.
func LoadState(root string) State {
	var st State
	if data, err := os.ReadFile(StatePath(root)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

// SaveState records an installation.
func SaveState(root string, st State) error {
	path := StatePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (in *Installer) platform() (string, string) {
	goos, goarch := in.GOOS, in.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

func (in *Installer) findArchive() (Archive, error) {
	goos, goarch := in.platform()
	table := in.Archives
	if table == nil {
		table = archives
	}
	if a, ok := table[goos+"-"+goarch]; ok {
		return a, nil
	}
	return Archive{}, fmt.Errorf("there is no llama.cpp build for %s/%s: sessions keep being named by the chat provider", goos, goarch)
}

func (in *Installer) findModel(id string) (Model, error) {
	if id == "" {
		id = DefaultModelID
	}
	models := in.Models
	if models == nil {
		models = Models
	}
	for _, m := range models {
		if m.ID == id {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("unknown title model %q", id)
}

// Install downloads the llama.cpp server and the model (what is already there and intact is not downloaded again),
// then records the installation and turns it on.
func (in *Installer) Install(ctx context.Context, modelID string) (State, error) {
	archive, err := in.findArchive()
	if err != nil {
		return State{}, err
	}
	model, err := in.findModel(modelID)
	if err != nil {
		return State{}, err
	}

	serverPath, err := in.installServer(ctx, archive)
	if err != nil {
		return State{}, err
	}
	modelPath, err := in.installModel(ctx, model)
	if err != nil {
		return State{}, err
	}
	st := State{Enabled: true, ModelID: model.ID, ModelPath: modelPath, ServerPath: serverPath, LlamaTag: LlamaTag}
	if err := SaveState(in.Root, st); err != nil {
		return State{}, fmt.Errorf("record the installation: %w", err)
	}
	return st, nil
}

func serverBinaryName(goos string) string {
	if goos == "windows" {
		return "llama-server.exe"
	}
	return "llama-server"
}

func (in *Installer) installServer(ctx context.Context, archive Archive) (string, error) {
	goos, _ := in.platform()
	target := filepath.Join(in.dir(), "llama", LlamaTag)
	if found := findFile(target, serverBinaryName(goos)); found != "" {
		return found, nil
	}

	if err := os.MkdirAll(in.dir(), 0o700); err != nil {
		return "", err
	}
	archivePath := filepath.Join(in.dir(), archive.Name+".partial")
	defer os.Remove(archivePath)
	if err := in.download(ctx, strings.TrimRight(in.LlamaBase, "/")+"/"+archive.Name, archivePath, archive.SHA256, archive.Size, "llama.cpp"); err != nil {
		return "", err
	}

	staging := target + ".staging"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	var extractErr error
	if strings.HasSuffix(archive.Name, ".zip") {
		extractErr = extractZip(archivePath, staging)
	} else {
		extractErr = extractTarGz(archivePath, staging)
	}
	if extractErr != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("unpack %s: %w", archive.Name, extractErr)
	}
	if findFile(staging, serverBinaryName(goos)) == "" {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("%s does not contain %s", archive.Name, serverBinaryName(goos))
	}
	_ = os.RemoveAll(target)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	found := findFile(target, serverBinaryName(goos))
	if goos != "windows" {
		if err := os.Chmod(found, 0o755); err != nil {
			return "", err
		}
	}
	return found, nil
}

func (in *Installer) installModel(ctx context.Context, model Model) (string, error) {
	dest := filepath.Join(in.dir(), "models", model.File)
	if sum, err := fileSHA256(dest); err == nil && sum == model.SHA256 {
		return dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/%s/resolve/main/%s", strings.TrimRight(in.HFBase, "/"), model.Repo, model.File)
	partial := dest + ".partial"
	defer os.Remove(partial)
	if err := in.download(ctx, url, partial, model.SHA256, model.Size, model.Label); err != nil {
		return "", err
	}
	if err := os.Rename(partial, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// maxDownloadSlack bounds a download above its announced size, in case the size in the catalog is a little off.
const maxDownloadSlack = 64 << 20

// download fetches url into path and checks its SHA-256 against want. Nothing is left at path on failure.
func (in *Installer) download(ctx context.Context, url, path, want string, size int64, what string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "seshat-title-model")
	httpClient := in.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", what, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s answered %s", what, url, resp.Status)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	total := resp.ContentLength
	if total <= 0 {
		total = size
	}
	reader := io.Reader(io.LimitReader(resp.Body, size+maxDownloadSlack))
	if in.Progress != nil {
		reader = &progressReader{r: reader, what: what, total: total, report: in.Progress}
	}
	written, copyErr := io.Copy(io.MultiWriter(file, hash), reader)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("download %s: %w", what, errors.Join(copyErr, closeErr))
	}
	if written > size+maxDownloadSlack-1 {
		_ = os.Remove(path)
		return fmt.Errorf("download %s: larger than expected", what)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		_ = os.Remove(path)
		return fmt.Errorf("download %s: the checksum does not match the one fixed in seshat (got %s): nothing was installed", what, got)
	}
	return nil
}

type progressReader struct {
	r      io.Reader
	what   string
	total  int64
	done   int64
	last   time.Time
	report Progress
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if now := time.Now(); now.Sub(p.last) > 250*time.Millisecond || err != nil {
		p.last = now
		p.report(p.what, p.done, p.total)
	}
	return n, err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// findFile returns the path of the first file called name under dir, or "".
func findFile(dir, name string) string {
	var found string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// Remove deletes everything an installation put on disk.
func Remove(root string) error {
	return os.RemoveAll(filepath.Join(root, "title-model"))
}

// inside reports whether target, joined to base, stays under base (no "..", no absolute path).
func inside(base, name string) (string, bool) {
	dest := filepath.Join(base, filepath.FromSlash(name))
	rel, err := filepath.Rel(base, dest)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
		return "", false
	}
	return dest, true
}

// maxExtractedBytes bounds what an archive may unpack to: the CPU builds are a few tens of megabytes.
const maxExtractedBytes = 400 << 20

// checkWritable refuses an entry that would be created outside dest through a link the archive made earlier. The name of an entry is
// checked as text by inside(); that is not enough once the archive holds a link, because "link/file" is under dest as text and
// wherever the link points on disk. The folder that holds the entry (or the closest one that exists) is resolved, with every link
// followed, and must still be under dest; and an entry is never written over a link, which a write would follow.
func checkWritable(dest, target string) error {
	realDest, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return err
	}
	existing := filepath.Dir(target)
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return fmt.Errorf("the archive holds a path outside its folder: %q", target)
		}
		existing = parent
	}
	realParent, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(realDest, realParent); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("the archive writes through a link that leaves its folder: %q", target)
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("the archive writes over a link: %q", target)
	}
	return nil
}

func extractZip(archivePath, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	var total int64
	for _, f := range zr.File {
		target, ok := inside(dest, f.Name)
		if !ok {
			return fmt.Errorf("the archive holds a path outside its folder: %q", f.Name)
		}
		if err := checkWritable(dest, target); err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		// The size is a field of the archive, so it is not trusted: a huge value must not wrap around into a small one, and what is
		// really written is counted against what is left, whatever the header said.
		if f.UncompressedSize64 > uint64(maxExtractedBytes-total) {
			return errors.New("the archive unpacks to more than expected")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		written, err := writeZipEntry(f, target, maxExtractedBytes-total)
		if err != nil {
			return err
		}
		total += written
	}
	return nil
}

// writeZipEntry writes one entry and stops with an error when it holds more than budget bytes, whatever its header says.
func writeZipEntry(f *zip.File, target string, budget int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) // #nosec G302 G304 -- target was checked by inside and checkWritable
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(out, io.LimitReader(rc, budget+1))
	if err != nil {
		_ = out.Close()
		return written, err
	}
	if written > budget {
		_ = out.Close()
		return written, errors.New("the archive unpacks to more than expected")
	}
	return written, out.Close()
}

func extractTarGz(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, ok := inside(dest, hdr.Name)
		if !ok {
			return fmt.Errorf("the archive holds a path outside its folder: %q", hdr.Name)
		}
		if err := checkWritable(dest, target); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > maxExtractedBytes-total {
				return errors.New("the archive unpacks to more than expected")
			}
			total += hdr.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, hdr.Size)); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// A shared library is often a link to its versioned file; the link must stay inside the folder.
			resolved := filepath.Join(filepath.Dir(target), filepath.FromSlash(hdr.Linkname))
			if filepath.IsAbs(hdr.Linkname) {
				return fmt.Errorf("the archive holds an absolute link: %q", hdr.Name)
			}
			if _, ok := inside(dest, mustRel(dest, resolved)); !ok {
				return fmt.Errorf("the archive holds a link that leaves its folder: %q", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// The check above reads the link as text. Where the link would lead on disk, links already made included, is what counts:
			// "s" pointing at "." and then "t" pointing at "s/.." is inside as text and outside on disk.
			if err := checkLinkTarget(dest, target, hdr.Linkname); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// checkLinkTarget refuses a link whose target, followed on disk through the links that exist now, leaves dest. A target that does not
// exist yet cannot be followed; checkWritable stops anything from being written through it later.
func checkLinkTarget(dest, link, linkname string) error {
	realDest, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return err
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(link))
	if err != nil {
		return err
	}
	// Joined as text, not with filepath.Join, which would cancel a ".." against the name before it and hide the link in between.
	resolved, err := filepath.EvalSymlinks(realParent + string(filepath.Separator) + filepath.FromSlash(linkname))
	if err != nil {
		return nil
	}
	if rel, err := filepath.Rel(realDest, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("the archive holds a link that leaves its folder: %q", link)
	}
	return nil
}

func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return ".." + string(filepath.Separator)
	}
	return filepath.ToSlash(rel)
}
