package groupme

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omniaura/go-kit/errs"
	"github.com/omniaura/go-kit/net/hit"
)

// MaxUploadBytes is a conservative SDK streaming bound, not a provider limit.
const MaxUploadBytes int64 = 50 << 20

// MediaURL admits only observed public GroupMe media origins. Avatars continue
// to use ImageURL, whose i.groupme.com-only contract is intentionally narrower.
func MediaURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(u.Path) < 2 || strings.Contains(u.Path, "\\") {
		return ""
	}
	for p := range strings.SplitSeq(u.Path, "/") {
		if p == "." || p == ".." {
			return ""
		}
	}
	switch u.Host {
	case "i.groupme.com", "v.groupme.com":
		return raw
	case "m.groupme.com":
		if strings.HasPrefix(u.Path, "/uploads/") && len(u.Path) > len("/uploads/") {
			return raw
		}
	}
	return ""
}

type UploadInput struct {
	Data        io.Reader
	Name        string
	ContentType string
	Size        int64
}

func validUploadURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 16384 || u.Scheme != "https" || u.Host != "cdn2.groupme.com" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/uploads/") || len(u.Path) <= len("/uploads/") || strings.Contains(u.Path, "\\") {
		return false
	}
	for p := range strings.SplitSeq(u.Path, "/") {
		if p == "." || p == ".." {
			return false
		}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || q.Get("sig") == "" {
		return false
	}
	allowed := map[string]bool{"skoid": true, "sktid": true, "skt": true, "ske": true, "sks": true, "skv": true, "sv": true, "se": true, "sr": true, "sp": true, "rsct": true, "sig": true, "st": true, "spr": true}
	for k, vs := range q {
		if !allowed[k] || len(vs) != 1 || len(vs[0]) > 2048 || k == "spr" && vs[0] != "https" {
			return false
		}
	}
	return true
}

func (c *Client) InitializeUpload(ctx context.Context, in InitializeUploadInput) (UploadTarget, error) {
	if !ValidConversationID(in.GroupID) || !ValidID(in.SenderID) || imageContentType(in.Extension) == "" || in.FileSize < 1 || in.FileSize > MaxUploadBytes || in.Width < 1 || in.Height < 1 || in.Width > 16384 || in.Height > 16384 {
		return UploadTarget{}, ErrInvalid.New(ctx)
	}
	if !ValidToken(c.token) {
		return UploadTarget{}, ErrAuthentication.New(ctx)
	}
	api := hit.NewClient[envelope]("https://m.groupme.com").Service("groupme").HTTPClient(c.httpClient).Timeout(20*time.Second).Retry(errs.NeverRetry()).Header("Accept", "application/json")
	if c.browserUserAgent != "" {
		api = api.Header("User-Agent", c.browserUserAgent)
	}

	var wire struct {
		UploadURL     string `json:"uploadUrl"`
		RenderURL     string `json:"renderUrl"`
		ThumbnailURL  string `json:"thumbnailUrl"`
		TranscriptURL string `json:"transcriptUrl"`
	}
	if err := api.POST("/uploads").Header("X-Access-Token", c.token).Body(&in).Do(ctx, &wire); err != nil {
		return UploadTarget{}, failure(ctx, err, true)
	}
	if !validUploadURL(wire.UploadURL) || MediaURL(wire.RenderURL) == "" || (wire.ThumbnailURL != "" && MediaURL(wire.ThumbnailURL) == "") || (wire.TranscriptURL != "" && MediaURL(wire.TranscriptURL) == "") {
		return UploadTarget{}, ErrUncertain.New(ctx)
	}
	if in.Extension != "mp4" && !validImageURL(wire.RenderURL) {
		return UploadTarget{}, ErrUncertain.New(ctx)
	}
	return UploadTarget{RenderURL: wire.RenderURL, ThumbnailURL: wire.ThumbnailURL, TranscriptURL: wire.TranscriptURL, uploadURL: wire.UploadURL, owner: sha256.Sum256([]byte(c.token)), size: in.FileSize, contentType: imageContentType(in.Extension)}, nil
}

