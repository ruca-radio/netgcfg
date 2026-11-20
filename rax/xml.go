package rax

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// Metadata is the JSON wrapper's metadata block. It carries everything needed
// to reproduce the container on re-encode, including the opaque 40-byte header
// (whose 32-byte signature we cannot recompute and therefore preserve as-is).
type Metadata struct {
	Format        string `json:"format"`
	Header        string `json:"header"` // hex-encoded 40-byte header
	Model         string `json:"model"`
	Firmware      string `json:"firmware"`
	FirmwareMajor string `json:"firmware_major"`
}

// FormatName is the value of metadata.format in RAX/Broadcom JSON wrappers.
const FormatName = "broadcom-lzw"

// JSON keys used when an XML element has attributes and/or text.
const (
	keyAttrs = "@attrs"
	keyText  = "#text"
)

type wrapper struct {
	Metadata  *Metadata `json:"metadata"`
	Config    any       `json:"config,omitempty"`
	ConfigRaw []byte    `json:"config_raw,omitempty"`
}

// ToJSON serializes the config to a JSON wrapper. When raw is true, the
// decompressed payload is stored verbatim in a base64-encoded "config_raw"
// field (lossless). Otherwise the XML is converted to a JSON object tree.
func (c *Config) ToJSON(raw bool) ([]byte, error) {
	w := wrapper{Metadata: &Metadata{
		Format:        FormatName,
		Header:        hex.EncodeToString(c.header[:]),
		Model:         c.model,
		Firmware:      c.firmware,
		FirmwareMajor: c.firmwareMajor,
	}}

	if raw {
		w.ConfigRaw = c.configXML
	} else {
		node, err := parseConfigXML(c.configXML)
		if err != nil {
			return nil, err
		}
		w.Config = node
	}

	b, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err = json.Indent(&buf, b, "", "    "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// FromJSON parses a JSON wrapper produced by ToJSON.
func FromJSON(data []byte) (*Config, error) {
	w := wrapper{}
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	if w.Metadata == nil {
		return nil, errors.New("'metadata' is required")
	}

	c := &Config{
		model:         w.Metadata.Model,
		firmware:      w.Metadata.Firmware,
		firmwareMajor: w.Metadata.FirmwareMajor,
	}
	header, err := hex.DecodeString(w.Metadata.Header)
	if err != nil || len(header) != headerSize {
		return nil, errors.New("metadata.header must be the hex-encoded 40-byte header")
	}
	copy(c.header[:], header)

	if w.ConfigRaw != nil && w.Config != nil {
		return nil, errors.New("only one of 'config' and 'config_raw' may be set")
	}
	if w.ConfigRaw != nil {
		c.configXML = w.ConfigRaw
		return c, nil
	}
	if w.Config != nil {
		if err := validateConfigNode(w.Config, "$"); err != nil {
			return nil, fmt.Errorf("invalid config: %w", err)
		}
		xmlBytes, err := buildConfigXML(w.Config)
		if err != nil {
			return nil, err
		}
		// Self-check: the regenerated XML must parse back into a stable tree.
		if _, err := parseConfigXML(xmlBytes); err != nil {
			return nil, fmt.Errorf("config does not round-trip through XML: %w", err)
		}
		c.configXML = xmlBytes
		return c, nil
	}
	return nil, errors.New("'config' or 'config_raw' is required")
}

// ---- XML <-> JSON -------------------------------------------------------

// xmlNode is an intermediate representation of an XML element.
type xmlNode struct {
	name     string
	attrs    []xml.Attr
	text     []byte
	children []*xmlNode
}

// parseXML decodes data into an element tree.
func parseXML(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var root *xmlNode
	var stack []*xmlNode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := &xmlNode{name: t.Name.Local, attrs: t.Attr}
			if len(stack) == 0 {
				root = el
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, el)
			}
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text = append(stack[len(stack)-1].text, t...)
			}
		}
	}
	if root == nil {
		return nil, errors.New("no root element in config XML")
	}
	return root, nil
}

// parseConfigXML converts the decompressed payload into a JSON object whose
// single key is the XML root element's name.
func parseConfigXML(payload []byte) (any, error) {
	trimmed := bytes.TrimRight(payload, "\x00")
	trimmed = bytes.TrimSpace(trimmed)
	if len(trimmed) == 0 {
		return nil, errors.New("config payload is empty")
	}
	root, err := parseXML(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse config XML: %w", err)
	}
	return map[string]any{root.name: elementToJSON(root)}, nil
}

// elementToJSON converts an XML element into its JSON representation.
//
//   - a leaf element (text, no children, no attributes)  -> string
//   - a leaf element with attributes                      -> {"@attrs": {...}, "#text": "..."}
//   - a container element                                 -> {"@attrs"?: {...}, "<child>": ...}
//
// Repeated child element names map to arrays.
func elementToJSON(n *xmlNode) any {
	hasChildren := len(n.children) > 0
	hasAttrs := len(n.attrs) > 0

	if hasChildren {
		m := make(map[string]any)
		if hasAttrs {
			m[keyAttrs] = attrsToMap(n.attrs)
		}
		for _, ch := range n.children {
			v := elementToJSON(ch)
			if existing, ok := m[ch.name]; ok {
				if arr, isArr := existing.([]any); isArr {
					m[ch.name] = append(arr, v)
				} else {
					m[ch.name] = []any{existing, v}
				}
			} else {
				m[ch.name] = v
			}
		}
		return m
	}

	if hasAttrs {
		return map[string]any{
			keyAttrs: attrsToMap(n.attrs),
			keyText:  string(n.text),
		}
	}
	return string(n.text)
}

