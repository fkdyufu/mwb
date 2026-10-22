// internal/clipboard/sync_test.go
package clipboard

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend reports each Content sent on events as a clipboard change.
type fakeBackend struct {
	events    chan Content
	processed chan struct{}
	sets      chan Content
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		events:    make(chan Content),
		processed: make(chan struct{}),
		sets:      make(chan Content, 10),
	}
}

func (b *fakeBackend) Set(c Content) error {
	b.sets <- c
	return nil
}

func (b *fakeBackend) Watch(ctx context.Context, onChange func(Content)) error {
	for {
		select {
		case c := <-b.events:
			onChange(c)
			b.processed <- struct{}{}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// change reports c as a local clipboard change and waits until it is handled.
func (b *fakeBackend) change(c Content) {
	b.events <- c
	<-b.processed
}

// nextSet waits for the clipboard to be set.
func (b *fakeBackend) nextSet(t *testing.T) Content {
	t.Helper()
	select {
	case c := <-b.sets:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("clipboard was not set")
		return Content{}
	}
}

// noSet checks that the clipboard is not set shortly.
func (b *fakeBackend) noSet(t *testing.T) {
	t.Helper()
	select {
	case c := <-b.sets:
		t.Fatalf("clipboard unexpectedly set to %s %q", c.MIME, c.Data)
	case <-time.After(100 * time.Millisecond):
	}
}

type sent struct {
	image   bool
	payload []byte
}

// pushedData is a transfer pushed to the peer.
type pushedData struct {
	postAction uint32
	name       string
	size       int64
	data       []byte
}

// fakePeer records sent payloads and pushes, and serves one fetchable
// transfer.
type fakePeer struct {
	mu        sync.Mutex
	sent      []sent
	fetches   int
	announces int
	pushes    chan pushedData

	name string
	data []byte
	size int64
}

func (p *fakePeer) SendClipboard(image bool, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, sent{image, payload})
	return nil
}

func (p *fakePeer) AnnounceClipboard() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.announces++
	return nil
}

func (p *fakePeer) PushClipboard(postAction uint32, name string, size int64, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	p.pushes <- pushedData{postAction, name, size, data}
	return nil
}

func (p *fakePeer) FetchClipboard() (string, int64, io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fetches++
	return p.name, p.size, io.NopCloser(bytes.NewReader(p.data)), nil
}

func (p *fakePeer) offer(name string, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.name, p.data, p.size = name, data, int64(len(data))
}

func (p *fakePeer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

func (p *fakePeer) fetchCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fetches
}

func (p *fakePeer) announceCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.announces
}

// nextPush waits for contents to be pushed to the peer.
func (p *fakePeer) nextPush(t *testing.T) pushedData {
	t.Helper()
	select {
	case d := <-p.pushes:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was pushed")
		return pushedData{}
	}
}

// noPush checks that nothing is pushed to the peer shortly.
func (p *fakePeer) noPush(t *testing.T) {
	t.Helper()
	select {
	case d := <-p.pushes:
		t.Fatalf("unexpectedly pushed %q", d.name)
	case <-time.After(100 * time.Millisecond):
	}
}

func startSync(t *testing.T, opts Options) (*Sync, *fakeBackend, *fakePeer) {
	t.Helper()
	backend := newFakeBackend()
	peer := &fakePeer{pushes: make(chan pushedData, 10)}
	s := NewSync(backend, opts)
	s.SetPeer(peer)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Run(ctx)
	backend.change(Content{}) // the clipboard at startup
	return s, backend, peer
}