// Upload sends only bytes to the short-lived, validated Azure SAS destination.
// It deliberately does not copy GroupMe credentials or browser headers there.
func (c *Client) Upload(ctx context.Context, target UploadTarget, contentType string, body io.Reader, size int64) error {
	if body == nil || size < 1 || size > MaxUploadBytes || size != target.size || target.uploadURL == "" || target.owner != sha256.Sum256([]byte(c.token)) || !validUploadURL(target.uploadURL) || contentType != target.contentType {
		return ErrInvalid.New(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.uploadURL, io.LimitReader(body, size+1))
	if err != nil {
		return ErrInvalid.New(ctx)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	// A SAS capability must never become a redirect Referer, even if the
	// configured client has a permissive policy. Preserve its transport/timeout.
	uploadClient := *c.httpClient
	uploadClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := uploadClient.Do(req)
	if err != nil {
		return ErrUncertain.New(ctx)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			slog.DebugContext(ctx, "GroupMe response body close failed")
		}
	}()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return statusFailure(ctx, response.StatusCode, true)
	}
	if _, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4097)); err != nil {
		return ErrUncertain.New(ctx)
	}
	return nil
}

// UploadAttachment handles supported images and MP4 without a generic-file
// fallback. Generic files must use UploadFile and retain its job before calling
// FileUploadStatus, so asynchronous uploads cannot lose their receipt here.
func (c *Client) UploadAttachment(ctx context.Context, cid, senderID string, in UploadInput) (Attachment, error) {
	if !ValidConversationID(cid) || !ValidID(senderID) || in.Data == nil || in.Size < 1 || in.Size > MaxUploadBytes || !validFileName(in.Name) {
		return Attachment{}, ErrInvalid.New(ctx)
	}
	if in.ContentType == "video/mp4" {
		prepared, err := c.PrepareVideoUpload(ctx, cid, senderID, in)
		if err != nil {
			return Attachment{}, err
		}
		defer prepared.Close()
		return c.UploadPreparedVideo(ctx, prepared)
	}
	ext := imageExtension(in.ContentType)
	if ext == "" {
		return Attachment{}, ErrUnsupported.New(ctx)
	}
	reader, cleanup, spoolErr := spoolUploadReader(ctx, in.Data, in.Size, "ditto-groupme-sdk-image-*")
	if spoolErr != nil {
		return Attachment{}, spoolErr
	}
	defer cleanup()
	var width, height int
	if ext == "webp" {
		header := make([]byte, 30)
		n, err := io.ReadFull(reader, header)
		if err != nil && err != io.ErrUnexpectedEOF {
			return Attachment{}, ErrInvalid.New(ctx)
		}
		width, height = webpDimensions(header[:n])
	} else {
		config, format, err := image.DecodeConfig(reader)
		if err != nil || imageExtension("image/"+format) != ext {
			return Attachment{}, ErrInvalid.New(ctx)
		}
		width, height = config.Width, config.Height
	}
	if width < 1 || height < 1 {
		return Attachment{}, ErrInvalid.New(ctx)
	}
	seeker, ok := reader.(io.Seeker)
	if !ok {
		return Attachment{}, ErrInvalid.New(ctx)
	}
	if offset, resetErr := seeker.Seek(0, io.SeekStart); resetErr != nil || offset != 0 {
		return Attachment{}, ErrInvalid.New(ctx)
	}
	target, err := c.InitializeUpload(ctx, InitializeUploadInput{Extension: ext, SenderID: senderID, GroupID: cid, FileSize: in.Size, Width: width, Height: height})
	if err != nil {
		return Attachment{}, err
	}
	if err = c.Upload(ctx, target, in.ContentType, reader, in.Size); err != nil {
		return Attachment{}, err
	}
	return Attachment{Type: "image", URL: target.RenderURL}, nil
}

// SupportsMediaUpload reports formats handled by the direct media upload flow.
// Other formats must use the split file-upload/job-status flow when a caller
// needs durable pending-job reconciliation, even if their MIME starts image/.
func SupportsMediaUpload(contentType string) bool {
	return imageExtension(contentType) != "" || contentType == "video/mp4"
}

