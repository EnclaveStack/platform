package catalogue

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

// Rendered is a template filled in for one stack.
type Rendered struct {
	Members []RenderedMember
	Outputs map[string]string
	// SecretParams are the secrets the stack makes for its secret parameters: name → value.
	SecretParams map[string]string
}

// RenderedMember is one resource to create, as the API takes it.
type RenderedMember struct {
	Kind api.Kind
	Name string // the full name: the stack's, or "<stack>-<local>"
	// Spec is the API spec; for a secret, the value is in Value (Generate for a random one), which may
	// still hold ${password:…} placeholders for FillPasswords.
	Spec     json.RawMessage
	Generate int
	Value    string
}

// Render fills the template in for a stack in a project, with parameter values and secret parameters.
func (t *Template) Render(stack, project string, values map[string]any, secrets map[string]string) (Rendered, error) {
	var ps Problems
	params := map[string]any{}
	types := map[string]string{}
	// unset are the secret parameters with no secret: outputs that use one are left out (CNX-05).
	unset := map[string]bool{}
	out := Rendered{Outputs: map[string]string{}, SecretParams: map[string]string{}}
	for _, p := range t.Parameters {
		types[p.Name] = p.Type
		if p.Type == api.ParamSecret {
			v, given := secrets[p.Name]
			if !given && p.Required {
				ps = append(ps, Problem{Path: "secretParameters." + p.Name, Message: "Give " + p.Title + "."})
			}
			name := stack + "-" + strings.ToLower(p.Name)
			params[p.Name] = name
			if given {
				out.SecretParams[name] = v
			} else {
				unset[p.Name] = true
			}
			continue
		}
		v, given := values[p.Name]
		if !given || v == nil {
			if p.Default != nil {
				v, given = p.Default, true
			}
		}
		if !given {
			if p.Required {
				ps = append(ps, Problem{Path: "parameters." + p.Name, Message: "Give " + p.Title + "."})
			}
			params[p.Name] = zero(p.Type)
			continue
		}
		tv, err := typed(p, v)
		if err != nil {
			ps = append(ps, Problem{Path: "parameters." + p.Name, Message: err.Error() + "."})
			continue
		}
		params[p.Name] = tv
	}
	for k := range values {
		if _, ok := types[k]; !ok || types[k] == api.ParamSecret {
			ps = append(ps, Problem{Path: "parameters." + k, Message: "The template has no parameter " + k + "."})
		}
	}
	for k := range secrets {
		if types[k] != api.ParamSecret {
			ps = append(ps, Problem{Path: "secretParameters." + k, Message: "The template has no secret parameter " + k + "."})
		}
	}
	if len(ps) > 0 {
		return Rendered{}, ps
	}
	full := func(local string) string {
		if local == "" {
			return stack
		}
		return stack + "-" + local
	}
	included := map[string]bool{}
	for _, m := range t.Members {
		included[m.Local] = m.When == "" || truthy(resolve(m.When, stack, project, params, t, full))
	}
	for i, m := range t.Members {
		if !included[m.Local] {
			continue
		}
		path := fmt.Sprintf("resources[%d]", i)
		name := full(m.Local)
		if e := api.ValidateName(name); e != nil {
			ps = append(ps, Problem{Line: m.Line, Path: path + ".name", Message: fmt.Sprintf("%s is too long a name for the stack %s: %s", name, stack, e[0].Message)})
			continue
		}
		var leftOut []string
		check := func(s string) {
			for _, ref := range refsIn(s) {
				if !included[ref] {
					leftOut = append(leftOut, ref)
				}
			}
		}
		walk(m.Spec, check)
		check(m.Value)
		if len(leftOut) > 0 {
			ps = append(ps, Problem{Line: m.Line, Path: path, Message: "it uses " + strings.Join(leftOut, ", ") + ", which is left out with these parameters."})
			continue
		}
		rm := RenderedMember{Kind: m.Kind, Name: name, Generate: m.Generate}
		if m.Kind == api.KindSecret {
			rm.Value = substituteKeepingPasswords(m.Value, stack, project, params, t, full)
		} else {
			b, err := json.Marshal(renderValue(m.Spec, stack, project, params, t, full))
			if err != nil {
				ps = append(ps, Problem{Line: m.Line, Path: path + ".spec", Message: err.Error()})
				continue
			}
			rm.Spec = b
		}
		out.Members = append(out.Members, rm)
	}
	for _, k := range t.outputOrder {
		if usesUnset(t.Outputs[k], unset) {
			continue
		}
		out.Outputs[k] = fmt.Sprint(resolve(t.Outputs[k], stack, project, params, t, full))
	}
	if len(ps) > 0 {
		return Rendered{}, ps
	}
	return out, nil
}