// waitFetched waits until s has finished fetching, so the next switch can
// fetch again.
func waitFetched(t *testing.T, s *Sync) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		fetching := s.fetching
		s.mu.Unlock()
		if !fetching {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("fetch did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func defaultOptions(t *testing.T) Options {
	return Options{FileDir: filepath.Join(t.TempDir(), "clipboard"), MaxTransferSize: 1 << 20}
}

func text(s string) Content {
	return Content{MIME: MIMEText, Data: []byte(s)}
}

func TestSyncSendsLocalChanges(t *testing.T) {
	backend := newFakeBackend()
	peer := &fakePeer{}
	s := NewSync(backend, defaultOptions(t))
	s.SetPeer(peer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	backend.change(text("already there"))
	if peer.count() != 0 {
		t.Fatal("clipboard contents present at startup should not be sent")
	}

	backend.change(text("copied\non linux"))
	if peer.count() != 1 {
		t.Fatalf("sent %d payloads, want 1", peer.count())
	}
	got := peer.sent[0]
	if got.image {
		t.Error("text sent as image")
	}
	decoded, err := DecodeText(got.payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "copied\non linux" {
		t.Errorf("sent %q", decoded)
	}

	backend.change(text("copied\non linux"))
	if peer.count() != 1 {
		t.Error("unchanged contents were sent again")
	}
}

func TestSyncSendsImages(t *testing.T) {
	_, backend, peer := startSync(t, defaultOptions(t))

	img := []byte("\x89PNG fake image data")
	backend.change(Content{MIME: MIMEPNG, Data: img})
	if peer.count() != 1 {
		t.Fatalf("sent %d payloads, want 1", peer.count())
	}
	if !peer.sent[0].image || !bytes.Equal(peer.sent[0].payload, img) {
		t.Errorf("sent image=%v payload=%q", peer.sent[0].image, peer.sent[0].payload)
	}
}

func TestSyncDoesNotEchoRemoteContents(t *testing.T) {
	s, backend, peer := startSync(t, defaultOptions(t))

	payload, err := EncodeText("from\r\nwindows")
	if err != nil {
		t.Fatal(err)
	}
	s.ApplyRemote(false, payload)
	if c := backend.nextSet(t); c.MIME != MIMEText || string(c.Data) != "from\nwindows" {
		t.Fatalf("local clipboard set to %s %q", c.MIME, c.Data)
	}

	// Setting the clipboard triggers a local change with the same contents.
	backend.change(text("from\nwindows"))
	if peer.count() != 0 {
		t.Error("contents received from the peer were sent back")
	}
}

func TestSyncSkipsUnsyncableContents(t *testing.T) {
	s, backend, peer := startSync(t, defaultOptions(t))

	backend.change(Content{})
	big := make([]byte, MaxPayload+1)
	big[0] = 1
	backend.change(Content{MIME: MIMEPNG, Data: big})
	if peer.count() != 0 {
		t.Fatalf("sent %d payloads, want 0", peer.count())
	}

	s.SetPeer(nil)
	backend.change(text("while disconnected"))
	if peer.count() != 0 {
		t.Error("sent while disconnected")
	}
}

func TestSyncRemoteHasDataAllowsResending(t *testing.T) {
	s, backend, peer := startSync(t, defaultOptions(t))

	backend.change(text("same"))
	s.RemoteHasData()
	backend.change(text("same"))
	if peer.count() != 2 {
		t.Errorf("sent %d payloads, want 2", peer.count())
	}
}

func TestApplyRemoteIgnoresInvalidPayloads(t *testing.T) {
	s, backend, _ := startSync(t, defaultOptions(t))
	s.ApplyRemote(false, []byte("garbage"))
	s.ApplyRemote(true, []byte("not a png"))
	backend.noSet(t)
}

func TestSyncFetchesFiles(t *testing.T) {
	opts := defaultOptions(t)
	s, backend, peer := startSync(t, opts)

	peer.offer(`C:\Users\me\报告 v1.txt`, []byte("first"))
	s.RemoteHasData()
	s.MachineSwitched()
	c := backend.nextSet(t)

	path := filepath.Join(opts.FileDir, "报告 v1.txt")
	if c.MIME != MIMEURIList || string(c.Data) != fileURI(path) {
		t.Fatalf("clipboard set to %s %q, want uri-list of %s", c.MIME, c.Data, path)
	}
	if !strings.HasPrefix(string(c.Data), "file:///") || strings.Contains(string(c.Data), " ") {
		t.Errorf("uri not escaped: %q", c.Data)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "first" {
		t.Fatalf("saved file = %q, %v", got, err)
	}

	// wl-copy also offers the uri-list as text; it must not be sent back.
	backend.change(text(string(c.Data)))
	if peer.count() != 0 {
		t.Error("received file uri was sent back")
	}

	// A new file replaces the previous one.
	waitFetched(t, s)
	peer.offer(`D:\second.bin`, []byte("second"))
	s.RemoteHasData()
	s.MachineSwitched()
	backend.nextSet(t)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("previous file still present: %v", err)
	}
	entries, _ := os.ReadDir(opts.FileDir)
	if len(entries) != 1 || entries[0].Name() != "second.bin" {
		t.Errorf("dir holds %v, want only second.bin", entries)
	}
}

func TestSyncFetchesBigText(t *testing.T) {
	s, backend, peer := startSync(t, defaultOptions(t))
	payload, err := EncodeText("big text")
	if err != nil {
		t.Fatal(err)
	}
	peer.offer("text", payload)
	s.RemoteHasData()
	s.MachineSwitched()
	if c := backend.nextSet(t); c.MIME != MIMEText || string(c.Data) != "big text" {
		t.Errorf("clipboard set to %s %q", c.MIME, c.Data)
	}
}

func TestSyncSkipsFetches(t *testing.T) {
	t.Run("too large", func(t *testing.T) {
		opts := defaultOptions(t)
		opts.MaxTransferSize = 4
		s, backend, peer := startSync(t, opts)
		peer.offer(`C:\big.iso`, []byte("12345"))
		s.RemoteHasData()
		s.MachineSwitched()
		backend.noSet(t)
	})
	t.Run("not sent by peer", func(t *testing.T) {
		s, backend, peer := startSync(t, defaultOptions(t))
		peer.offer(`C:\folder - Folder is not supported, zip it first!`, nil)
		s.RemoteHasData()
		s.MachineSwitched()
		backend.noSet(t)
	})
	t.Run("disabled", func(t *testing.T) {
		opts := defaultOptions(t)
		opts.MaxTransferSize = 0
		s, backend, peer := startSync(t, opts)
		peer.offer(`C:\a.txt`, []byte("a"))
		s.RemoteHasData()
		s.MachineSwitched()
		backend.noSet(t)
		if peer.fetchCount() != 0 {
			t.Error("fetched although fetching is disabled")
		}
	})
	t.Run("not announced", func(t *testing.T) {
		s, backend, peer := startSync(t, defaultOptions(t))
		peer.offer(`C:\a.txt`, []byte("a"))
		s.MachineSwitched()
		backend.noSet(t)
		if peer.fetchCount() != 0 {
			t.Error("fetched without an announcement")
		}
	})
	t.Run("announcement expired", func(t *testing.T) {
		s, backend, peer := startSync(t, defaultOptions(t))
		now := time.Now()
		s.now = func() time.Time { return now }
		peer.offer(`C:\a.txt`, []byte("a"))
		s.RemoteHasData()
		now = now.Add(fetchWindow + time.Second)
		s.MachineSwitched()
		backend.noSet(t)
		if peer.fetchCount() != 0 {
			t.Error("fetched an expired announcement")
		}
	})
}

func TestRemoteBaseName(t *testing.T) {
	for path, want := range map[string]string{
		`C:\Users\me\doc.txt`:    "doc.txt",
		`\\server\share\a b.pdf`: "a b.pdf",
		"/unix/style":            "style",
		"plain":                  "plain",
		`C:\dir\`:                "",
		`C:\..`:                  "",
		"bad\x01name":            "",
		"":                       "",
	} {
		if got := remoteBaseName(path); got != want {
			t.Errorf("remoteBaseName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSaveFileIncomplete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "clipboard")
	if _, err := saveFile(dir, `C:\a.txt`, strings.NewReader("abc"), 10); err == nil {
		t.Fatal("expected error for incomplete file")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("incomplete file left behind: %v", entries)
	}
}

// copyFiles reports files copied in a file manager as a local change.
func copyFiles(b *fakeBackend, paths ...string) {
	var list string
	for _, p := range paths {
		list += fileURI(p)
	}
	b.change(Content{MIME: MIMEURIList, Data: []byte(list)})
}

func TestSyncPushesFiles(t *testing.T) {
	s, backend, peer := startSync(t, defaultOptions(t))
	dir := t.TempDir()
	first := filepath.Join(dir, "12:30 report.txt")
	second := filepath.Join(dir, "second.txt")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("contents of "+filepath.Base(p)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// MWB sends only the first of several files.
	copyFiles(backend, first, second)
	if peer.announceCount() != 1 || peer.count() != 0 {
		t.Fatalf("announced %d times and sent %d payloads, want 1 and 0", peer.announceCount(), peer.count())
	}
	s.PushRequested(1)
	got := peer.nextPush(t)
	if got.postAction != 1 || got.name != "/12_30 report.txt" || string(got.data) != "contents of 12:30 report.txt" || got.size != int64(len(got.data)) {
		t.Errorf("pushed %+v", got)
	}

	// The file is sent again when asked again, as it changed since.
	waitPushed(t, s)
	if err := os.WriteFile(first, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.PushRequested(0)
	if got := peer.nextPush(t); string(got.data) != "changed" {
		t.Errorf("pushed %q after the file changed", got.data)
	}
}

func TestSyncPushesBigContents(t *testing.T) {
	opts := defaultOptions(t)
	opts.MaxTransferSize = 4 << 20
	s, backend, peer := startSync(t, opts)

	img := append([]byte("\x89PNG"), make([]byte, MaxPayload)...)
	backend.change(Content{MIME: MIMEPNG, Data: img})
	if peer.announceCount() != 1 || peer.count() != 0 {
		t.Fatalf("announced %d times and sent %d payloads, want 1 and 0", peer.announceCount(), peer.count())
	}
	s.PushRequested(0)
	if got := peer.nextPush(t); got.name != "image" || !bytes.Equal(got.data, img) {
		t.Errorf("pushed %q with %d bytes", got.name, len(got.data))
	}
}

func TestSyncDoesNotOfferUnsendableFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, 5), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := defaultOptions(t)
	opts.MaxTransferSize = 4

	for name, list := range map[string]string{
		"folder":    fileURI(dir),
		"too large": fileURI(big),
		"missing":   fileURI(filepath.Join(dir, "missing.txt")),
	} {
		t.Run(name, func(t *testing.T) {
			s, backend, peer := startSync(t, opts)
			backend.change(Content{MIME: MIMEURIList, Data: []byte(list)})
			s.PushRequested(0)
			peer.noPush(t)
			if peer.announceCount() != 0 {
				t.Error("unsendable file was announced")
			}
		})
	}
	t.Run("disabled", func(t *testing.T) {
		opts := defaultOptions(t)
		opts.MaxTransferSize = 0
		s, backend, peer := startSync(t, opts)
		copyFiles(backend, big)
		s.PushRequested(0)
		peer.noPush(t)
		if peer.announceCount() != 0 {
			t.Error("file announced although transfers are disabled")
		}
	})
}

func TestSyncOfferReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeText("from windows")
	if err != nil {
		t.Fatal(err)
	}

	for name, replace := range map[string]func(*testing.T, *Sync, *fakeBackend){
		"by local text": func(_ *testing.T, _ *Sync, b *fakeBackend) { b.change(text("local text")) },
		"by clearing":   func(_ *testing.T, _ *Sync, b *fakeBackend) { b.change(Content{}) },
		"by remote text": func(t *testing.T, s *Sync, b *fakeBackend) {
			s.ApplyRemote(false, payload)
			b.nextSet(t)
		},
		"by disconnecting": func(_ *testing.T, s *Sync, _ *fakeBackend) { s.SetPeer(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			s, backend, peer := startSync(t, defaultOptions(t))
			copyFiles(backend, path)
			replace(t, s, backend)
			s.PushRequested(0)
			peer.noPush(t)
		})
	}
}

// waitPushed waits until s has finished pushing, so it can push again.
func waitPushed(t *testing.T, s *Sync) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		pushing := s.pushing
		s.mu.Unlock()
		if !pushing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("push did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFilePaths(t *testing.T) {
	list := "# comment\r\nfile:///home/me/a%20b.txt\r\nhttps://example.com/x\r\nfile://localhost/tmp/c\nfile://otherhost/d\r\n\r\nfile:relative\r\n"
	got := filePaths([]byte(list))
	want := []string{"/home/me/a b.txt", "/tmp/c"}
	if !slices.Equal(got, want) {
		t.Errorf("filePaths = %q, want %q", got, want)
	}
}

func TestPushName(t *testing.T) {
	for path, want := range map[string]string{
		"/home/me/report.pdf":    "/report.pdf",
		"/home/me/text notes.md": "/text notes.md",
		`/tmp/a:b*c?"d<e>f|g\h`:  "/a_b_c__d_e_f_g_h",
		"/tmp/trailing. .":       "/trailing",
		"/tmp/...":               "/file",
		"/tmp/tab\tname":         "/tab_name",
	} {
		if got := pushName(path); got != want {
			t.Errorf("pushName(%q) = %q, want %q", path, got, want)
		}
	}
}
