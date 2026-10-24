package api

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Bucket objects (L2-013 OBJ-*, L3-002 §14).

// ObjectInfo is one object of a bucket.
type ObjectInfo struct {
	Key         string            `json:"key"`
	Size        int64             `json:"size"`
	Modified    time.Time         `json:"modified"`
	ContentType string            `json:"contentType"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// ObjectList is one folder of a bucket, one page at a time.
type ObjectList struct {
	Prefix     string       `json:"prefix"`
	Folders    []string     `json:"folders"`
	Items      []ObjectInfo `json:"items"`
	NextCursor *string      `json:"nextCursor"`
}

// Limits (L3-002 §14.2).
const (
	ObjectUploadMax       = 5 << 30
	ObjectListMax         = 1000
	ObjectTagsMax         = 10
	ObjectTagValueMax     = 256
	ObjectTagHeaderPrefix = "X-Enclave-Tag-"
)

var objectTagName = regexp.MustCompile(TagNamePatternSource)

// ValidateObjectKey checks a key an object is written to or read from (OBJ-04).
func ValidateObjectKey(key string) []FieldError {
	bad := func(msg string) []FieldError {
		return []FieldError{{Field: "key", Code: FieldInvalidFormat, Message: msg}}
	}
	switch {
	case key == "":
		return []FieldError{{Field: "key", Code: FieldRequired, Message: "Give the object's key, like videos/intro.mov."}}
	case len(key) > ObjectKeyMaxLen || !utf8.ValidString(key) || strings.ContainsAny(key, "\r\n"):
		return bad("Use a key of up to 1024 characters on one line.")
	case strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/"):
		return bad("A key doesn't start or end with /: use videos/intro.mov.")
	case slices.ContainsFunc(strings.Split(key, "/"), func(p string) bool { return p == "" || p == "." || p == ".." }):
		return bad("A key has no empty, . or .. folder.")
	}
	return nil
}

// ValidateObjectPrefix checks a folder to list: "" or a key-like path ending in "/".
func ValidateObjectPrefix(prefix string) []FieldError {
	if prefix == "" {
		return nil
	}
	if !strings.HasSuffix(prefix, "/") || ValidateObjectKey(strings.TrimSuffix(prefix, "/")) != nil {
		return []FieldError{{Field: "prefix", Code: FieldInvalidFormat, Message: "Use a folder like videos/ or media/2026/."}}
	}
	return nil
}

// ValidateObjectTags checks the tags of an upload (OBJ-02).
func ValidateObjectTags(tags map[string]string) []FieldError {
	var errs []FieldError
	if len(tags) > ObjectTagsMax {
		errs = append(errs, FieldError{Field: "tags", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d tags.", ObjectTagsMax)})
	}
	for _, k := range sortedKeys(tags) {
		v := tags[k]
		switch {
		case !objectTagName.MatchString(k):
			errs = append(errs, FieldError{Field: "tags." + k, Code: FieldInvalidFormat, Message: "A tag name uses lowercase letters, digits and hyphens, like video-id."})
		case v == "" || len(v) > ObjectTagValueMax || strings.IndexFunc(v, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0:
			errs = append(errs, FieldError{Field: "tags." + k, Code: FieldInvalidFormat, Message: fmt.Sprintf("A tag value is 1–%d printable characters.", ObjectTagValueMax)})
		}
	}
	return errs
}