func imageExtension(kind string) string {
	switch kind {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	}
	return ""
}
func imageContentType(ext string) string {
	switch ext {
	case "mp4":
		return "video/mp4"
	case "png":
		return "image/png"
	case "jpg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	}
	return ""
}

func webpDimensions(b []byte) (int, int) {
	if len(b) < 25 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(b[12:16]) {
	case "VP8X":
		if len(b) >= 30 {
			return int(b[24]) + (int(b[25]) << 8) + (int(b[26]) << 16) + 1, int(b[27]) + (int(b[28]) << 8) + (int(b[29]) << 16) + 1
		}
	case "VP8L":
		if b[20] == 0x2f {
			v := binary.LittleEndian.Uint32(b[21:25])
			return int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1
		}
	case "VP8 ":
		if len(b) >= 30 && bytes.Equal(b[23:26], []byte{0x9d, 0x01, 0x2a}) {
			return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
		}
	}
	return 0, 0
}

// FileInfo never contains a download URL or credentials. Size is the number of
// bytes copied to the caller's owned writer after a successful bounded read.
type FileInfo struct {
	FileID      string `json:"file_id"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

type FileUpload struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	FileID string `json:"file_id,omitempty"`
}

func validFileName(name string) bool {
	if !utf8.ValidString(name) || len(name) < 1 || len(name) > 255 || strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, v := range name {
		if v < 0x20 || v == 0x7f {
			return false
		}
	}
	return true
}

// fileHTTPClient keeps timeout and transport identity, replaces the JSON-only
// response cap, and independently forbids credential-bearing redirects.
func (c *Client) fileHTTPClient() *http.Client {
	h := *c.httpClient
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if t, ok := h.Transport.(boundedTransport); ok {
		h.Transport = t.base
	}
	return &h
}

type fileRequestOptions struct {
	Query       url.Values
	Body        io.Reader
	Method      string
	Route       string
	ContentType string
	Size        int64
	Mutation    bool
}

func (c *Client) fileRequest(ctx context.Context, options fileRequestOptions) (*http.Response, error) {
	method, route, q := options.Method, options.Route, options.Query
	body, size, contentType, mutation := options.Body, options.Size, options.ContentType, options.Mutation
	if !ValidToken(c.token) {
		return nil, ErrAuthentication.New(ctx)
	}
	u := url.URL{Scheme: "https", Host: "file.groupme.com", Path: route, RawQuery: q.Encode()}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, ErrInvalid.New(ctx)
	}
	req.Header.Set("X-Access-Token", c.token)
	if c.browserUserAgent != "" {
		req.Header.Set("User-Agent", c.browserUserAgent)
	}
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.fileHTTPClient().Do(req)
	if err != nil {
		return nil, statusFailure(ctx, 0, mutation)
	}
	if resp.StatusCode != http.StatusOK && (!mutation || method != http.MethodPost || resp.StatusCode != http.StatusCreated) {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.DebugContext(ctx, "GroupMe response body close failed")
		}
		return nil, statusFailure(ctx, resp.StatusCode, mutation)
	}
	return resp, nil
}

// UploadFile uses the observed raw text/plain upload, with a research-supported
// status_url receipt whose native response body still needs paired live QA.
// It performs exactly one POST. Reconcile its returned job using FileUploadStatus.
func (c *Client) UploadFile(ctx context.Context, cid string, in UploadInput) (FileUpload, error) {
	if !ValidConversationID(cid) || in.Data == nil || !validFileName(in.Name) || in.Size < 1 || in.Size > MaxUploadBytes {
		return FileUpload{}, ErrInvalid.New(ctx)
	}
	reader, cleanup, err := spoolUploadReader(ctx, in.Data, in.Size, "ditto-groupme-sdk-file-*")
	if err != nil {
		return FileUpload{}, err
	}
	defer cleanup()
	prefix := make([]byte, 512)
	n, readErr := io.ReadFull(reader, prefix)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return FileUpload{}, ErrInvalid.New(ctx)
	}
	kind := safeFileContentType(in.ContentType, prefix[:n])
	if kind == "" {
		return FileUpload{}, ErrInvalid.New(ctx)
	}
	resp, err := c.fileRequest(ctx, fileRequestOptions{Method: http.MethodPost, Route: "/v1/" + cid + "/files", Query: url.Values{"name": {in.Name}}, Body: io.MultiReader(bytes.NewReader(prefix[:n]), reader), Size: in.Size, ContentType: kind, Mutation: true})
	if err != nil {
		return FileUpload{}, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.DebugContext(ctx, "GroupMe response body close failed")
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return FileUpload{}, ErrUncertain.New(ctx)
	}

	var out struct {
		StatusURL string `json:"status_url"`
	}
	if json.Unmarshal(body, &out) != nil {
		return FileUpload{}, ErrUncertain.New(ctx)
	}
	job := fileStatusJob(out.StatusURL, cid)
	if job == "" {
		return FileUpload{}, ErrUncertain.New(ctx)
	}
	return FileUpload{JobID: job, Status: "pending"}, nil
}

func fileStatusJob(raw, cid string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host != "file.groupme.com" || u.User != nil || u.Fragment != "" || u.Path != "/v1/"+cid+"/uploadStatus" || u.RawPath != "" {
		return ""
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["job"]) != 1 || !ValidID(q.Get("job")) {
		return ""
	}
	return q.Get("job")
}

func (c *Client) FileUploadStatus(ctx context.Context, cid, jobID string) (FileUpload, error) {
	if !ValidConversationID(cid) || !ValidID(jobID) {
		return FileUpload{}, ErrInvalid.New(ctx)
	}
	resp, err := c.fileRequest(ctx, fileRequestOptions{Method: http.MethodGet, Route: "/v1/" + cid + "/uploadStatus", Query: url.Values{"job": {jobID}, "cnt": {"0"}}})
	if err != nil {
		return FileUpload{}, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.DebugContext(ctx, "GroupMe response body close failed")
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return FileUpload{}, ErrUnavailable.New(ctx)
	}

	var out struct {
		Status string `json:"status"`
		FileID string `json:"file_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF {
		return FileUpload{}, ErrUnavailable.New(ctx)
	}
	switch out.Status {
	case "pending", "processing":
		if out.FileID != "" {
			return FileUpload{}, ErrUnavailable.New(ctx)
		}
	case "completed":
		if !ValidID(out.FileID) {
			return FileUpload{}, ErrUnavailable.New(ctx)
		}
	default:
		return FileUpload{}, ErrUnavailable.New(ctx)
	}
	return FileUpload{JobID: jobID, Status: out.Status, FileID: out.FileID}, nil
}

