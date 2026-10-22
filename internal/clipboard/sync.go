// internal/clipboard/sync.go
package clipboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Backend reads and writes the local system clipboard.
type Backend interface {
	// Set replaces the clipboard contents.
	Set(c Content) error
	// Watch calls onChange with the current contents once when it starts and
	// again after every change, until ctx is done or watching fails. Contents
	// that cannot be synced are reported with nil Data.
	Watch(ctx context.Context, onChange func(Content)) error
}

// Peer is the MWB machine the clipboard is synced with.
type Peer interface {
	// SendClipboard sends a payload over the message connection.
	SendClipboard(image bool, payload []byte) error
	// AnnounceClipboard tells the peer that contents it has to request are
	// available: files and contents over MaxPayload.
	AnnounceClipboard() error
	// FetchClipboard requests the contents the peer announced as available,
	// returning the transfer name ("text", "image" or a file path, or why
	// nothing is sent when size is 0) and a reader for size bytes.
	FetchClipboard() (name string, size int64, body io.ReadCloser, err error)
	// PushClipboard sends announced contents the peer asked for: name is
	// "text", "image" or a file path, and body holds size bytes.
	PushClipboard(postAction uint32, name string, size int64, body io.Reader) error
}

// fetchWindow is how long after the peer announces contents a switch to this
// machine fetches them, as in MWB.
const fetchWindow = 30 * time.Second

// Options configures a Sync.
type Options struct {
	// FileDir stores files received from the peer. It is emptied before each
	// file is received, so it must be dedicated to mwb.
	FileDir string
	// MaxTransferSize bounds the files and big contents transferred over
	// the clipboard connection, in either direction; 0 disables them.
	MaxTransferSize int64
}

// Sync keeps the local clipboard and the MWB peer's clipboard in step.
type Sync struct {
	backend Backend
	opts    Options
	now     func() time.Time

	mu   sync.Mutex
	peer Peer
	// last is the hash of the most recent contents synced in either
	// direction, so contents applied from the peer are not sent back.
	last [sha256.Size]byte
	// announced is when the peer last announced contents to fetch.
	announced time.Time
	fetching  bool
	// offered is the local contents announced to the peer, if any.
	offered *offer
	pushing bool
}

// offer is local clipboard contents the peer has to request: a file, or a
// payload too big for the message connection.
type offer struct {
	path    string // the file, or "" for a payload
	image   bool
	payload []byte
}

// NewSync returns a Sync using backend for the local clipboard.
func NewSync(backend Backend, opts Options) *Sync {
	return &Sync{backend: backend, opts: opts, now: time.Now}
}

// SetPeer sets the peer to sync with; nil stops syncing with the peer.
func (s *Sync) SetPeer(peer Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peer = peer
	s.announced = time.Time{}
}

// Run watches the local clipboard until ctx is done, restarting the watcher
// when it fails (for example before the compositor is running).
func (s *Sync) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		started := time.Now()
		baseline := true
		err := s.backend.Watch(ctx, func(c Content) {
			// The first report is the clipboard as it was before the watcher
			// started; remember it without overwriting the peer's clipboard.
			if baseline {
				baseline = false
				s.remember(c)
				return
			}
			s.localChanged(c)
		})
		if ctx.Err() != nil {
			return
		}

		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("clipboard watcher stopped", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// RemoteHasData records that the peer copied contents it only sends on
// request: files and contents over MaxPayload.
func (s *Sync) RemoteHasData() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Forget the last synced contents, so copying them again sends them.
	s.last = [sha256.Size]byte{}
	s.announced = s.now()
	slog.Debug("remote clipboard has data to fetch")
}

