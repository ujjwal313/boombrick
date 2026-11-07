package helper

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	// Used only when the system has no CA certificates, as on the Brick.
	_ "golang.org/x/crypto/x509roots/fallback"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/audio"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"github.com/devgianlu/go-librespot/session"
	"github.com/devgianlu/go-librespot/spclient"

	"github.com/ujjwal313/boombrick/internal/spotify"
)

const (
	// retryOffline is how long a session that couldn't connect waits before
	// a request tries again.
	retryOffline = 10 * time.Second
	// keepFor is how long a stream handed back before its end is kept, and
	// keepMax how many: each holds what it has downloaded.
	keepFor = time.Minute
	keepMax = 2
	// Listing limits: rootlist entries per request, and the most pages and
	// tracks a list is read to.
	rootlistPage = 500
	maxPages     = 256
	maxTracks    = 10000
	// metaBatch is how many tracks one metadata request asks about.
	metaBatch = 100
	imageURL  = "https://i.scdn.co/image/"
)

// Librespot is the Session on go-librespot: one account, whose device ID and
// credentials are kept in dir/state.json.
type Librespot struct {
	log   *slog.Logger
	lg    librespot.Logger // log, as go-librespot takes it
	web   *http.Client
	store store
	ctx   context.Context // cancelled by Close, ending a connection under way
	stop  context.CancelFunc

	mu       sync.Mutex
	state    *librespot.AppState
	st       spotify.State
	login    *spotify.Login
	premium  bool
	free     bool // as premium, only once the access point has said so
	country  string
	name     string            // the account's display name, once looked up
	names    map[string]string // display names by user ID
	attempt  int               // bumped by every sign-in and sign-out and by Close, so a stale one is dropped
	cancel   context.CancelFunc
	retry    time.Time     // when an offline session may be tried again
	changed  chan struct{} // closed when st changes
	sess     *session.Session
	busy     map[string]*stream // streams being read, by key
	kept     []*stream          // streams let go early, kept for a seek or a return to the track
	keepTick *time.Timer

	// profile looks a user's display name up.
	profile func(ctx context.Context, sess *session.Session, user string) (string, error)
}

// NewLibrespot loads dir's state and, when it holds credentials, starts
// opening the session.
func NewLibrespot(dir string, log *slog.Logger) (*Librespot, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Librespot{
		log:     log,
		lg:      logger{log},
		web:     &http.Client{Timeout: 30 * time.Second},
		store:   store{filepath.Join(dir, "state.json")},
		ctx:     ctx,
		stop:    stop,
		st:      spotify.SignedOut,
		changed: make(chan struct{}),
		busy:    map[string]*stream{},
		names:   map[string]string{},
		profile: profileName,
	}
	var err error
	if s.state, err = s.store.Load(); err != nil {
		return nil, err
	}
	if s.state.DeviceId == "" {
		id := make([]byte, 20)
		rand.Read(id)
		s.state.DeviceId = hex.EncodeToString(id)
		if err := s.store.Save(s.state); err != nil {
			return nil, err
		}
	}
	if s.state.Credentials.Username != "" {
		s.st = spotify.Connecting
		go s.connect(s.attempt)
	}
	return s, nil
}

// Close ends the session, any sign-in and a connection under way.
func (s *Librespot) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stop()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.attempt++ // a session that opens all the same is closed
	s.dropLocked()
}

// Status is where the session is, with the account once signed in.
func (s *Librespot) Status() spotify.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := spotify.Status{State: s.st}
	switch s.st {
	case spotify.SigningIn:
		st.Login = s.login
	case spotify.SignedOut:
	default:
		st.User, st.Name, st.Premium = s.state.Credentials.Username, s.name, s.premium
	}
	return st
}

