package cachelab

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Recorder is a loopback reverse proxy placed between the experiment client and
// the real provider. It forwards every header and byte unchanged, so the
// provider sees the client's own request, and it journals what crossed the
// boundary: the request body's digest and length, the HTTP status, the latency,
// the response usage exactly as the provider wrote it, and whether the frozen
// task's expected marker came back.
//
// Bodies are recorded as digests only. The captured response copy lives in
// memory for the duration of one request — usage parsing and the marker check
// need it — and is dropped when the request completes.
type Recorder struct {
	srv     *httptest.Server
	proxy   *httputil.ReverseProxy
	journal *Journal
	target  *url.URL

	mu      sync.Mutex
	turn    TurnContext
	pending map[int]*pendingSample
	samples []Sample
	repeat  map[string]int
	attempt map[string]int
	lastAt  time.Time
	seq     int
}

// TurnContext is the experiment context the driver binds before each request.
// The recorder stamps it onto every sample it takes until the driver changes it,
// which is exact because an arm runs its requests serially.
type TurnContext struct {
	Arm          string
	RunID        string
	TeamID       string
	MemberID     string
	ModelRef     string
	Client       string
	ClientCommit string
	ConfigDigest string
	// TurnSeq is the driver's turn index within the member's session.
	TurnSeq int
	// Expect is the exact marker the frozen task asks for. Empty records the
	// quality outcome as unchecked rather than assumed.
	Expect string
	// Confounds are reasons this arm's samples cannot support a causal claim.
	Confounds []string
}

type pendingSample struct {
	sample     Sample
	started    time.Time
	attemptKey string
}

// requestIndexKey carries one request's reserved sample number from the inbound
// request into the proxy's response hook, where the sample is finished.
type requestIndexKey struct{}

// captureBody copies the response body into memory up to a limit while it
// streams to the client. Closing it finalises the sample: by then the provider's
// usage and the task marker are both available.
type captureBody struct {
	rc      io.ReadCloser
	buf     bytes.Buffer
	limit   int
	readErr error
	done    func([]byte, error)
	closed  bool
}

func (c *captureBody) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 && c.buf.Len() < c.limit {
		if room := c.limit - c.buf.Len(); room < n {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p[:n])
		}
	}
	if err != nil && err != io.EOF {
		c.readErr = err
	}
	return n, err
}

func (c *captureBody) Close() error {
	err := c.rc.Close()
	if !c.closed {
		c.closed = true
		c.done(c.buf.Bytes(), c.readErr)
	}
	return err
}

// NewRecorder starts the proxy in front of the given upstream endpoint. The
// upstream must be the real provider base URL: the recorder never rewrites the
// request, so a client pointed here reaches the endpoint it would have reached.
func NewRecorder(upstream string, journal *Journal) (*Recorder, error) {
	target, err := url.Parse(strings.TrimSpace(upstream))
	if err != nil {
		return nil, fmt.Errorf("cachelab: parse upstream %q: %w", upstream, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("cachelab: upstream %q has no scheme or host", upstream)
	}
	r := &Recorder{
		journal: journal,
		target:  target,
		pending: map[int]*pendingSample{},
		repeat:  map[string]int{},
		attempt: map[string]int{},
	}
	r.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// Only the destination moves: method, headers, body and query reach the
			// provider as the client wrote them. No forwarded-for headers are added,
			// because an unexpected header is itself a request difference.
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = joinedPath(target.Path, pr.In.URL.Path)
			pr.Out.Host = target.Host
		},
		// FlushInterval -1 flushes every write, so a streamed response keeps its
		// timing instead of being buffered by the proxy.
		FlushInterval:  -1,
		ModifyResponse: r.captureResponse,
		ErrorHandler:   r.failRequest,
	}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	return r, nil
}

// URL is the loopback base URL a client or a member pool entry points at.
func (r *Recorder) URL() string { return r.srv.URL }

// UpstreamHost is the provider host the recorder forwards to, for the journal.
func (r *Recorder) UpstreamHost() string { return r.target.Host }

// Begin binds the context the next requests are recorded under. The driver calls
// it before each request, so an arm change or a turn change is never inferred.
func (r *Recorder) Begin(turn TurnContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turn = turn
}

// Samples returns the samples taken so far, in request order.
func (r *Recorder) Samples() []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Sample(nil), r.samples...)
}

// joinedPath composes an upstream base path with the inbound request path, so a
// provider reached under a prefix keeps that prefix. An inbound path that already
// carries the base path is left alone: a client dialling the recorder uses the
// same versioned path it would use against the provider itself, and doubling it
// would request a path the provider does not serve.
func joinedPath(base, request string) string {
	request = strings.TrimSpace(request)
	if request == "" || request == "/" {
		request = "/"
	}
	if !strings.HasPrefix(request, "/") {
		request = "/" + request
	}
	base = strings.Trim(strings.TrimSpace(base), "/")
	if base == "" {
		return request
	}
	if request == "/"+base || strings.HasPrefix(request, "/"+base+"/") {
		return request
	}
	return "/" + base + request
}

