package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Error is one problem with a document, at path (e.g.
// deployments.grafana.example.com.target_port; "" for the whole document).
type Error struct {
	Path string `json:"path"`
	Msg  string `json:"msg"`
}

func (e Error) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

// Errors is every problem found; Parse and the server's plan return it.
type Errors []Error

func (es Errors) Error() string {
	msgs := make([]string, len(es))
	for i, e := range es {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "\n")
}

func errAt(path, format string, args ...any) Errors {
	return Errors{{Path: path, Msg: fmt.Sprintf(format, args...)}}
}

// List is a list of strings that may end ("..." anywhere, really) with
// the spread marker.
type List struct {
	Items  []string
	Spread bool
}

// NewList is a list of items without the spread marker.
func NewList(items ...string) *List { return &List{Items: append([]string{}, items...)} }

func (l List) MarshalJSON() ([]byte, error) {
	items := append([]string{}, l.Items...)
	if l.Spread {
		items = append(items, Spread)
	}
	return json.Marshal(items)
}

func (l *List) decodePath(raw json.RawMessage, path string) error {
	if isNull(raw) {
		return nil // an empty list
	}
	var vals []json.RawMessage
	if err := json.Unmarshal(raw, &vals); err != nil {
		return errAt(path, "expected a list")
	}
	for i, v := range vals {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return errAt(fmt.Sprintf("%s[%d]", path, i), "expected a string (quote it)")
		}
		if s == Spread {
			l.Spread = true
			continue
		}
		l.Items = append(l.Items, s)
	}
	return nil
}

// Map is a map from a key (a hostname, slug, name or path prefix) to an
// item, in the order the document gives them; "...": true sets Spread.
type Map[T any] struct {
	Keys   []string
	Items  map[string]*T
	Spread bool
}

// Get is the item at key, nil if there's none.
func (m *Map[T]) Get(key string) *T {
	if m == nil {
		return nil
	}
	return m.Items[key]
}

// Set adds or replaces the item at key.
func (m *Map[T]) Set(key string, v *T) {
	if m.Items == nil {
		m.Items = map[string]*T{}
	}
	if _, ok := m.Items[key]; !ok {
		m.Keys = append(m.Keys, key)
	}
	m.Items[key] = v
}

// Sort orders the keys alphabetically.
func (m *Map[T]) Sort() { sort.Strings(m.Keys) }

func (m Map[T]) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	if m.Spread {
		b.WriteString(`"...":true`)
		first = false
	}
	for _, k := range m.Keys {
		if !first {
			b.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(m.Items[k])
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (m *Map[T]) decodePath(raw json.RawMessage, path string) error {
	m.Items = map[string]*T{}
	if isNull(raw) {
		return nil // an empty map
	}
	entries, err := objectEntries(raw)
	if err != nil {
		return errAt(path, "expected a map")
	}
	for _, e := range entries {
		at := join(path, e.key)
		if e.key == Spread {
			if err := json.Unmarshal(e.val, &m.Spread); err != nil {
				return errAt(at, `the spread marker is "...": true`)
			}
			continue
		}
		if strings.TrimSpace(e.key) == "" {
			return errAt(at, "empty key")
		}
		if _, dup := m.Items[e.key]; dup {
			return errAt(at, "listed twice")
		}
		item := new(T)
		if err := decodeStruct(e.val, reflect.ValueOf(item).Elem(), at); err != nil {
			return err
		}
		m.Set(e.key, item)
	}
	return nil
}

// mapItem is a new item of the map's type (the JSON Schema uses it).
func (m *Map[T]) mapItem() any { return new(T) }

func (h *Hash) decodePath(raw json.RawMessage, path string) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return errAt(path, "give the bcrypt hash as {hash: ...}, never the password itself")
	}
	if err := decodeStruct(raw, reflect.ValueOf(h).Elem(), path); err != nil {
		return err
	}
	if h.Hash != "" && !strings.HasPrefix(h.Hash, "$2") {
		return errAt(join(path, "hash"), "not a bcrypt hash (they start with $2)")
	}
	return nil
}

type pathDecoder interface {
	decodePath(raw json.RawMessage, path string) error
}

type entry struct {
	key string
	val json.RawMessage
}

// objectEntries reads a JSON object's members in order.
func objectEntries(raw json.RawMessage) ([]entry, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}
	var out []entry
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out = append(out, entry{key, val})
	}
	return out, nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// decodeStruct fills the struct v from a JSON object, refusing fields it
// doesn't have.
func decodeStruct(raw json.RawMessage, v reflect.Value, path string) error {
	if isNull(raw) {
		return nil
	}
	entries, err := objectEntries(raw)
	if err != nil {
		return errAt(path, "expected a map")
	}
	t := v.Type()
	seen := map[string]bool{}
	for _, e := range entries {
		at := join(path, e.key)
		if seen[e.key] {
			return errAt(at, "listed twice")
		}
		seen[e.key] = true
		idx := -1
		for i := 0; i < t.NumField(); i++ {
			if n := jsonName(t.Field(i)); n != "" && n == e.key {
				idx = i
				break
			}
		}
		if idx < 0 {
			return errAt(at, "unknown field")
		}
		if err := decodeValue(e.val, v.Field(idx), at); err != nil {
			return err
		}
	}
	return nil
}

