package api

import "strings"

// v0.8: the catalogue and stacks (L2-010 CAT-*, L3-002 §11.5 – §11.7).

// Template parameter types.
const (
	ParamString   = "string"
	ParamNumber   = "number"
	ParamBoolean  = "boolean"
	ParamSecret   = "secret"
	ParamHostname = "hostname"
)

// Catalogue categories: products to install, ways in from the internet (L2-014 TUN-15), and outside services
// apps use (L2-015 CNX-01).
const (
	CategoryApps         = "apps"
	CategoryPublicAccess = "public-access"
	CategoryConnections  = "connections"
)

// CatalogueCategories lists the categories, in the order the console shows them.
var CatalogueCategories = []string{CategoryApps, CategoryPublicAccess, CategoryConnections}

// ParameterTypes lists the parameter types.
var ParameterTypes = []string{ParamString, ParamNumber, ParamBoolean, ParamSecret, ParamHostname}

// Template limits (L3-002 §11.6).
const (
	TemplateMaxBytes    = 64 << 10
	TemplateMembersMax  = 50
	TemplateParamsMax   = 30
	TemplateVersionsMax = 20
	GenerateMin         = 16
	GenerateMax         = 128
)

// TemplateParameter is one of a template's parameters.
type TemplateParameter struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// TemplateMember is a resource a template creates, as it will be named ("<stack>" stands for the stack).
type TemplateMember struct {
	Kind     Kind   `json:"kind"`
	Name     string `json:"name"`
	Optional bool   `json:"optional"`
}

// CatalogueEntry is one stack of the catalogue.
type CatalogueEntry struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Versions    []string `json:"versions"`
	BuiltIn     bool     `json:"builtIn"`
	// Category groups entries; Signup is where to make the account a way in needs (TUN-15).
	Category   string              `json:"category"`
	Signup     string              `json:"signup,omitempty"`
	Parameters []TemplateParameter `json:"parameters"`
	Members    []TemplateMember    `json:"members"`
	Outputs    []string            `json:"outputs"`
}

// CatalogueList wraps entries.
type CatalogueList struct {
	Items []CatalogueEntry `json:"items"`
}

// AddTemplate is the body of POST /catalogue.
type AddTemplate struct {
	Body string `json:"body"`
}

// StackSpec is a stack: a template (from the catalogue, or given inline) and its parameters (ADR-048).
type StackSpec struct {
	Template     string         `json:"template,omitempty"`
	TemplateBody string         `json:"templateBody,omitempty"`
	Version      string         `json:"version,omitempty"`
	Parameters   map[string]any `json:"parameters,omitempty"`
	// SecretParameters are write-only: they become the stack's secrets and are never returned.
	SecretParameters map[string]string `json:"secretParameters,omitempty"`
}

// StackMember is one member in a stack's outputs.
type StackMember struct {
	Kind  Kind   `json:"kind"`
	Name  string `json:"name"`
	ID    string `json:"id"`
	Phase Phase  `json:"phase"`
}

// Stack outputs.
const (
	OutputTemplate        = "template"
	OutputTemplateVersion = "version"
	OutputMembers         = "members"
	OutputStackOutputs    = "outputs"
	OutputReady           = "ready"
	OutputTotal           = "total"
)

func normalizeStack(s StackSpec) StackSpec {
	s.Template, s.Version = strings.TrimSpace(s.Template), strings.TrimSpace(s.Version)
	return s
}

func validateStack(s StackSpec) []FieldError {
	var errs []FieldError
	switch {
	case s.Template == "" && s.TemplateBody == "":
		errs = append(errs, FieldError{Field: "spec.template", Code: FieldExactlyOne, Message: "Name a catalogue entry, or give a template."})
	case s.Template != "" && s.TemplateBody != "":
		errs = append(errs, FieldError{Field: "spec.template", Code: FieldExactlyOne, Message: "Name a catalogue entry or give a template, not both."})
	case s.Template != "" && ValidateName(s.Template) != nil:
		errs = append(errs, FieldError{Field: "spec.template", Code: FieldInvalidFormat, Message: "Name a catalogue entry."})
	case len(s.TemplateBody) > TemplateMaxBytes:
		errs = append(errs, FieldError{Field: "spec.templateBody", Code: FieldTooLong, Message: "A template can be at most 64 KiB."})
	}
	for _, k := range sortedKeys(s.SecretParameters) {
		if _, dup := s.Parameters[k]; dup {
			errs = append(errs, FieldError{Field: "spec.secretParameters." + k, Code: FieldNotAllowed, Message: k + " is given twice."})
		}
	}
	return errs
}
