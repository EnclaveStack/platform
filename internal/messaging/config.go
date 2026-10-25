package messaging

import (
	"fmt"
	"slices"
	"strings"
)

// The broker's configuration (ADR-029): one account with JetStream, the control plane's system user, and
// one user per queue and topic whose permissions name only that resource's subjects.

// SystemUserName is the control plane's own user.
const SystemUserName = "system"

// User is one broker user. PasswordHash is a bcrypt hash; the plain password never reaches the file.
// Nil permission lists mean everything (the system user only).
type User struct {
	Name         string
	PasswordHash string
	Publish      []string
	Subscribe    []string
}

// QueueUserName and TopicUserName name the users apps sign in as.
func QueueUserName(name string) string { return "q-" + name }

// TopicUserName is a topic's user.
func TopicUserName(name string) string { return "t-" + name }

// QueueUser may send to its queue and receive from it through its one consumer, and nothing else.
func QueueUser(name, hash string) User {
	stream := QueueStream(name)
	return User{Name: QueueUserName(name), PasswordHash: hash,
		Publish: []string{QueueSubject(name), "$JS.API.CONSUMER.MSG.NEXT." + stream + "." + QueueConsumer, "$JS.ACK." + stream + "." + QueueConsumer + ".>",
			"$JS.API.STREAM.INFO." + stream, "$JS.API.CONSUMER.INFO." + stream + "." + QueueConsumer, "$JS.API.INFO"},
		Subscribe: []string{"_INBOX.>"}}
}

// TopicUser may publish to its topic and listen to it live, and nothing else (TOP-06).
func TopicUser(name, hash string) User {
	return User{Name: TopicUserName(name), PasswordHash: hash,
		Publish:   []string{TopicSubject(name), "$JS.API.STREAM.INFO." + TopicStream(name), "$JS.API.INFO"},
		Subscribe: []string{TopicSubject(name), "_INBOX.>"}}
}

// MaxPayload bounds one message on the wire: the largest body (1 MiB) plus room for headers.
const MaxPayload = 1<<20 + 64<<10

// HeaderAllowance is what a stream allows beyond a queue's maximum body, for headers.
const HeaderAllowance = 64 << 10

// RenderConfig writes the broker's configuration file. Users are sorted, so the same users always give
// the same file and an unchanged configuration is never reloaded.
func RenderConfig(users []User) []byte {
	sorted := slices.Clone(users)
	slices.SortFunc(sorted, func(a, b User) int { return strings.Compare(a.Name, b.Name) })
	var b strings.Builder
	b.WriteString("# Written by EnclaveStack (ADR-029); changes are overwritten.\n")
	fmt.Fprintf(&b, "listen: \"0.0.0.0:4222\"\nmax_payload: %d\n", MaxPayload)
	b.WriteString("jetstream {\n  store_dir: \"/data\"\n}\n")
	b.WriteString("accounts {\n  HC: {\n    jetstream: enabled\n    users: [\n")
	for _, u := range sorted {
		fmt.Fprintf(&b, "      {user: %s, password: %s", quote(u.Name), quote(u.PasswordHash))
		if u.Publish != nil || u.Subscribe != nil {
			fmt.Fprintf(&b, ", permissions: {publish: {allow: %s}, subscribe: {allow: %s}}", quoteList(u.Publish), quoteList(u.Subscribe))
		}
		b.WriteString("}\n")
	}
	b.WriteString("    ]\n  }\n}\n")
	return []byte(b.String())
}

// quote writes a configuration string. Names, subjects and bcrypt hashes are all the file ever holds,
// none with control characters; the format knows only \\ and \" (and \t, \n, \r, \xXX) as escapes.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func quoteList(l []string) string {
	if l == nil {
		l = []string{}
	}
	parts := make([]string, len(l))
	for i, s := range l {
		parts[i] = quote(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