func attrsToMap(attrs []xml.Attr) map[string]any {
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		m[a.Name.Local] = a.Value
	}
	return m
}

// validateConfigNode checks the shape of a parsed config tree before it is
// turned back into XML. It exists so that a hand-edited wrapper that is valid
// JSON but structurally wrong fails loudly with a useful message, rather than
// silently producing a .cfg the router will reject.
func validateConfigNode(node any, path string) error {
	switch v := node.(type) {
	case string:
		return nil
	case []any:
		if len(v) == 0 {
			return fmt.Errorf("%s: empty array (an XML element must have at least one occurrence)", path)
		}
		for i, item := range v {
			switch item.(type) {
			case string, map[string]any:
				if err := validateConfigNode(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s[%d]: unsupported value type %T (want string or object)", path, i, item)
			}
		}
		return nil
	case map[string]any:
		for k, child := range v {
			switch k {
			case keyAttrs:
				attrs, ok := child.(map[string]any)
				if !ok {
					return fmt.Errorf("%s: %q must be an object", path, keyAttrs)
				}
				for an, av := range attrs {
					if s, ok := av.(string); !ok {
						return fmt.Errorf("%s.%s.%s: attribute value must be a string, got %T", path, keyAttrs, an, av)
					} else if !validXMLName(an) {
						return fmt.Errorf("%s.%s: invalid attribute name %q", path, keyAttrs, an)
					} else if !utf8.ValidString(s) {
						return fmt.Errorf("%s.%s.%s: attribute value is not valid UTF-8", path, keyAttrs, an)
					}
				}
			case keyText:
				if _, ok := child.(string); !ok {
					return fmt.Errorf("%s: %q must be a string", path, keyText)
				}
			default:
				if !validXMLName(k) {
					return fmt.Errorf("%s: invalid element name %q", path, k)
				}
				if err := validateConfigNode(child, path+"/"+k); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("%s: unsupported value type %T (want string, object, or array)", path, node)
	}
}

// validXMLName reports whether s is a plausible XML element/attribute name.
func validXMLName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r == ':' || r == '.' || r == '-':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false // XML names cannot start with a digit
			}
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return true
}

// buildConfigXML converts a JSON object (whose single key is the root element
// name) back into an XML payload, terminated by the NUL byte these devices
// expect.
func buildConfigXML(node any) ([]byte, error) {
	rm, ok := node.(map[string]any)
	if !ok || len(rm) != 1 {
		return nil, errors.New("config must be a single-element object keyed by the XML root name")
	}
	var rootName string
	var rootVal any
	for name, val := range rm {
		rootName, rootVal = name, val
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>`)
	b.WriteByte('\n')
	if err := writeElement(&b, rootName, rootVal, 0); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	b.WriteByte(0) // NUL terminator
	return []byte(b.String()), nil
}

func writeElement(b *strings.Builder, name string, node any, indent int) error {
	switch v := node.(type) {
	case string:
		writeIndent(b, indent)
		b.WriteByte('<')
		b.WriteString(name)
		b.WriteByte('>')
		b.WriteString(escapeXML(v))
		b.WriteString("</")
		b.WriteString(name)
		b.WriteString(">\n")
		return nil

	case []any:
		for _, item := range v {
			if err := writeElement(b, name, item, indent); err != nil {
				return err
			}
		}
		return nil

	case map[string]any:
		var attrs map[string]any
		if a, ok := v[keyAttrs].(map[string]any); ok {
			attrs = a
		}
		text, hasText := v[keyText].(string)

		hasChildren := false
		for k := range v {
			if k != keyAttrs && k != keyText {
				hasChildren = true
				break
			}
		}

		if !hasChildren {
			writeIndent(b, indent)
			b.WriteByte('<')
			b.WriteString(name)
			writeAttrs(b, attrs)
			if hasText {
				b.WriteByte('>')
				b.WriteString(escapeXML(text))
				b.WriteString("</")
				b.WriteString(name)
				b.WriteString(">\n")
			} else {
				b.WriteString("/>\n")
			}
			return nil
		}

		writeIndent(b, indent)
		b.WriteByte('<')
		b.WriteString(name)
		writeAttrs(b, attrs)
		b.WriteString(">\n")

		keys := make([]string, 0, len(v))
		for k := range v {
			if k != keyAttrs && k != keyText {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := writeElement(b, k, v[k], indent+1); err != nil {
				return err
			}
		}

		writeIndent(b, indent)
		b.WriteString("</")
		b.WriteString(name)
		b.WriteString(">\n")
		return nil
	}
	return fmt.Errorf("unsupported JSON value for element %q: %T", name, node)
}

func writeAttrs(b *strings.Builder, attrs map[string]any) {
	if len(attrs) == 0 {
		return
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeXML(fmt.Sprint(attrs[k])))
		b.WriteByte('"')
	}
}

func writeIndent(b *strings.Builder, indent int) {
	for i := 0; i < indent; i++ {
		b.WriteString("  ")
	}
}

// escapeXML escapes a string for use as XML text or an attribute value.
func escapeXML(s string) string {
	if !strings.ContainsAny(s, `&<>"'`) {
		return s
	}
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
