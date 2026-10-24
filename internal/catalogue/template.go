// Package catalogue parses and renders stack templates (ADR-048, DAR-038, L3-002 §11.6). A template is data:
// API specs with placeholders from a fixed list. There are no loops, expressions, functions, files or
// network access, so a template can build no more than its installer could by hand: the service validates
// every rendered member as a hand-made create.
package catalogue

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

// Template is a parsed template.
type Template struct {
	Name        string
	Title       string
	Description string
	Version     string
	// Category groups templates (api.CatalogueCategories); Signup is where to make an account it needs.
	Category   string
	Signup     string
	Parameters []api.TemplateParameter
	Members    []Member
	// Outputs are rendered in this order.
	Outputs     map[string]string
	outputOrder []string
	// Source is the template as given.
	Source string
}

// Member is one resource of a template.
type Member struct {
	Kind api.Kind
	// Local is the name in the template: "" for the stack's own name.
	Local string
	When  string
	// Generate (secrets only) is the length of a random value; Value (secrets only) a value with
	// placeholders, where ${password:…} may name a member's managed password.
	Generate int
	Value    string
	Spec     any
	Line     int
}

// Problem is one error found in a template, with its line when there is one.
type Problem struct {
	Line    int
	Path    string
	Message string
}

// Problems are a template's errors.
type Problems []Problem

func (p Problems) Error() string {
	parts := make([]string, len(p))
	for i, e := range p {
		parts[i] = e.Field() + ": " + e.Message
	}
	return strings.Join(parts, "; ")
}

// Field is how a problem is named in an API error: "line 12: resources[2].spec".
func (p Problem) Field() string {
	if p.Line > 0 {
		return fmt.Sprintf("line %d: %s", p.Line, p.Path)
	}
	return p.Path
}

// FieldErrors turns problems into an API error's details.
func (p Problems) FieldErrors(prefix string) []api.FieldError {
	out := make([]api.FieldError, len(p))
	for i, e := range p {
		f := e.Field()
		if prefix != "" {
			f = prefix + "." + f
		}
		out[i] = api.FieldError{Field: f, Code: api.FieldInvalidFormat, Message: e.Message}
	}
	return out
}

