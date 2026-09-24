// Package agentproto is the wire contract between the VRSky remote agent — a
// small binary on a customer machine — and the remote-agent gateway service it
// dials out to (#266). Both sides import it, so a change here is a protocol
// change: bump ProtoVersion for anything an older peer would misread.
//
// The agent speaks plain HTTPS through the ingress; it never touches NATS. NATS
// has no authentication of its own today, so the gateway is the boundary: it
// authenticates the agent's credential, resolves it to one agent in one tenant,
// and only ever hands that agent its own work.
//
// Operations are a fixed, typed set — there is no command execution. Every
// work item carries an Op so new operation types can be added without
// breaking agents that do not know them (they skip what they do not recognise).
package agentproto

import "time"

// ProtoVersion is the protocol major version. The agent sends it in
// HeaderProto; a gateway that cannot speak it answers 426 unsupported_protocol.
const ProtoVersion = 1

// HTTP headers.
const (
	HeaderProto    = "X-Vrsky-Agent-Proto"
	HeaderChecksum = "X-Vrsky-Checksum"
)

// Token formats. The prefixes make a pasted value recognisable, and stop the
// one-time registration token being mistaken for the long-lived credential it
// is exchanged for.
const (
	RegTokenPrefix   = "vrsky_reg_"
	CredentialPrefix = "vrsky_agent_"
)

const (
	// PollHoldMax is the longest the gateway holds a work poll open. Under
	// ingress-nginx's 60 s default read timeout and typical 30-60 s NAT idle
	// timeouts, so a quiet poll returns before anything in between drops it.
	PollHoldMax = 25 * time.Second

	// LeaseDuration is how long a delivery handed to an agent stays leased to
	// it. One not acknowledged within this is offered again on the next poll,
	// which covers an agent that crashed mid-write.
	LeaseDuration = 2 * time.Minute

	// InlineWorkBytes is the largest payload carried inside a poll response.
	// Anything bigger is fetched separately as a stream from BodyURL.
	InlineWorkBytes = 64 << 10
)

// Operation types.
const (
	OpWriteFile = "write_file" // Delivery: write the body into a write directory
	OpWatchDir  = "watch_dir"  // Watch: upload new files from a read directory
)

// Directory modes.
const (
	ModeRead  = "read"  // the agent uploads files that appear here
	ModeWrite = "write" // the agent writes delivered files here
)

// What a read directory does with a file once it has been uploaded.
const (
	AfterMove   = "move"   // into <dir>/processed/
	AfterDelete = "delete" // remove it
)

// Ack statuses.
const (
	AckOK     = "ok"
	AckFailed = "failed"
)

// Error codes, in ErrorResponse.Error.
const (
	ErrInvalidToken        = "invalid_token"
	ErrTokenExpiredOrUsed  = "token_expired_or_used"
	ErrUnauthorized        = "unauthorized"
	ErrAgentRevoked        = "agent_revoked"
	ErrUnsupportedProtocol = "unsupported_protocol"
	ErrUnknownDelivery     = "unknown_delivery"
	ErrUnknownDirectory    = "unknown_directory"
	ErrInvalidFilename     = "invalid_filename"
	ErrNoActiveWatch       = "no_active_watch"
	ErrNameTaken           = "name_taken"
	ErrTooLarge            = "too_large"
	ErrBadRequest          = "bad_request"
)

// Directory is one directory the agent's config defines. Names and modes only:
// the path stays in the agent's config file and never leaves the machine.
type Directory struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// RegisterRequest exchanges a one-time registration token for a credential.
type RegisterRequest struct {
	RegistrationToken string      `json:"registration_token"`
	Name              string      `json:"name,omitempty"`
	Hostname          string      `json:"hostname"`
	OS                string      `json:"os"`
	Arch              string      `json:"arch"`
	Version           string      `json:"version"`
	Directories       []Directory `json:"directories"`
}

// RegisterResponse carries the long-lived credential. It is returned exactly
// once; the gateway stores only its hash.
type RegisterResponse struct {
	AgentID    string `json:"agent_id"`
	Name       string `json:"name"`
	Credential string `json:"credential"`
}

// AnnounceRequest is sent on every agent start and after every reconnect, so
// the directory list and machine details stay current.
type AnnounceRequest struct {
	Hostname    string      `json:"hostname"`
	OS          string      `json:"os"`
	Arch        string      `json:"arch"`
	Version     string      `json:"version"`
	Directories []Directory `json:"directories"`
}

// WorkResponse answers a poll. Watches is the FULL set of directories the agent
// should currently be watching, not a delta, so either side can restart and
// the agent converges on the next poll.
type WorkResponse struct {
	Watches []Watch `json:"watches"`
	// WatchesVersion identifies this watch set. The agent echoes it back as
	// the "watches" query parameter of its next poll; when it no longer
	// matches, the gateway answers at once instead of holding the poll, so a
	// newly deployed pipeline reaches the agent immediately rather than after
	// up to PollHoldMax.
	WatchesVersion string     `json:"watches_version"`
	Deliveries     []Delivery `json:"deliveries"`
	// NextPollMS asks the agent to wait before polling again; 0 means at once.
	NextPollMS int `json:"next_poll_ms"`
}

// Watch asks the agent to upload new files from one of its read directories
// into a pipeline.
type Watch struct {
	Op           string `json:"op"` // OpWatchDir
	ConnectionID string `json:"connection_id"`
	Directory    string `json:"directory"`
	After        string `json:"after"` // AfterMove | AfterDelete
}

// Delivery asks the agent to write one file into one of its write
// directories. The body is InlineBase64 when small, otherwise a stream from
// BodyURL. Either way the agent verifies Checksum before making the file
// visible, then acknowledges.
type Delivery struct {
	Op           string `json:"op"` // OpWriteFile
	ID           string `json:"id"`
	ConnectionID string `json:"connection_id"`
	Directory    string `json:"directory"`
	Filename     string `json:"filename"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	Checksum     string `json:"checksum"` // "sha256:<hex>"
	InlineBase64 string `json:"inline_base64,omitempty"`
	BodyURL      string `json:"body_url,omitempty"`
}

// AckRequest reports the outcome of a delivery.
type AckRequest struct {
	Status string `json:"status"` // AckOK | AckFailed
	Error  string `json:"error,omitempty"`
}

// UploadResponse confirms a file entered the pipeline.
type UploadResponse struct {
	EnvelopeID string `json:"envelope_id"`
}

// ErrorResponse is every non-2xx body the gateway sends.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}