// Login starts a device sign-in, replacing the account, and returns its code
// once Spotify has issued it.
func (s *Librespot) Login(ctx context.Context) (spotify.Login, error) {
	type result struct {
		login spotify.Login
		err   error
	}
	first := make(chan result, 1)
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.dropLocked() // signing in again replaces the account
	s.attempt++
	n := s.attempt
	lctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.login = nil
	s.setLocked(spotify.SigningIn)
	opts := s.options(session.DeviceAuthCredentials{OnCode: func(c *session.DeviceAuthCode) {
		if c == nil {
			return
		}
		l := spotify.Login{URL: c.VerificationUrl, Code: c.UserCode, Expires: c.ExpiresAt}
		s.mu.Lock()
		if n == s.attempt {
			s.login = &l
		}
		s.mu.Unlock()
		select {
		case first <- result{login: l}:
		default:
		}
	}})
	s.mu.Unlock()

	go func() {
		// Waits until the code is approved, refused or expires.
		sess, err := session.NewSessionFromOptions(lctx, opts)
		s.mu.Lock()
		defer s.mu.Unlock()
		if n != s.attempt {
			if err == nil {
				sess.Close()
			}
			return // a newer sign-in or a sign-out came first
		}
		s.cancel, s.login = nil, nil
		if err != nil {
			s.log.Warn("sign in", "err", err)
			s.setLocked(spotify.SignedOut)
			select {
			case first <- result{err: classify(err)}:
			default:
			}
			return
		}
		s.state.Credentials.Username, s.state.Credentials.Data = sess.Username(), sess.StoredCredentials()
		if err := s.store.Save(s.state); err != nil {
			s.log.Error("save credentials", "err", err)
		}
		s.attachLocked(sess)
	}()

	select {
	case r := <-first:
		return r.login, r.err
	case <-ctx.Done():
		return spotify.Login{}, ctx.Err()
	}
}

// Logout ends the session and any sign-in, and forgets the credentials.
func (s *Librespot) Logout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.attempt++
	s.dropLocked()
	s.login, s.premium, s.free, s.name = nil, false, false, ""
	s.state.Credentials.Username, s.state.Credentials.Data = "", nil
	s.setLocked(spotify.SignedOut)
	return s.store.Save(s.state)
}

// Playlists lists the account's playlists, folders left out, with their
// owners' display names.
func (s *Librespot) Playlists(ctx context.Context) ([]spotify.Playlist, error) {
	sess, err := s.ready(ctx)
	if err != nil {
		return nil, err
	}
	var out []spotify.Playlist
	from := 0
	for range maxPages {
		list, err := sess.Spclient().Rootlist(ctx, sess.Username(), from, rootlistPage)
		if err != nil {
			return nil, classify(err)
		}
		c := list.GetContents()
		out = append(out, playlists(c.GetItems(), c.GetMetaItems())...)
		from += len(c.GetItems())
		if !c.GetTruncated() || len(c.GetItems()) == 0 {
			break
		}
	}
	for i := range out {
		out[i].Owner = s.nameOf(ctx, sess, out[i].Owner)
	}
	return out, nil
}

// nameOf is user's display name, looked up once and remembered; the ID
// itself when it can't be.
func (s *Librespot) nameOf(ctx context.Context, sess *session.Session, user string) string {
	s.mu.Lock()
	n, ok := s.names[user]
	s.mu.Unlock()
	if ok {
		return n
	}
	n, err := s.profile(ctx, sess, user)
	if err != nil {
		s.log.Warn("look up a name", "user", user, "err", err)
		return user
	}
	s.mu.Lock()
	s.names[user] = n
	s.mu.Unlock()
	return n
}

// profileName is user's display name, from their Spotify profile.
func profileName(ctx context.Context, sess *session.Session, user string) (string, error) {
	resp, err := sess.Spclient().Request(ctx, http.MethodGet, "/user-profile-view/v3/profile/"+user, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("profile: %s", resp.Status)
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return "", err
	}
	if p.Name == "" {
		return "", errors.New("profile: no name")
	}
	return p.Name, nil
}