// DownloadFile requests the fixed authenticated route with a header, never a
// credential-bearing URL. Header authentication needs live provider verification.
func (c *Client) DownloadFile(ctx context.Context, cid, fileID string, dst io.Writer) (FileInfo, error) {
	if !ValidConversationID(cid) || !ValidID(fileID) || dst == nil {
		return FileInfo{}, ErrInvalid.New(ctx)
	}
	resp, err := c.fileRequest(ctx, fileRequestOptions{Method: http.MethodGet, Route: "/v1/" + cid + "/files/" + fileID})
	if err != nil {
		return FileInfo{}, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.DebugContext(ctx, "GroupMe response body close failed")
		}
	}()
	if resp.ContentLength > MaxUploadBytes {
		return FileInfo{}, ErrUnavailable.New(ctx)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "application/octet-stream"
	}
	info := FileInfo{FileID: fileID, ContentType: downloadContentType(mediaType)}
	if _, params, e := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); e == nil && validFileName(params["filename"]) {
		info.Name = params["filename"]
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, MaxUploadBytes))
	if err != nil {
		return FileInfo{}, ErrUnavailable.New(ctx)
	}
	var extra [1]byte
	more, readErr := resp.Body.Read(extra[:])
	if more != 0 || (readErr != nil && readErr != io.EOF) || (resp.ContentLength >= 0 && n != resp.ContentLength) {
		return FileInfo{}, ErrUnavailable.New(ctx)
	}
	info.Size = n
	return info, nil
}

// Active content is uploaded as a downloadable binary file, never an inline
// image or caller-controlled HTML content type. Header sniffing bounds the input
// type while allowing standard document/archive types with binary signatures.
func safeFileContentType(declared string, header []byte) string {
	kind, _, err := mime.ParseMediaType(declared)
	if err != nil || len(kind) > 128 || len(header) == 0 {
		return ""
	}
	sniff, _, _ := strings.Cut(http.DetectContentType(header), ";")
	switch kind {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "application/javascript", "text/javascript", "text/xml", "application/xml":
		return "application/octet-stream"
	}
	if kind == "application/octet-stream" {
		return kind
	}
	// These ordinary text file formats have no signature beyond plain text.
	// Keep the declared format only when sniffing agrees the body is text.
	if sniff == "text/plain" {
		switch kind {
		case "text/markdown", "text/csv", "application/json":
			return kind
		}
	}
	if kind == sniff {
		return kind
	}
	if kind == "text/plain" && strings.HasPrefix(sniff, "text/") {
		return "application/octet-stream"
	}
	if strings.HasPrefix(kind, "application/") && (sniff == "application/octet-stream" || sniff == "application/zip") {
		return kind
	}
	return ""
}