// MachineSwitched fetches the contents the peer announced, if any, now that
// the mouse moved to this machine.
func (s *Sync) MachineSwitched() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.announced.IsZero() || s.now().Sub(s.announced) > fetchWindow {
		return
	}
	s.announced = time.Time{}
	if s.opts.MaxTransferSize <= 0 {
		slog.Info("remote clipboard holds files or big contents, fetching is disabled")
		return
	}
	if s.peer == nil || s.fetching {
		return
	}
	s.fetching = true
	go func(peer Peer) {
		s.fetch(peer)
		s.mu.Lock()
		s.fetching = false
		s.mu.Unlock()
	}(s.peer)
}

func (s *Sync) fetch(peer Peer) {
	name, size, body, err := peer.FetchClipboard()
	if err != nil {
		slog.Warn("fetch remote clipboard", "err", err)
		return
	}
	defer func() { _ = body.Close() }()

	switch {
	case size == 0:
		// The name explains why, e.g. folders or files over 100 MB.
		slog.Info("remote clipboard not transferred", "reason", name)
		return
	case size > s.opts.MaxTransferSize:
		slog.Info("remote clipboard too large to fetch", "name", remoteBaseName(name), "bytes", size, "max", s.opts.MaxTransferSize)
		return
	}

	if hasPrefixFold(name, "text") || hasPrefixFold(name, "image") {
		payload, err := io.ReadAll(body)
		if err != nil {
			slog.Warn("fetch remote clipboard", "err", err)
			return
		}
		s.ApplyRemote(hasPrefixFold(name, "image"), payload)
		return
	}

	path, err := saveFile(s.opts.FileDir, name, body, size)
	if err != nil {
		slog.Warn("receive remote file", "err", err)
		return
	}
	if err := s.set(Content{MIME: MIMEURIList, Data: []byte(fileURI(path))}); err != nil {
		slog.Warn("set clipboard", "err", err)
		return
	}
	slog.Info("file received", "path", path, "bytes", size)
}

// PushRequested sends the contents announced to the peer, now that it asks
// for them.
func (s *Sync) PushRequested(postAction uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.offered == nil || s.peer == nil || s.pushing {
		slog.Debug("remote asked for clipboard, nothing to send", "busy", s.pushing)
		return
	}
	s.pushing = true
	go func(peer Peer, o offer) {
		s.push(peer, o, postAction)
		s.mu.Lock()
		s.pushing = false
		s.mu.Unlock()
	}(s.peer, *s.offered)
}

func (s *Sync) push(peer Peer, o offer, postAction uint32) {
	if o.path == "" {
		name := "text"
		if o.image {
			name = "image"
		}
		if err := peer.PushClipboard(postAction, name, int64(len(o.payload)), bytes.NewReader(o.payload)); err != nil {
			slog.Warn("send clipboard", "err", err)
			return
		}
		slog.Debug("clipboard sent", "name", name, "bytes", len(o.payload))
		return
	}

	// The file may have changed since it was copied.
	f, err := os.Open(o.path)
	if err != nil {
		slog.Warn("send file", "err", err)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		slog.Warn("send file", "err", err)
		return
	}
	if !s.sendable(o.path, fi) {
		return
	}
	if err := peer.PushClipboard(postAction, pushName(o.path), fi.Size(), f); err != nil {
		slog.Warn("send file", "path", o.path, "err", err)
		return
	}
	slog.Info("file sent", "path", o.path, "bytes", fi.Size())
}

// sendable reports whether the file at path can be sent to the peer.
func (s *Sync) sendable(path string, fi os.FileInfo) bool {
	switch {
	case fi.IsDir():
		slog.Info("folders cannot be sent, zip it first", "path", path)
	case !fi.Mode().IsRegular():
		slog.Info("only regular files can be sent", "path", path)
	case fi.Size() > s.opts.MaxTransferSize:
		slog.Info("file too large to send", "path", path, "bytes", fi.Size(), "max", s.opts.MaxTransferSize)
	default:
		return true
	}
	return false
}

