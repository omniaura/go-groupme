package groupme

import (
	"context"
	"io"
	"sync"
)

// PreparedVideoUpload holds one process-local upload capability. It exposes
// only its public attachment; signed targets and readers cannot be serialized.
type PreparedVideoUpload struct {
	state *preparedVideoState
}

type preparedVideoState struct {
	reader  io.Reader
	cleanup func()
	target  UploadTarget
	mu      sync.Mutex
	used    bool
}

func (PreparedVideoUpload) String() string   { return "GroupMe prepared video upload" }
func (PreparedVideoUpload) GoString() string { return "GroupMe prepared video upload" }

// MarshalJSON deliberately excludes every process-local capability field.
func (PreparedVideoUpload) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Attachment returns the exact public URLs learned before any body transfer.
func (p *PreparedVideoUpload) Attachment() Attachment {
	if p == nil || p.state == nil {
		return Attachment{}
	}
	s := p.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return Attachment{Type: "video", URL: s.target.RenderURL, PreviewURL: s.target.ThumbnailURL}
}

// Close releases only owned temporary data, never the caller's reader.
func (p *PreparedVideoUpload) Close() {
	if p == nil || p.state == nil {
		return
	}
	s := p.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cleanup != nil {
		s.cleanup()
		s.cleanup = nil
	}
	s.used = true
	s.reader = nil
	s.target.uploadURL = ""
}

// PrepareVideoUpload validates exact bytes and initializes one MP4 target.
// Callers may validate all returned public URLs before transferring any body.
func (c *Client) PrepareVideoUpload(ctx context.Context, cid, senderID string, in UploadInput) (*PreparedVideoUpload, error) {
	if !ValidConversationID(cid) || !ValidID(senderID) || in.ContentType != "video/mp4" || in.Data == nil || in.Size < 1 || in.Size > MaxUploadBytes || !validFileName(in.Name) {
		return nil, ErrInvalid.New(ctx)
	}
	reader, cleanup, err := prepareMP4Reader(ctx, in.Data, in.Size)
	if err != nil {
		return nil, err
	}
	width, height := mp4Dimensions(reader, in.Size)
	if width < 1 || height < 1 {
		cleanup()
		return nil, ErrInvalid.New(ctx)
	}
	target, err := c.InitializeUpload(ctx, InitializeUploadInput{Extension: "mp4", SenderID: senderID, GroupID: cid, FileSize: in.Size, Width: width, Height: height})
	if err != nil {
		cleanup()
		return nil, err
	}
	if !validVideoURL(target.RenderURL) || !validImageURL(target.ThumbnailURL) {
		cleanup()
		return nil, ErrUncertain.New(ctx)
	}
	return &PreparedVideoUpload{state: &preparedVideoState{target: target, reader: reader, cleanup: cleanup}}, nil
}

// UploadPreparedVideo consumes a capability once, including uncertain writes.
func (c *Client) UploadPreparedVideo(ctx context.Context, prepared *PreparedVideoUpload) (Attachment, error) {
	if prepared == nil || prepared.state == nil {
		return Attachment{}, ErrInvalid.New(ctx)
	}
	s := prepared.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used || s.reader == nil {
		return Attachment{}, ErrInvalid.New(ctx)
	}
	s.used = true
	if err := c.Upload(ctx, s.target, "video/mp4", s.reader, s.target.size); err != nil {
		return Attachment{}, err
	}
	return Attachment{Type: "video", URL: s.target.RenderURL, PreviewURL: s.target.ThumbnailURL}, nil
}