// prepareMP4Reader reuses the engine's seekable spool or creates one bounded
// private temporary file for ordinary readers. Parsing and upload share bytes.
func prepareMP4Reader(ctx context.Context, reader io.Reader, size int64) (io.Reader, func(), error) {
	if seekable, ok := reader.(interface {
		io.ReaderAt
		io.Seeker
	}); ok {
		position, positionErr := seekable.Seek(0, io.SeekCurrent)
		if positionErr != nil {
			return nil, func() {}, ErrInvalid.New(ctx)
		}
		if position == 0 {
			end, endErr := seekable.Seek(0, io.SeekEnd)
			restored, restoreErr := seekable.Seek(0, io.SeekStart)
			if endErr != nil || restoreErr != nil || restored != 0 || end != size {
				return nil, func() {}, ErrInvalid.New(ctx)
			}
			return reader, func() {}, nil
		}
	}
	return spoolUploadReader(ctx, reader, size, "ditto-groupme-sdk-mp4-*")
}

// spoolUploadReader validates one exact bounded body before any provider POST.
// Temporary disk avoids retaining a maximum-size file in memory.
func spoolUploadReader(ctx context.Context, reader io.Reader, size int64, pattern string) (io.Reader, func(), error) {
	spool, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, func() {}, ErrUnavailable.New(ctx)
	}
	cleanup := func() {
		if closeErr := spool.Close(); closeErr != nil {
			slog.DebugContext(ctx, "GroupMe temporary media close failed")
		}
		if removeErr := os.Remove(spool.Name()); removeErr != nil {
			slog.DebugContext(ctx, "GroupMe temporary media removal failed")
		}
	}
	written, err := io.Copy(spool, io.LimitReader(reader, size+1))
	if err != nil || written != size {
		cleanup()
		return nil, func() {}, ErrInvalid.New(ctx)
	}
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, func() {}, ErrInvalid.New(ctx)
	}
	return spool, cleanup, nil
}

// mp4Dimensions reads a bounded ISO BMFF box tree without decoding media.
// Malformed/oversized boxes fail closed; compatible brands are honored.
func mp4Dimensions(reader io.Reader, size int64) (int, int) {
	r, ok := reader.(io.ReaderAt)
	if !ok || size < 24 || size > MaxUploadBytes {
		return 0, 0
	}
	seeker, ok := reader.(io.Seeker)
	if !ok {
		return 0, 0
	}
	pos, err := seeker.Seek(0, io.SeekCurrent)
	if err != nil || pos != 0 {
		return 0, 0
	}
	var head [12]byte
	if _, err = r.ReadAt(head[:], 0); err != nil || string(head[4:8]) != "ftyp" {
		return 0, 0
	}
	ftypSize := int64(binary.BigEndian.Uint32(head[:4]))
	if ftypSize < 16 || ftypSize > 4096 || ftypSize > size || ftypSize%4 != 0 {
		return 0, 0
	}
	brands := make([]byte, ftypSize)
	if _, err = r.ReadAt(brands, 0); err != nil {
		return 0, 0
	}
	compatible := validMP4Brand(string(brands[8:12]))
	for offset := 16; offset+4 <= len(brands); offset += 4 {
		compatible = compatible || validMP4Brand(string(brands[offset:offset+4]))
	}
	if !compatible {
		return 0, 0
	}
	budget := 2048
	var scan func(int64, int64, int) (int, int)
	scan = func(start, end int64, depth int) (int, int) {
		if depth > 4 {
			return 0, 0
		}
		for offset := start; offset+8 <= end; {
			budget--
			if budget < 0 {
				return 0, 0
			}
			var header [16]byte
			if _, err = r.ReadAt(header[:8], offset); err != nil {
				return 0, 0
			}
			length := int64(binary.BigEndian.Uint32(header[:4]))
			headerSize := int64(8)
			if length == 1 {
				if offset+16 > end {
					return 0, 0
				}
				if _, err = r.ReadAt(header[8:], offset+8); err != nil {
					return 0, 0
				}
				large := binary.BigEndian.Uint64(header[8:])
				if large > math.MaxInt64 {
					return 0, 0
				}
				length = int64(large)
				headerSize = 16
			}
			if length == 0 {
				length = end - offset
			}
			if length < headerSize || length > end-offset {
				return 0, 0
			}
			switch string(header[4:8]) {
			case "moov", "trak":
				if w, h := scan(offset+headerSize, offset+length, depth+1); w > 0 && h > 0 {
					return w, h
				}
			case "tkhd":
				if length < headerSize+84 {
					return 0, 0
				}
				var tail [8]byte
				if _, err = r.ReadAt(tail[:], offset+length-8); err != nil {
					return 0, 0
				}
				w, h := int(binary.BigEndian.Uint32(tail[:4])>>16), int(binary.BigEndian.Uint32(tail[4:])>>16)
				if w > 0 && h > 0 && w <= 16384 && h <= 16384 {
					return w, h
				}
			}
			offset += length
		}
		return 0, 0
	}
	return scan(0, size, 0)
}