var (
	semverPattern    = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	paramNamePattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]{0,31}$`)
	placeholder      = regexp.MustCompile(`\$\$|\$\{([a-z]+)(?::([^}]*))?\}`)
	topKeys          = []string{"name", "title", "description", "version", "category", "signup", "parameters", "resources", "outputs"}
	memberKeys       = []string{"kind", "name", "spec", "when", "generate", "value"}
	paramKeys        = []string{"name", "title", "description", "type", "default", "required"}
)

// Parse reads and checks a template (YAML or JSON).
func Parse(body []byte) (*Template, error) {
	var ps Problems
	if len(body) > api.TemplateMaxBytes {
		return nil, Problems{{Path: "template", Message: "A template can be at most 64 KiB."}}
	}
	var root yaml.Node
	if err := yaml.Unmarshal(body, &root); err != nil {
		return nil, Problems{{Path: "template", Message: "It isn't YAML or JSON: " + err.Error()}}
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, Problems{{Line: root.Line, Path: "template", Message: "A template is a map with name, title, version, parameters, resources and outputs."}}
	}
	doc := root.Content[0]
	t := &Template{Source: string(body), Outputs: map[string]string{}}
	fields := mapping(doc, topKeys, "", &ps)
	str := func(key string, required bool) string {
		n := fields[key]
		if n == nil {
			if required {
				ps = append(ps, Problem{Line: doc.Line, Path: key, Message: "Give the template's " + key + "."})
			}
			return ""
		}
		if n.Kind != yaml.ScalarNode {
			ps = append(ps, Problem{Line: n.Line, Path: key, Message: key + " is text."})
			return ""
		}
		return n.Value
	}
	t.Name, t.Title, t.Description, t.Version = str("name", true), str("title", true), str("description", false), str("version", true)
	t.Category, t.Signup = str("category", false), str("signup", false)
	if t.Category == "" {
		t.Category = api.CategoryApps
	} else if !slices.Contains(api.CatalogueCategories, t.Category) {
		ps = append(ps, Problem{Line: fields["category"].Line, Path: "category", Message: "Use " + strings.Join(api.CatalogueCategories, " or ") + "."})
	}
	if t.Signup != "" && !strings.HasPrefix(t.Signup, "https://") {
		ps = append(ps, Problem{Line: fields["signup"].Line, Path: "signup", Message: "signup is an https:// address."})
	}
	if t.Name != "" && api.ValidateName(t.Name) != nil {
		ps = append(ps, Problem{Line: fields["name"].Line, Path: "name", Message: "A catalogue name follows the resource name rules: lowercase letters, digits and hyphens."})
	}
	if t.Version != "" && !semverPattern.MatchString(t.Version) {
		ps = append(ps, Problem{Line: fields["version"].Line, Path: "version", Message: "The version is MAJOR.MINOR.PATCH, like 1.0.0."})
	}
	t.Parameters = parseParams(fields["parameters"], &ps)
	t.Members = parseMembers(fields["resources"], &ps)
	if n := fields["outputs"]; n != nil {
		if n.Kind != yaml.MappingNode {
			ps = append(ps, Problem{Line: n.Line, Path: "outputs", Message: "outputs maps names to text."})
		} else {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if v.Kind != yaml.ScalarNode {
					ps = append(ps, Problem{Line: v.Line, Path: "outputs." + k.Value, Message: "An output is text."})
					continue
				}
				t.Outputs[k.Value] = v.Value
				t.outputOrder = append(t.outputOrder, k.Value)
			}
		}
	}
	ps = append(ps, t.checkPlaceholders()...)
	if len(ps) > 0 {
		return nil, ps
	}
	return t, nil
}

// mapping checks a map's keys and returns its values by key.
func mapping(n *yaml.Node, allowed []string, path string, ps *Problems) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		p := k.Value
		if path != "" {
			p = path + "." + k.Value
		}
		if !slices.Contains(allowed, k.Value) {
			*ps = append(*ps, Problem{Line: k.Line, Path: p, Message: fmt.Sprintf("%s isn't a template field (use %s).", k.Value, strings.Join(allowed, ", "))})
			continue
		}
		if _, dup := out[k.Value]; dup {
			*ps = append(*ps, Problem{Line: k.Line, Path: p, Message: k.Value + " is given twice."})
		}
		out[k.Value] = n.Content[i+1]
	}
	return out
}

func parseParams(n *yaml.Node, ps *Problems) []api.TemplateParameter {
	if n == nil {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		*ps = append(*ps, Problem{Line: n.Line, Path: "parameters", Message: "parameters is a list."})
		return nil
	}
	if len(n.Content) > api.TemplateParamsMax {
		*ps = append(*ps, Problem{Line: n.Line, Path: "parameters", Message: fmt.Sprintf("A template has at most %d parameters.", api.TemplateParamsMax)})
	}
	var out []api.TemplateParameter
	seen := map[string]bool{}
	for i, item := range n.Content {
		path := fmt.Sprintf("parameters[%d]", i)
		if item.Kind != yaml.MappingNode {
			*ps = append(*ps, Problem{Line: item.Line, Path: path, Message: "A parameter is a map with name, title and type."})
			continue
		}
		f := mapping(item, paramKeys, path, ps)
		var p api.TemplateParameter
		if err := item.Decode(&p); err != nil {
			*ps = append(*ps, Problem{Line: item.Line, Path: path, Message: err.Error()})
			continue
		}
		switch {
		case !paramNamePattern.MatchString(p.Name):
			*ps = append(*ps, Problem{Line: item.Line, Path: path + ".name", Message: "A parameter's name is a letter, then letters and digits (camelCase), up to 32."})
		case seen[p.Name]:
			*ps = append(*ps, Problem{Line: item.Line, Path: path + ".name", Message: p.Name + " is a parameter twice."})
		}
		seen[p.Name] = true
		if p.Title == "" {
			p.Title = p.Name
		}
		if !slices.Contains(api.ParameterTypes, p.Type) {
			*ps = append(*ps, Problem{Line: item.Line, Path: path + ".type", Message: "The type is one of " + strings.Join(api.ParameterTypes, ", ") + "."})
		}
		if d := f["default"]; d != nil {
			if p.Type == api.ParamSecret {
				*ps = append(*ps, Problem{Line: d.Line, Path: path + ".default", Message: "A secret has no default in a template."})
			} else if _, err := typed(p, p.Default); err != nil {
				*ps = append(*ps, Problem{Line: d.Line, Path: path + ".default", Message: err.Error()})
			}
		}
		out = append(out, p)
	}
	return out
}

func parseMembers(n *yaml.Node, ps *Problems) []Member {
	if n == nil || n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		line := 0
		if n != nil {
			line = n.Line
		}
		*ps = append(*ps, Problem{Line: line, Path: "resources", Message: "resources is a list of at least one resource."})
		return nil
	}
	if len(n.Content) > api.TemplateMembersMax {
		*ps = append(*ps, Problem{Line: n.Line, Path: "resources", Message: fmt.Sprintf("A template has at most %d resources.", api.TemplateMembersMax)})
	}
	var out []Member
	seen := map[string]bool{}
	for i, item := range n.Content {
		path := fmt.Sprintf("resources[%d]", i)
		if item.Kind != yaml.MappingNode {
			*ps = append(*ps, Problem{Line: item.Line, Path: path, Message: "A resource is a map with kind, name and spec."})
			continue
		}
		f := mapping(item, memberKeys, path, ps)
		m := Member{Line: item.Line}
		if k := f["kind"]; k != nil {
			m.Kind = api.Kind(k.Value)
		}
		if !m.Kind.Valid() || m.Kind == api.KindStack {
			*ps = append(*ps, Problem{Line: item.Line, Path: path + ".kind", Message: fmt.Sprintf("%q isn't a kind a stack can hold.", m.Kind)})
		}
		if nm := f["name"]; nm != nil {
			m.Local = nm.Value
		}
		// The local name is a suffix ("db" in "blog-db"): its characters are checked here, the whole name's
		// length when it is rendered for a stack.
		if m.Local != "" && (api.ValidateName("stack-"+m.Local) != nil || strings.HasPrefix(m.Local, "-") || strings.HasSuffix(m.Local, "-")) {
			*ps = append(*ps, Problem{Line: item.Line, Path: path + ".name", Message: "A resource's name follows the resource name rules (lowercase, digits, hyphens), or is empty for the stack's own name."})
		}
		key := string(m.Kind) + "/" + m.Local
		if seen[key] {
			*ps = append(*ps, Problem{Line: item.Line, Path: path + ".name", Message: "Two resources of one kind can't share a name."})
		}
		seen[key] = true
		if w := f["when"]; w != nil {
			m.When = w.Value
			if !placeholder.MatchString(m.When) || placeholder.FindString(m.When) != m.When {
				*ps = append(*ps, Problem{Line: w.Line, Path: path + ".when", Message: "when is one placeholder, like ${param:domain}."})
			}
		}
		if g := f["generate"]; g != nil {
			n, err := strconv.Atoi(g.Value)
			if err != nil || n < api.GenerateMin || n > api.GenerateMax {
				*ps = append(*ps, Problem{Line: g.Line, Path: path + ".generate", Message: fmt.Sprintf("generate is a length from %d to %d.", api.GenerateMin, api.GenerateMax)})
			}
			m.Generate = n
		}
		if v := f["value"]; v != nil {
			m.Value = v.Value
		}
		switch {
		case m.Kind == api.KindSecret && (m.Generate == 0) == (m.Value == ""):
			*ps = append(*ps, Problem{Line: item.Line, Path: path, Message: "A secret has generate (a random value) or value, one of them."})
		case m.Kind == api.KindSecret && f["spec"] != nil:
			*ps = append(*ps, Problem{Line: f["spec"].Line, Path: path + ".spec", Message: "A secret has generate or value, not a spec."})
		case m.Kind != api.KindSecret && (f["generate"] != nil || f["value"] != nil):
			*ps = append(*ps, Problem{Line: item.Line, Path: path, Message: "generate and value are for secrets."})
		case m.Kind != api.KindSecret:
			if s := f["spec"]; s == nil || s.Kind != yaml.MappingNode {
				*ps = append(*ps, Problem{Line: item.Line, Path: path + ".spec", Message: "Give the resource's spec, as the API takes it."})
			} else if err := s.Decode(&m.Spec); err != nil {
				*ps = append(*ps, Problem{Line: s.Line, Path: path + ".spec", Message: err.Error()})
			}
		}
		out = append(out, m)
	}
	return out
}

// checkPlaceholders finds unknown placeholders and references to parameters or members that don't exist.
func (t *Template) checkPlaceholders() Problems {
	var ps Problems
	params := map[string]bool{}
	for _, p := range t.Parameters {
		params[p.Name] = true
	}
	members := map[string]api.Kind{}
	for _, m := range t.Members {
		members[m.Local] = m.Kind
	}
	check := func(s string, line int, path string, inSecret bool) {
		for _, loc := range placeholder.FindAllStringSubmatchIndex(s, -1) {
			whole := s[loc[0]:loc[1]]
			if whole == "$$" {
				continue
			}
			fn, arg := s[loc[2]:loc[3]], ""
			if loc[4] >= 0 {
				arg = s[loc[4]:loc[5]]
			}
			bad := func(msg string) { ps = append(ps, Problem{Line: line, Path: path, Message: whole + ": " + msg}) }
			switch fn {
			case "stack", "project":
				if loc[4] >= 0 {
					bad("takes nothing after the colon.")
				}
			case "param":
				if !params[arg] {
					bad("there's no parameter " + arg + ".")
				}
			case "name", "ident":
				if _, ok := members[arg]; !ok {
					bad("there's no resource named " + arg + " in the template.")
				}
			case "host":
				kind, name, _ := strings.Cut(arg, "/")
				if k, ok := members[name]; !ok || string(k) != kind {
					bad("there's no " + kind + " named " + name + " in the template.")
				}
			case "password":
				if !inSecret {
					bad("a password only goes into a secret's value.")
				} else if k, ok := members[arg]; !ok || !domain.HasManagedPassword(k) {
					bad("there's no resource named " + arg + " with a managed password.")
				}
			default:
				bad("not a placeholder (use stack, project, param, name, host, ident or password).")
			}
		}
	}
	for i, m := range t.Members {
		path := fmt.Sprintf("resources[%d]", i)
		walk(m.Spec, func(s string) { check(s, m.Line, path+".spec", false) })
		check(m.Value, m.Line, path+".value", true)
		check(m.When, m.Line, path+".when", false)
	}
	for _, k := range t.outputOrder {
		check(t.Outputs[k], 0, "outputs."+k, false)
	}
	return ps
}

// walk calls f with every string in a decoded YAML value.
func walk(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case map[string]any:
		for _, k := range sortedKeys(x) {
			walk(x[k], f)
		}
	case []any:
		for _, e := range x {
			walk(e, f)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// typed checks a parameter value against its type and returns it as JSON would carry it.
func typed(p api.TemplateParameter, v any) (any, error) {
	switch p.Type {
	case api.ParamNumber:
		switch n := v.(type) {
		case int:
			return float64(n), nil
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, fmt.Errorf("%s is a number", p.Name)
			}
			return n, nil
		case string:
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				return f, nil
			}
		}
		return nil, fmt.Errorf("%s is a number", p.Name)
	case api.ParamBoolean:
		switch b := v.(type) {
		case bool:
			return b, nil
		case string:
			if x, err := strconv.ParseBool(b); err == nil {
				return x, nil
			}
		}
		return nil, fmt.Errorf("%s is true or false", p.Name)
	case api.ParamHostname:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s is a host name", p.Name)
		}
		s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
		if s != "" && api.ValidateHostname(s) != nil {
			return nil, fmt.Errorf("%s isn't a host name a domain can have", p.Name)
		}
		return s, nil
	default:
		switch x := v.(type) {
		case string:
			return x, nil
		case float64, int, bool:
			return fmt.Sprint(x), nil
		}
		return nil, fmt.Errorf("%s is text", p.Name)
	}
}

// Entry describes a template for the catalogue.
func (t *Template) Entry(builtIn bool, versions []string) api.CatalogueEntry {
	e := api.CatalogueEntry{Name: t.Name, Title: t.Title, Description: t.Description, Version: t.Version, Versions: versions, BuiltIn: builtIn, Category: t.Category, Signup: t.Signup,
		Parameters: t.Parameters, Members: []api.TemplateMember{}, Outputs: slices.Clone(t.outputOrder)}
	if e.Parameters == nil {
		e.Parameters = []api.TemplateParameter{}
	}
	if e.Outputs == nil {
		e.Outputs = []string{}
	}
	for _, m := range t.Members {
		name := "<stack>"
		if m.Local != "" {
			name += "-" + m.Local
		}
		e.Members = append(e.Members, api.TemplateMember{Kind: m.Kind, Name: name, Optional: m.When != ""})
	}
	return e
}