// usesUnset reports whether s names one of the unset secret parameters.
func usesUnset(s string, unset map[string]bool) bool {
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		if m[1] == "param" && unset[m[2]] {
			return true
		}
	}
	return false
}

func zero(typ string) any {
	switch typ {
	case api.ParamNumber:
		return float64(0)
	case api.ParamBoolean:
		return false
	}
	return ""
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false"
	case float64:
		return x != 0
	}
	return v != nil
}

// refsIn lists the template members a string names (name, host, ident and password placeholders).
func refsIn(s string) []string {
	var out []string
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		switch m[1] {
		case "name", "ident", "password":
			out = append(out, m[2])
		case "host":
			_, name, _ := strings.Cut(m[2], "/")
			out = append(out, name)
		}
	}
	return out
}

// renderValue fills placeholders in a decoded spec.
func renderValue(v any, stack, project string, params map[string]any, t *Template, full func(string) string) any {
	switch x := v.(type) {
	case string:
		return resolve(x, stack, project, params, t, full)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = renderValue(e, stack, project, params, t, full)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = renderValue(e, stack, project, params, t, full)
		}
		return out
	}
	return v
}

// resolve fills a string; one that is exactly one parameter placeholder takes the parameter's type.
func resolve(s, stack, project string, params map[string]any, t *Template, full func(string) string) any {
	if m := placeholder.FindStringSubmatch(s); m != nil && m[0] == s && m[1] == "param" {
		return params[m[2]]
	}
	return placeholder.ReplaceAllStringFunc(s, func(whole string) string {
		return one(whole, stack, project, params, t, full)
	})
}

// substituteKeepingPasswords fills a secret's value but leaves ${password:…} for FillPasswords.
func substituteKeepingPasswords(s, stack, project string, params map[string]any, t *Template, full func(string) string) string {
	return placeholder.ReplaceAllStringFunc(s, func(whole string) string {
		if strings.HasPrefix(whole, "${password:") {
			return whole
		}
		return one(whole, stack, project, params, t, full)
	})
}

func one(whole, stack, project string, params map[string]any, t *Template, full func(string) string) string {
	if whole == "$$" {
		return "$"
	}
	m := placeholder.FindStringSubmatch(whole)
	switch m[1] {
	case "stack":
		return stack
	case "project":
		return project
	case "param":
		return fmt.Sprint(params[m[2]])
	case "name":
		return full(m[2])
	case "ident":
		return domain.DatabaseIdentifier(full(m[2]))
	case "host":
		kind, name, _ := strings.Cut(m[2], "/")
		return "es-" + kind + "-" + domain.Qualified(project, full(name))
	}
	return whole
}

// FillPasswords puts members' managed passwords into a secret's value: ${password:db} becomes the password
// the database db was given. password returns it by the member's local name.
func FillPasswords(value string, password func(local string) (string, bool)) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(value, func(whole string) string {
		m := placeholder.FindStringSubmatch(whole)
		if m == nil || m[1] != "password" {
			return whole
		}
		pw, ok := password(m[2])
		if !ok {
			missing = append(missing, m[2])
			return whole
		}
		return pw
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("no password for %s: list it before the secret that uses it", strings.Join(missing, ", "))
	}
	return out, nil
}

// Generate makes a random value of n characters from letters and digits.
func Generate(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[k.Int64()]
	}
	return string(b)
}
