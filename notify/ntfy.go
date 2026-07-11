package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultNtfyServer is the public instance. It needs no account and no API key.
const DefaultNtfyServer = "https://ntfy.sh"

// Turns "forgot a method" into an error here rather than at some distant call site.
var _ Notifier = (*Ntfy)(nil)

// ⚠️ Publishing to ntfy is an outbound TLS call, so the runtime image needs CA certificates.
// A `scratch` base has none, and every push dies x509 while the health check stays green.
// See the Dockerfile.

// Ntfy publishes to an ntfy server (https://ntfy.sh/docs/publish/).
//
// ⚠️ The topic name is the ONLY secret ntfy has. There is no key: anyone who knows the
// topic can subscribe to your notifications *and* send you some. So pick an unguessable
// one, keep it in an env var, and never put it in git, in a log line, or in frontend code
// (a VITE_* variable is inlined into the bundle every visitor downloads).
type Ntfy struct {
	Server string // no trailing slash; DefaultNtfyServer if empty
	Topic  string
	Client *http.Client // http.DefaultClient if nil — which has NO timeout, so set one
}

func NewNtfy(server, topic string) *Ntfy {
	if server == "" {
		server = DefaultNtfyServer
	}
	return &Ntfy{
		Server: strings.TrimRight(server, "/"),
		Topic:  topic,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

// FromEnv builds the Notifier the environment asks for: $NTFY_TOPIC turns notifications on,
// $NTFY_SERVER optionally redirects them at a self-hosted instance. The caller gets a working
// Nop either way, so it only needs ok to decide what to log.
func FromEnv() (n Notifier, ok bool) {
	topic := strings.TrimSpace(os.Getenv("NTFY_TOPIC"))
	if topic == "" {
		return Nop{}, false
	}
	return NewNtfy(strings.TrimSpace(os.Getenv("NTFY_SERVER")), topic), true
}

// String deliberately omits the topic: it is a shared secret, and this ends up in log
// files and in the host's log stream.
func (n *Ntfy) String() string { return "ntfy " + n.Server }

func (n *Ntfy) Notify(ctx context.Context, msg Notification) error {
	url := n.Server + "/" + n.Topic
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(msg.Body))
	if err != nil {
		return fmt.Errorf("build ntfy request: %w", err)
	}

	// ntfy takes the message as the raw body and everything else as headers.
	// ⚠️ HTTP headers are Latin-1: a non-ASCII Title arrives mangled. The body is UTF-8
	// and safe, which is why the emoji live in Tags (names, not glyphs) and not the Title.
	if msg.Title != "" {
		req.Header.Set("Title", msg.Title)
	}
	if len(msg.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(msg.Tags, ","))
	}
	if msg.Click != "" {
		req.Header.Set("Click", msg.Click)
	}
	req.Header.Set("Priority", strconv.Itoa(msg.Priority.ntfyLevel()))

	client := n.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post to ntfy: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		// Drain a little of the body: ntfy explains its refusals (429 = topic rate limit),
		// and the status alone rarely says enough.
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		return fmt.Errorf("ntfy refused the notification: %s: %s", res.Status, strings.TrimSpace(string(detail)))
	}
	// Drain the rest, or this connection cannot be reused for the next notification.
	io.Copy(io.Discard, res.Body)
	return nil
}