// WaitForSamples waits until at least want requests have been finalised, or the
// timeout expires, and returns what was recorded either way. A sample is only
// final once the provider's stream ended, which trails the client's last read,
// so a caller analysing a run waits instead of sampling the recorder.
func (r *Recorder) WaitForSamples(want int, timeout time.Duration) []Sample {
	deadline := time.Now().Add(timeout)
	for {
		samples := r.Samples()
		if len(samples) >= want || time.Now().After(deadline) {
			return samples
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Close stops the proxy.
func (r *Recorder) Close() {
	if r.srv != nil {
		r.srv.Close()
	}
}

// serve digests the request body before handing the request to the proxy, so the
// forwarded request is byte-identical to the one the client sent.
func (r *Recorder) serve(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64<<20))
	if err != nil {
		http.Error(w, "cachelab: read request body", http.StatusBadRequest)
		return
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	idx := r.openSample(body)
	req = req.WithContext(context.WithValue(req.Context(), requestIndexKey{}, idx))
	r.proxy.ServeHTTP(&capturingWriter{ResponseWriter: w}, req)
}

// openSample reserves the sample's sequence number and request identity. The
// response half is filled in when the exchange completes.
func (r *Recorder) openSample(body []byte) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	now := time.Now()
	sample := Sample{
		Arm:          r.turn.Arm,
		RunID:        r.turn.RunID,
		Seq:          r.seq,
		At:           now.UTC().Format(time.RFC3339Nano),
		TeamID:       r.turn.TeamID,
		MemberID:     r.turn.MemberID,
		ModelRef:     r.turn.ModelRef,
		UpstreamHost: r.target.Host,
		Client:       r.turn.Client,
		ClientCommit: r.turn.ClientCommit,
		ConfigDigest: r.turn.ConfigDigest,
		RequestHash:  RequestDigest(body),
		RequestBytes: len(body),
		TurnSeq:      r.turn.TurnSeq,
		QualityCheck: QualityUnchecked,
		Confounds:    SortedConfounds(r.turn.Confounds),
	}
	key := sample.Arm + "\x00" + sample.RequestHash
	r.repeat[key]++
	sample.Repeat = r.repeat[key]
	attemptKey := sample.Arm + "\x00" + strconv.Itoa(sample.TurnSeq)
	sample.Attempt = 1
	if r.attempt[attemptKey] > 0 {
		sample.Attempt = r.attempt[attemptKey] + 1
	}
	r.attempt[attemptKey] = 0
	if !r.lastAt.IsZero() {
		sample.GapMS = now.Sub(r.lastAt).Milliseconds()
	}
	r.lastAt = now
	r.pending[sample.Seq] = &pendingSample{sample: sample, started: now, attemptKey: attemptKey}
	return sample.Seq
}

// captureResponse wraps the provider's response so the body copy is handed over
// when the stream ends. Usage is parsed from the copy, never from a client's
// normalized view of it.
func (r *Recorder) captureResponse(resp *http.Response) error {
	idx, ok := responseIndex(resp)
	if !ok {
		return nil
	}
	status := resp.StatusCode
	if pending, ok := r.pendingSample(idx); ok {
		pending.sample.Status = status
	}
	resp.Body = &captureBody{rc: resp.Body, limit: 4 << 20, done: func(body []byte, err error) {
		r.finishSample(idx, status, body, err)
	}}
	return nil
}

// responseIndex recovers the sample number the request was reserved under.
func responseIndex(resp *http.Response) (int, bool) {
	if resp == nil || resp.Request == nil {
		return 0, false
	}
	idx, ok := resp.Request.Context().Value(requestIndexKey{}).(int)
	return idx, ok
}

// pendingSample looks up one reserved sample.
func (r *Recorder) pendingSample(idx int) (*pendingSample, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pending[idx]
	return pending, ok
}

// failRequest records a transport-level failure: no response was ever received,
// so there is no status and no usage, and the request must not become a
// measurement.
func (r *Recorder) failRequest(w http.ResponseWriter, req *http.Request, err error) {
	if idx, ok := req.Context().Value(requestIndexKey{}).(int); ok {
		if pending, ok := r.pendingSample(idx); ok {
			pending.sample.Error = err.Error()
			r.mu.Lock()
			r.attempt[pending.attemptKey] = pending.sample.Attempt
			r.mu.Unlock()
			// A failed exchange is a sample like any other: it is finalised here
			// rather than left pending forever, because the client never sees a
			// response body to close.
			r.finishSample(idx, http.StatusBadGateway, nil, err)
		}
	}
	w.WriteHeader(http.StatusBadGateway)
}

// finishSample parses usage and the task marker out of the captured copy, then
// stores the completed sample and lets the body go.
func (r *Recorder) finishSample(idx, status int, body []byte, readErr error) {
	pending, ok := r.pendingSample(idx)
	if !ok {
		return
	}
	r.mu.Lock()
	turn := r.turn
	r.mu.Unlock()

	sample := pending.sample
	sample.Status = status
	sample.LatencyMS = time.Since(pending.started).Milliseconds()
	// A 2xx exchange succeeded: a read error there is the client closing a stream
	// it had finished with, which is a property of streaming, not a failure. A
	// failure status keeps the read error, because that exchange did fail.
	if readErr != nil && sample.Error == "" && (status < 200 || status > 299) {
		sample.Error = readErr.Error()
	}
	usage := parseUsage(body)
	if readErr != nil && status >= 200 && status <= 299 && !usage.Reported {
		// The provider answered, but the observation is incomplete: record why
		// instead of reading a truncated body as "the provider reported nothing".
		usage.Problem = UsageProblemBodyReadError
		usage.Split = false
	}
	sample.UsageReported = usage.Reported
	sample.UsageSplit = usage.Split
	sample.UsageProblem = usage.Problem
	sample.UsageShape = usage.Shape
	sample.UsageKeys = usage.Keys
	// The gateway's own reading is recorded beside the protocol reading and never
	// folded into it: it is a private extension some responses omit, so it can
	// corroborate a sample and can never produce one.
	sample.UsageOraclePresent = usage.OraclePresent
	sample.UsageOracleKeys = usage.OracleKeys
	sample.UsageOracleAgrees = usage.OracleAgrees
	if usage.OraclePresent {
		sample.UsageOraclePromptTokens = usage.OraclePrompt
		sample.UsageOracleHitTokens = usage.OracleHit
		sample.UsageOracleMissTokens = usage.OracleMiss
	}
	if usage.Reported {
		sample.UsageSource = "response_body"
		sample.PromptTokens = usage.Prompt
		sample.CacheHitTokens = usage.Hit
		sample.CacheMissTokens = usage.Miss
		sample.CacheWriteTokens = usage.Write
		sample.CompletionTokens = usage.Completion
	}
	sample.QualityCheck = checkMarker(body, turn.Expect)

	r.mu.Lock()
	delete(r.pending, idx)
	if sample.Error == "" && (status < 200 || status > 299) {
		// A failed exchange makes a repeat of the same turn a retry, which the
		// contract excludes from baselines instead of pooling it as warm.
		r.attempt[pending.attemptKey] = sample.Attempt
	}
	journal := r.journal
	r.mu.Unlock()
	if journal != nil {
		// A journal write failure must not change the in-memory record; the run
		// reports the loss instead.
		_ = journal.Append(sample)
	}
	// Publish the sample only after the journal attempt: a reader that waits for
	// the sample and then reads the journal back would otherwise race its own line.
	r.mu.Lock()
	r.samples = append(r.samples, sample)
	r.mu.Unlock()
}

// capturingWriter notes the status the proxy wrote before the body starts.
type capturingWriter struct {
	http.ResponseWriter
	written bool
}

func (w *capturingWriter) WriteHeader(status int) {
	w.written = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *capturingWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps a streamed response streaming through the capturing writer.
func (w *capturingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// checkMarker reports whether the frozen task's expected marker came back. The
// marker is a synthetic word the task asks for verbatim, so the check is
// mechanical and needs no model judgement.
//
// The check reads the response text, not the response bytes: a streamed answer
// arrives in deltas, so a marker can be split across two events and never appear
// contiguously in the body.
func checkMarker(body []byte, expect string) string {
	expect = strings.TrimSpace(expect)
	if expect == "" {
		return QualityUnchecked
	}
	if bytes.Contains(body, []byte(expect)) {
		return QualityPass
	}
	if strings.Contains(responseText(body), expect) {
		return QualityPass
	}
	return QualityFail
}

// The value keys whose strings make up what the model said. They span the
// anthropic and openai response shapes, streamed and whole.
var responseTextKeys = map[string]bool{"text": true, "delta": true, "content": true}

// responseText concatenates the model's own output strings in event order, which
// is what a task check has to read when the transport split them.
func responseText(body []byte) string {
	var b strings.Builder
	for _, chunk := range jsonChunks(body) {
		collectResponseText(chunk, &b)
	}
	return b.String()
}

// collectResponseText appends every recognised output string of one decoded
// value, in the order the document presents them.
func collectResponseText(value any, out *strings.Builder) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if text, ok := child.(string); ok && responseTextKeys[key] {
				out.WriteString(text)
				continue
			}
			collectResponseText(child, out)
		}
	case []any:
		for _, child := range node {
			collectResponseText(child, out)
		}
	}
}