func decodeValue(raw json.RawMessage, v reflect.Value, path string) error {
	if v.Kind() == reflect.Pointer {
		if isNull(raw) && !v.Type().Implements(reflect.TypeOf((*pathDecoder)(nil)).Elem()) {
			return nil // left out
		}
		p := reflect.New(v.Type().Elem())
		if d, ok := p.Interface().(pathDecoder); ok {
			if err := d.decodePath(raw, path); err != nil {
				return err
			}
		} else if p.Elem().Kind() == reflect.Struct {
			if err := decodeStruct(raw, p.Elem(), path); err != nil {
				return err
			}
		} else if err := decodeScalar(raw, p.Elem(), path); err != nil {
			return err
		}
		v.Set(p)
		return nil
	}
	return decodeScalar(raw, v, path)
}

func decodeScalar(raw json.RawMessage, v reflect.Value, path string) error {
	if err := json.Unmarshal(raw, v.Addr().Interface()); err != nil {
		switch v.Kind() {
		case reflect.String:
			return errAt(path, "expected a string")
		case reflect.Int:
			return errAt(path, "expected a whole number")
		case reflect.Bool:
			return errAt(path, "expected true or false")
		case reflect.Slice:
			return errAt(path, "expected a list of strings")
		}
		return errAt(path, "unexpected value")
	}
	return nil
}

// Parse reads a YAML or JSON document, upgrades it to CurrentVersion and
// checks its shape (not its references: the server does that).
func Parse(data []byte) (*Document, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errAt("", "the document is empty")
	}
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, errAt("", "not valid YAML or JSON: %v", err)
	}
	entries, err := objectEntries(js)
	if err != nil {
		return nil, errAt("", "the document must be a map, starting with version: %d", CurrentVersion)
	}
	version := 0
	for _, e := range entries {
		if e.key == "version" {
			if json.Unmarshal(e.val, &version) != nil {
				return nil, errAt("version", "expected a whole number")
			}
		}
	}
	switch {
	case version == 0:
		return nil, errAt("version", "missing: start the document with version: %d", CurrentVersion)
	case version < MinVersion:
		return nil, errAt("version", "version %d is too old for this server (it reads %d to %d)", version, MinVersion, CurrentVersion)
	case version > CurrentVersion:
		return nil, errAt("version", "version %d is newer than this server reads (%d to %d): update the server", version, MinVersion, CurrentVersion)
	}
	if js, err = upgrade(js, version); err != nil {
		return nil, err
	}
	doc := &Document{}
	if err := decodeStruct(js, reflect.ValueOf(doc).Elem(), ""); err != nil {
		return nil, err
	}
	doc.Version = CurrentVersion
	doc.From = version
	if errs := doc.check(); len(errs) > 0 {
		return nil, errs
	}
	return doc, nil
}

// check finds problems decoding can't: ui naming fields that don't exist,
// and keys that name the same thing twice.
func (d *Document) check() Errors {
	var errs Errors
	checkUI := func(path string, item any, ui []string) {
		fields := Fields(item)
		for _, f := range ui {
			if !slices.Contains(fields, f) {
				errs = append(errs, Error{Path: join(path, "ui"), Msg: fmt.Sprintf("%q isn't a field here (one of %s)", f, strings.Join(fields, ", "))})
			}
		}
	}
	// Keys the server treats as the same name (group names ignore case,
	// hosts and slugs are lowercased) would quietly merge into one.
	unique := func(coll string, keys []string) {
		seen := map[string]string{}
		for _, k := range keys {
			n := strings.ToLower(strings.TrimSpace(k))
			if first, ok := seen[n]; ok {
				errs = append(errs, Error{Path: join(coll, k), Msg: fmt.Sprintf("the same name as %q (names ignore case)", first)})
				continue
			}
			seen[n] = k
		}
	}
	if d.Domains != nil {
		unique("domains", d.Domains.Items)
	}
	if d.Groups != nil {
		for _, g := range []struct {
			kind string
			m    *Map[Group]
		}{{"groups.ip", d.Groups.IP}, {"groups.email", d.Groups.Email}} {
			kind, m := g.kind, g.m
			if m == nil {
				continue
			}
			unique(kind, m.Keys)
			for _, k := range m.Keys {
				checkUI(join(kind, k), m.Items[k], m.Items[k].UI)
			}
		}
	}
	if d.Deployments != nil {
		unique("deployments", d.Deployments.Keys)
		for _, k := range d.Deployments.Keys {
			dep := d.Deployments.Items[k]
			checkUI(join("deployments", k), dep, dep.UI)
			if dep.PathRules != nil {
				for _, p := range dep.PathRules.Keys {
					checkUI(join("deployments", k)+".path_rules."+p, dep.PathRules.Items[p], dep.PathRules.Items[p].UI)
				}
			}
		}
	}
	if d.Bridges != nil {
		unique("bridges", d.Bridges.Keys)
		for _, k := range d.Bridges.Keys {
			checkUI(join("bridges", k), d.Bridges.Items[k], d.Bridges.Items[k].UI)
		}
	}
	return errs
}

// JSON is the document as JSON.
func (d *Document) JSON() ([]byte, error) { return json.MarshalIndent(d, "", "  ") }

// YAML is the document as YAML.
func (d *Document) YAML() ([]byte, error) {
	js, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(js)
}