// Tracks lists the tracks of the playlist or collection at uri, up to
// maxTracks.
func (s *Librespot) Tracks(ctx context.Context, uri string) ([]spotify.Track, error) {
	sess, err := s.ready(ctx)
	if err != nil {
		return nil, err
	}
	sp := sess.Spclient()
	r, err := spclient.NewContextResolver(ctx, s.lg, sp, &connectpb.Context{Uri: uri})
	if err != nil {
		return nil, classify(err)
	}
	var uris []string
	for page := 0; page < maxPages && len(uris) < maxTracks; page++ {
		items, err := r.Page(ctx, page)
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, classify(err)
		}
		for _, it := range items {
			if u := trackURI(r.Type(), it); u != "" {
				uris = append(uris, u)
			}
		}
	}
	meta := make(map[string]*metadatapb.Track, len(uris))
	for batch := range slices.Chunk(uris, metaBatch) {
		req := &extmetadatapb.BatchedEntityRequest{}
		for _, u := range batch {
			req.EntityRequest = append(req.EntityRequest, &extmetadatapb.EntityRequest{
				EntityUri: u,
				Query:     []*extmetadatapb.ExtensionQuery{{ExtensionKind: extmetadatapb.ExtensionKind_TRACK_V4}},
			})
		}
		resp, err := sp.ExtendedMetadata(ctx, req)
		if err != nil {
			return nil, classify(err)
		}
		for _, item := range resp.GetExtendedMetadata() {
			for _, d := range item.GetExtensionData() {
				if d.GetHeader().GetStatusCode() != 200 || d.GetExtensionData() == nil {
					continue
				}
				var t metadatapb.Track
				if d.GetExtensionData().UnmarshalTo(&t) == nil {
					meta[d.GetEntityUri()] = &t
				}
			}
		}
	}
	out := make([]spotify.Track, 0, len(uris))
	for _, u := range uris {
		if t := meta[u]; t != nil {
			out = append(out, track(strings.TrimPrefix(u, "spotify:track:"), t))
		}
	}
	return out, nil
}

// Stream opens track id from ms, taking over the stream of it already open
// at kbps and level when there is one.
func (s *Librespot) Stream(ctx context.Context, id string, ms int64, kbps int, level bool) (Stream, error) {
	key := fmt.Sprintf("%s/%d/%t", id, kbps, level)
	if t := s.claim(ctx, key); t != nil {
		if err := t.src.SeekMs(ms); err == nil {
			return t, nil
		}
		s.forget(t)
	}
	sess, err := s.ready(ctx)
	if err != nil {
		return nil, err
	}
	sid, err := librespot.SpotifyIdFromBase62(librespot.SpotifyIdTypeTrack, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", spotify.ErrUnavailable, err)
	}
	if s.freeOn(sess) {
		return nil, spotify.ErrPremium // not worth asking for a key
	}
	s.log.Info("open stream", "id", id, "kbps", kbps)
	o, err := s.open(ctx, sess, *sid, kbps, level, ms)
	if err != nil {
		s.log.Warn("open stream", "id", id, "err", err)
		return nil, refused(err, s.freeOn(sess))
	}
	return s.adopt(key, o, o.duration, sess), nil
}