func validMP4Brand(brand string) bool {
	switch brand {
	case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "dash", "M4V ":
		return true
	}
	return false
}

func downloadContentType(kind string) string {
	switch kind {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "application/javascript", "text/javascript", "text/xml", "application/xml":
		return "application/octet-stream"
	}
	return kind
}

type FileMetadata struct {
	FileID   string `json:"file_id"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size"`
	Status   int    `json:"status"`
}

// FileMetadata performs one bounded read using the native POST fileData route.
// IDs must come from the caller's explicitly selected owned message page.
func (c *Client) FileMetadata(ctx context.Context, cid string, fileIDs []string) ([]FileMetadata, error) {
	if !ValidConversationID(cid) || len(fileIDs) > 100 {
		return nil, ErrInvalid.New(ctx)
	}
	wanted := map[string]bool{}
	for _, id := range fileIDs {
		if !ValidID(id) || wanted[id] {
			return nil, ErrInvalid.New(ctx)
		}
		wanted[id] = true
	}
	if len(fileIDs) == 0 {
		return []FileMetadata{}, nil
	}
	body, err := json.Marshal(struct {
		FileIDs []string `json:"file_ids"`
	}{fileIDs})
	if err != nil {
		return nil, ErrInvalid.New(ctx)
	}
	resp, err := c.fileRequest(ctx, fileRequestOptions{Method: http.MethodPost, Route: "/v1/" + cid + "/fileData", Body: bytes.NewReader(body), Size: int64(len(body)), ContentType: "application/json"})
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.DebugContext(ctx, "GroupMe response body close failed")
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrUnavailable.New(ctx)
	}

	var wire []struct {
		FileID string `json:"file_id"`
		Data   struct {
			Name     string `json:"file_name"`
			MIMEType string `json:"mime_type"`
			Size     int64  `json:"file_size"`
		} `json:"file_data"`
		Meta int `json:"meta"`
	}
	if json.Unmarshal(raw, &wire) != nil || len(wire) != len(fileIDs) {
		return nil, ErrUnavailable.New(ctx)
	}
	seen := map[string]bool{}
	out := make([]FileMetadata, 0, len(wire))
	for _, item := range wire {
		if !wanted[item.FileID] || seen[item.FileID] || item.Meta != 200 || item.Data.Size < 0 {
			return nil, ErrUnavailable.New(ctx)
		}
		seen[item.FileID] = true
		name := item.Data.Name
		if !validFileName(name) {
			name = ""
		}
		kind := item.Data.MIMEType
		if kind != "" {
			parsed, _, err := mime.ParseMediaType(kind)
			if err != nil {
				kind = ""
			} else {
				kind = downloadContentType(parsed)
			}
		}
		out = append(out, FileMetadata{FileID: item.FileID, Name: name, MIMEType: kind, Size: item.Data.Size, Status: item.Meta})
	}
	return out, nil
}