// ApplyRemote sets the local clipboard from a payload received from the peer.
func (s *Sync) ApplyRemote(image bool, payload []byte) {
	var c Content
	if image {
		png, err := TrimPNG(payload)
		if err != nil {
			slog.Warn("ignoring remote clipboard image", "err", err)
			return
		}
		c = Content{MIME: MIMEPNG, Data: png}
	} else {
		text, err := DecodeText(payload)
		if err != nil {
			slog.Warn("ignoring remote clipboard text", "err", err)
			return
		}
		c = Content{MIME: MIMEText, Data: []byte(text)}
	}

	if err := s.set(c); err != nil {
		slog.Warn("set clipboard", "err", err)
		return
	}
	slog.Debug("clipboard received", "mime", c.MIME, "bytes", len(c.Data))
}

// set puts contents received from the peer in the local clipboard, replacing
// what was offered to the peer.
func (s *Sync) set(c Content) error {
	s.remember(c)
	s.mu.Lock()
	s.offered = nil
	s.mu.Unlock()
	return s.backend.Set(c)
}

// remember records c as synced and reports whether it differs from the
// previously synced contents.
func (s *Sync) remember(c Content) bool {
	h := hash(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	if h == s.last {
		return false
	}
	s.last = h
	return true
}

func (s *Sync) localChanged(c Content) {
	if c.Data == nil {
		s.mu.Lock()
		s.offered = nil
		s.mu.Unlock()
		return
	}
	if !s.remember(c) {
		return
	}

	s.mu.Lock()
	s.offered = nil
	peer := s.peer
	s.mu.Unlock()
	if peer == nil {
		return
	}

	if c.MIME == MIMEURIList {
		s.offerFile(peer, c.Data)
		return
	}

	image := c.MIME == MIMEPNG
	payload := c.Data
	if !image {
		var err error
		if payload, err = EncodeText(string(c.Data)); err != nil {
			slog.Warn("encode clipboard text", "err", err)
			return
		}
	}
	if len(payload) > MaxPayload {
		if int64(len(payload)) > s.opts.MaxTransferSize {
			slog.Info("clipboard too large to sync", "mime", c.MIME, "bytes", len(payload), "max", max(MaxPayload, s.opts.MaxTransferSize))
			return
		}
		s.announce(peer, offer{image: image, payload: payload})
		return
	}

	if err := peer.SendClipboard(image, payload); err != nil {
		slog.Warn("send clipboard", "err", err)
		return
	}
	slog.Debug("clipboard sent", "mime", c.MIME, "bytes", len(c.Data))
}

// offerFile announces the first of the files in a text/uri-list to the peer,
// as MWB sends only one file.
func (s *Sync) offerFile(peer Peer, uriList []byte) {
	paths := filePaths(uriList)
	if len(paths) == 0 {
		return
	}
	if s.opts.MaxTransferSize <= 0 {
		slog.Info("files copied, sending files is disabled")
		return
	}
	if len(paths) > 1 {
		slog.Info("only the first copied file is sent", "files", len(paths))
	}
	fi, err := os.Stat(paths[0])
	if err != nil {
		slog.Warn("send file", "err", err)
		return
	}
	if s.sendable(paths[0], fi) {
		s.announce(peer, offer{path: paths[0]})
	}
}

// announce offers o to the peer, which asks for it when the mouse moves to
// it within 30 seconds.
func (s *Sync) announce(peer Peer, o offer) {
	s.mu.Lock()
	s.offered = &o
	s.mu.Unlock()
	if err := peer.AnnounceClipboard(); err != nil {
		slog.Warn("announce clipboard", "err", err)
		return
	}
	if o.path != "" {
		slog.Debug("clipboard offered", "path", o.path)
	} else {
		slog.Debug("clipboard offered", "bytes", len(o.payload))
	}
}

// hash identifies clipboard contents by their data alone: wl-copy offers
// text/uri-list contents as plain text too, and both must match.
func hash(c Content) [sha256.Size]byte {
	return sha256.Sum256(c.Data)
}