// ready waits for the session, opening it again if it was offline and has
// waited long enough.
func (s *Librespot) ready(ctx context.Context) (*session.Session, error) {
	for {
		s.mu.Lock()
		if s.st == spotify.Offline && time.Now().After(s.retry) {
			s.setLocked(spotify.Connecting)
			go s.connect(s.attempt)
		}
		sess, st, changed := s.sess, s.st, s.changed
		s.mu.Unlock()
		switch {
		case sess != nil:
			return sess, nil
		case st == spotify.SignedOut || st == spotify.SigningIn:
			return nil, spotify.ErrSignedOut
		case st == spotify.Offline:
			return nil, spotify.ErrOffline
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// connect opens the session with the stored credentials, unless a sign-in,
// sign-out or Close after attempt n got there first.
func (s *Librespot) connect(n int) {
	s.mu.Lock()
	opts := s.options(session.StoredCredentials{Username: s.state.Credentials.Username, Data: s.state.Credentials.Data})
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	sess, err := session.NewSessionFromOptions(ctx, opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n != s.attempt {
		if err == nil {
			sess.Close()
		}
		return
	}
	if err != nil {
		s.log.Warn("connect", "err", err)
		if errors.Is(classify(err), spotify.ErrSignedOut) {
			s.state.Credentials.Username, s.state.Credentials.Data = "", nil
			s.store.Save(s.state)
			s.setLocked(spotify.SignedOut)
			return
		}
		s.retry = time.Now().Add(retryOffline)
		s.setLocked(spotify.Offline)
		return
	}
	s.attachLocked(sess)
}

// attachLocked makes sess the session, looks the account's name up, and
// listens for what the access point says about the account: only packets
// that arrive after this are heard.
func (s *Librespot) attachLocked(sess *session.Session) {
	s.sess, s.premium, s.free, s.country = sess, false, false, ""
	s.setLocked(spotify.Ready)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n := s.nameOf(ctx, sess, sess.Username())
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.sess == sess {
			s.name = n
		}
	}()
	recv := sess.Accesspoint().Receive(ap.PacketTypeProductInfo, ap.PacketTypeCountryCode)
	go func() {
		for pkt := range recv {
			s.mu.Lock()
			if s.sess == sess {
				switch pkt.Type {
				case ap.PacketTypeProductInfo:
					typ := productType(pkt.Payload)
					s.premium, s.free = typ == "premium", typ == "free"
				case ap.PacketTypeCountryCode:
					s.country = string(pkt.Payload)
				}
			}
			s.mu.Unlock()
		}
		// The access point gave up reconnecting: the session is gone.
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.sess == sess {
			s.log.Warn("session lost")
			s.dropLocked()
			s.retry = time.Now()
			s.setLocked(spotify.Offline)
		}
	}()
}

// dropLocked closes the session and the streams kept. Those being read are
// closed as their requests let them go.
func (s *Librespot) dropLocked() {
	go closeAll(s.kept)
	s.kept, s.busy = nil, map[string]*stream{}
	if s.sess != nil {
		s.sess.Close()
		s.sess = nil
	}
}

func (s *Librespot) setLocked(st spotify.State) {
	s.st = st
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Librespot) options(creds any) *session.Options {
	return &session.Options{
		Log:         s.lg,
		DeviceType:  devicespb.DeviceType_SPEAKER,
		DeviceId:    s.state.DeviceId,
		Credentials: creds,
		Client:      s.web,
		StateStore:  s.store,
	}
}

// source is an open track's audio: what open opens, or a test's.
type source interface {
	Read(p []float32) (int, error)
	SeekMs(ms int64) error
	Close() error
}

// stream is an open track, closed once it is let go for good.
type stream struct {
	s        *Librespot
	sess     *session.Session // the session it was opened in
	key      string           // the track, quality and levelling it was opened with
	src      source
	duration int64 // ms
	until    time.Time
	ended    bool          // read to its end: it holds the whole file by now
	taken    atomic.Bool   // a newer request wants it: reading stops
	free     chan struct{} // closed once the request reading it lets it go
}

func (t *stream) SampleRate() int { return spotify.Rate }
func (t *stream) Duration() int64 { return t.duration }
func (t *stream) Release()        { t.s.release(t) }

func (t *stream) Read(p []float32) (int, error) {
	if t.taken.Load() {
		return 0, errTaken
	}
	n, err := t.src.Read(p)
	if errors.Is(err, io.EOF) {
		t.ended = true
	}
	return n, err
}

// closeAll closes streams let go, off the lock: closing one waits for its
// download to stop.
func closeAll(ts []*stream) {
	for _, t := range ts {
		t.src.Close()
	}
}

// adopt makes src, opened in sess, a stream being read under key.
func (s *Librespot) adopt(key string, src source, duration int64, sess *session.Session) *stream {
	t := &stream{s: s, sess: sess, key: key, src: src, duration: duration, free: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy[key] = t
	return t
}

// claim returns the stream open under key, if there is one: one kept, or one
// being read, which is taken from its reader. A seek asks again before the
// request it replaces has let go, and a new stream would mean a new key.
func (s *Librespot) claim(ctx context.Context, key string) *stream {
	s.mu.Lock()
	if t := s.busy[key]; t != nil {
		t.taken.Store(true)
		free := t.free
		s.mu.Unlock()
		select {
		case <-free:
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
			return nil
		}
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	for i, t := range s.kept {
		if t.key == key {
			s.kept = slices.Delete(s.kept, i, i+1)
			t.taken.Store(false)
			t.free = make(chan struct{})
			s.busy[key] = t
			return t
		}
	}
	return nil
}

// forget lets t go without keeping it.
func (s *Librespot) forget(t *stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[t.key] == t {
		delete(s.busy, t.key)
	}
	close(t.free)
	go closeAll([]*stream{t})
}

// release takes t back from its reader and, while its session lasts, keeps
// it for keepFor, letting the oldest go beyond keepMax. One read to its end
// is let go: only a seek back could use it, and it holds the whole file.
func (s *Librespot) release(t *stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(t.free)
	if s.busy[t.key] == t {
		delete(s.busy, t.key)
	}
	if t.ended || t.sess != s.sess {
		go closeAll([]*stream{t})
		return
	}
	t.until = time.Now().Add(keepFor)
	s.kept = append(s.kept, t)
	if len(s.kept) > keepMax {
		go closeAll([]*stream{s.kept[0]})
		s.kept = s.kept[1:]
	}
	if s.keepTick == nil {
		s.keepTick = time.AfterFunc(keepFor, s.expire)
	}
}

// expire lets the streams kept past their time go.
func (s *Librespot) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keepTick = nil
	now := time.Now()
	var left, gone []*stream
	for _, t := range s.kept {
		if now.After(t.until) {
			gone = append(gone, t)
		} else {
			left = append(left, t)
		}
	}
	go closeAll(gone)
	s.kept = left
	if len(left) > 0 {
		s.keepTick = time.AfterFunc(time.Until(left[0].until), s.expire)
	}
}

// freeOn reports whether the access point has said sess's account is free;
// false once sess has been replaced.
func (s *Librespot) freeOn(sess *session.Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sess == sess && s.free
}

// refused is why a stream didn't open, on a free account if free is set. A
// refused key asks for a wait, except on a free account: Spotify gives keys to
// Premium alone, so waiting would never end.
func refused(err error, free bool) error {
	var key *audio.KeyProviderError
	if free && errors.As(err, &key) {
		return fmt.Errorf("%w: %w", spotify.ErrPremium, err)
	}
	return classify(err)
}

// classify turns go-librespot's errors into the helper's.
func classify(err error) error {
	var (
		key   *audio.KeyProviderError
		limit *spclient.RateLimitedError
		login *ap.AccesspointLoginError
		cert  *tls.CertificateVerificationError
		nerr  net.Error
	)
	switch {
	case errors.Is(err, librespot.ErrMediaRestricted), errors.Is(err, librespot.ErrNoSupportedFormats):
		return fmt.Errorf("%w: %w", spotify.ErrUnavailable, err)
	case errors.As(err, &key):
		return &spotify.Throttled{Wait: spotify.KeyWait}
	case errors.As(err, &limit):
		return &spotify.Throttled{Wait: limit.RetryAfter}
	case errors.As(err, &login):
		return fmt.Errorf("%w: %w", spotify.ErrSignedOut, err)
	case errors.As(err, &cert):
		return err // inside a network error, but the network is there
	case errors.As(err, &nerr):
		return fmt.Errorf("%w: %w", spotify.ErrOffline, err)
	}
	return err
}

// playlists turns rootlist entries into playlists; folders are left out.
func playlists(items []*playlist4pb.Item, metas []*playlist4pb.MetaItem) []spotify.Playlist {
	var out []spotify.Playlist
	for i, it := range items {
		if !strings.HasPrefix(it.GetUri(), "spotify:playlist:") || i >= len(metas) {
			continue
		}
		m, a := metas[i], metas[i].GetAttributes()
		out = append(out, spotify.Playlist{
			URI:   it.GetUri(),
			Name:  a.GetName(),
			Owner: m.GetOwnerUsername(),
			Count: int(m.GetLength()),
			Cover: playlistCover(a),
		})
	}
	return out
}

// playlistCover is the picture Spotify names "default", any other it has a
// link for, or one built from the picture's file ID.
func playlistCover(a *playlist4pb.ListAttributes) string {
	var first string
	for _, size := range a.GetPictureSize() {
		switch {
		case size.GetUrl() == "":
		case size.GetTargetName() == "default":
			return size.GetUrl()
		case first == "":
			first = size.GetUrl()
		}
	}
	if first == "" && len(a.GetPicture()) > 0 {
		first = imageURL + hex.EncodeToString(a.GetPicture())
	}
	return first
}

// trackURI names a context's entry if it is a track, from its URI or its
// GID.
func trackURI(typ librespot.SpotifyIdType, t *connectpb.ContextTrack) string {
	uri := t.GetUri()
	if uri == "" && len(t.GetGid()) == 16 {
		uri = librespot.SpotifyIdFromGid(typ, t.GetGid()).Uri()
	}
	if !strings.HasPrefix(uri, "spotify:track:") {
		return ""
	}
	return uri
}

// track is a track's metadata as the helper lists it.
func track(id string, t *metadatapb.Track) spotify.Track {
	out := spotify.Track{ID: id, Title: t.GetName(), Album: t.GetAlbum().GetName(), Duration: int64(t.GetDuration())}
	for _, a := range t.GetArtist() {
		out.Artists = append(out.Artists, a.GetName())
	}
	if as := t.GetAlbum().GetArtist(); len(as) > 0 {
		out.AlbumArtist = as[0].GetName()
	}
	for _, x := range t.GetExternalId() {
		if strings.EqualFold(x.GetType(), "isrc") {
			out.ISRC = x.GetId()
		}
	}
	out.Cover = cover(t.GetAlbum().GetCoverGroup().GetImage())
	return out
}

// cover is the link to the large picture of images, or the closest there is.
func cover(images []*metadatapb.Image) string {
	var best *metadatapb.Image
	rank := map[metadatapb.Image_Size]int{metadatapb.Image_LARGE: 4, metadatapb.Image_XLARGE: 3, metadatapb.Image_DEFAULT: 2, metadatapb.Image_SMALL: 1}
	for _, im := range images {
		if len(im.GetFileId()) > 0 && (best == nil || rank[im.GetSize()] > rank[best.GetSize()]) {
			best = im
		}
	}
	if best == nil {
		return ""
	}
	return imageURL + hex.EncodeToString(best.GetFileId())
}

// productType is the account's type ("premium", "free") from the access
// point's product info, a small XML document: its first <type>.
func productType(payload []byte) string {
	_, rest, ok := bytes.Cut(payload, []byte("<type>"))
	if !ok {
		return ""
	}
	typ, _, ok := bytes.Cut(rest, []byte("</type>"))
	if !ok {
		return ""
	}
	return string(bytes.TrimSpace(typ))
}

// store keeps go-librespot's state, the device ID and credentials, in a file
// only its owner can read.
type store struct{ path string }

func (f store) Load() (*librespot.AppState, error) {
	st := &librespot.AppState{}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	} else if err != nil {
		return nil, err
	}
	return st, json.Unmarshal(b, st)
}

func (f store) Save(st *librespot.AppState) error {
	st.Lock()
	b, err := json.Marshal(st)
	st.Unlock()
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// logger is go-librespot's Logger on slog. Its info lines go to debug, as
// they come with every track; warnings and errors are kept.
type logger struct{ l *slog.Logger }

func (g logger) Tracef(string, ...any)                {}
func (g logger) Debugf(string, ...any)                {}
func (g logger) Infof(f string, a ...any)             { g.l.Debug(fmt.Sprintf(f, a...)) }
func (g logger) Warnf(f string, a ...any)             { g.l.Warn(fmt.Sprintf(f, a...)) }
func (g logger) Errorf(f string, a ...any)            { g.l.Error(fmt.Sprintf(f, a...)) }
func (g logger) Trace(...any)                         {}
func (g logger) Debug(...any)                         {}
func (g logger) Info(a ...any)                        { g.l.Debug(fmt.Sprint(a...)) }
func (g logger) Warn(a ...any)                        { g.l.Warn(fmt.Sprint(a...)) }
func (g logger) Error(a ...any)                       { g.l.Error(fmt.Sprint(a...)) }
func (g logger) WithError(err error) librespot.Logger { return logger{g.l.With("err", err)} }
func (g logger) WithField(k string, v any) librespot.Logger {
	return logger{g.l.With(k, v)}
}
