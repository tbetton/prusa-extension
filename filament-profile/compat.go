package main

// What PrusaSlicer offers for a printer. A filament preset says which printers
// it fits with `compatible_printers_condition`, an expression over the options
// of the printer preset, or with an explicit `compatible_printers` list. This
// file reads that language and answers the same question PrusaSlicer does, so
// the presets the tab offers are the ones PrusaSlicer's list shows for the
// printer the person chose. It uses nothing outside the standard library.
//
// The language is the part of PrusaSlicer's placeholder parser the bundle
// uses: numbers, "strings", /regular expressions/, options with an optional
// [index], ! and or && ||, == != < > <= >=, =~ !~ and parentheses. A regular
// expression must match the whole text, and `.` matches a line break.

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	errCondition = errors.New("compatibility condition")
	errNoPrinter = errors.New("no such printer preset")
)

type tokenKind int

const (
	tokNum tokenKind = iota
	tokStr
	tokRegex
	tokOp
	tokIdent
)

type token struct {
	kind tokenKind
	text string
}

func isIdentStart(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// quoted reads from the delimiter at start to the next one, keeping the
// backslash escapes as they are, and returns the index after it.
func quoted(rs []rune, start int, delim rune) (int, string, error) {
	var b strings.Builder

	for i := start + 1; i < len(rs); i++ {
		switch {
		case rs[i] == '\\' && i+1 < len(rs):
			b.WriteRune(rs[i])
			b.WriteRune(rs[i+1])

			i++
		case rs[i] == delim:
			return i + 1, b.String(), nil
		default:
			b.WriteRune(rs[i])
		}
	}

	return 0, "", fmt.Errorf("%w: a %c is never closed", errCondition, delim)
}

func lex(src string) ([]token, error) {
	var out []token

	rs := []rune(src)

	for i := 0; i < len(rs); {
		r := rs[i]

		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			i++
		case r == '/':
			next, text, err := quoted(rs, i, '/')
			if err != nil {
				return nil, err
			}

			out = append(out, token{tokRegex, strings.ReplaceAll(text, `\/`, `/`)})
			i = next
		case r == '"':
			next, text, err := quoted(rs, i, '"')
			if err != nil {
				return nil, err
			}

			out = append(out, token{tokStr, text})
			i = next
		case isDigit(r):
			j := i
			for j < len(rs) && (isDigit(rs[j]) || rs[j] == '.') {
				j++
			}

			out = append(out, token{tokNum, string(rs[i:j])})
			i = j
		case isIdentStart(r):
			j := i
			for j < len(rs) && (isIdentStart(rs[j]) || isDigit(rs[j])) {
				j++
			}

			out = append(out, token{tokIdent, string(rs[i:j])})
			i = j
		default:
			if i+1 < len(rs) {
				switch two := string(rs[i : i+2]); two {
				case "=~", "!~", "==", "!=", "<=", ">=", "&&", "||":
					out = append(out, token{tokOp, two})
					i += 2

					continue
				}
			}

			if !strings.ContainsRune("()![]<>", r) {
				return nil, fmt.Errorf("%w: unexpected %q", errCondition, r)
			}

			out = append(out, token{tokOp, string(r)})
			i++
		}
	}

	return out, nil
}

type valueKind int

const (
	vStr valueKind = iota
	vNum
	vBool
	vRegex
)

type value struct {
	kind valueKind
	str  string
	num  float64
	flag bool
}

func boolValue(b bool) value { return value{kind: vBool, flag: b} }

func truth(v value) bool {
	switch v.kind {
	case vBool:
		return v.flag
	case vNum:
		return v.num != 0
	case vRegex:
		return true
	default:
		return v.str != "" && v.str != "0"
	}
}

func text(v value) string {
	switch v.kind {
	case vNum:
		return strconv.FormatFloat(v.num, 'f', -1, 64)
	case vBool:
		if v.flag {
			return "1"
		}

		return "0"
	default:
		return v.str
	}
}

// number reads a value as a number when it is one: a number, a boolean, or a
// text that spells one.
func number(v value) (float64, bool) {
	switch v.kind {
	case vNum:
		return v.num, true
	case vBool:
		if v.flag {
			return 1, true
		}

		return 0, true
	case vStr:
		n, err := strconv.ParseFloat(strings.TrimSpace(v.str), 64)

		return n, err == nil
	default:
		return 0, false
	}
}

// defaults are the values of the options a printer preset may leave out.
//
//nolint:gochecknoglobals // a closed table read on every evaluation, never written.
var defaults = map[string]string{
	"single_extruder_multi_material": "1",
	"nozzle_high_flow":               "0",
	"nozzle_diameter":                "0.4",
	"printer_notes":                  "",
	"printer_model":                  "",
	"printer_variant":                "",
	"printer_technology":             "FFF",
}

func compare(op string, a, b value) (value, error) {
	if op == "=~" || op == "!~" {
		if b.kind != vRegex {
			return value{}, fmt.Errorf("%w: %s needs a /regular expression/", errCondition, op)
		}

		re, err := regexp.Compile("(?s)^(?:" + b.str + ")$")
		if err != nil {
			return value{}, fmt.Errorf("%w: %w", errCondition, err)
		}

		return boolValue(re.MatchString(text(a)) == (op == "=~")), nil
	}

	x, okX := number(a)
	y, okY := number(b)

	var cmp int

	switch {
	case okX && okY:
		switch {
		case x < y:
			cmp = -1
		case x > y:
			cmp = 1
		}
	case !okX && !okY:
		cmp = strings.Compare(text(a), text(b))
	default:
		// A number against a word is never equal, and has no order.
		return boolValue(op == "!="), nil
	}

	switch op {
	case "==":
		return boolValue(cmp == 0), nil
	case "!=":
		return boolValue(cmp != 0), nil
	case "<":
		return boolValue(cmp < 0), nil
	case ">":
		return boolValue(cmp > 0), nil
	case "<=":
		return boolValue(cmp <= 0), nil
	default:
		return boolValue(cmp >= 0), nil
	}
}

type parser struct {
	toks []token
	pos  int
	env  map[string]string
}

func (p *parser) peek() token {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}

	return token{kind: tokOp}
}

func (p *parser) isOp(op string) bool {
	t := p.peek()

	return t.kind == tokOp && t.text == op
}

func (p *parser) isWord(word string) bool {
	t := p.peek()

	return t.kind == tokIdent && t.text == word
}

func (p *parser) or() (value, error) {
	left, err := p.and()
	if err != nil {
		return value{}, err
	}

	for p.isWord("or") || p.isOp("||") {
		p.pos++

		right, err := p.and()
		if err != nil {
			return value{}, err
		}

		left = boolValue(truth(left) || truth(right))
	}

	return left, nil
}

func (p *parser) and() (value, error) {
	left, err := p.cmp()
	if err != nil {
		return value{}, err
	}

	for p.isWord("and") || p.isOp("&&") {
		p.pos++

		right, err := p.cmp()
		if err != nil {
			return value{}, err
		}

		left = boolValue(truth(left) && truth(right))
	}

	return left, nil
}

func (p *parser) cmp() (value, error) {
	left, err := p.unary()
	if err != nil {
		return value{}, err
	}

	t := p.peek()
	if t.kind != tokOp {
		return left, nil
	}

	switch t.text {
	case "==", "!=", "<", ">", "<=", ">=", "=~", "!~":
	default:
		return left, nil
	}

	p.pos++

	right, err := p.unary()
	if err != nil {
		return value{}, err
	}

	return compare(t.text, left, right)
}

func (p *parser) unary() (value, error) {
	if p.isOp("!") {
		p.pos++

		v, err := p.unary()
		if err != nil {
			return value{}, err
		}

		return boolValue(!truth(v)), nil
	}

	return p.atom()
}

func (p *parser) atom() (value, error) {
	if p.pos >= len(p.toks) {
		return value{}, fmt.Errorf("%w: it ends too soon", errCondition)
	}

	t := p.toks[p.pos]
	p.pos++

	switch t.kind {
	case tokNum:
		n, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return value{}, fmt.Errorf("%w: %q is not a number", errCondition, t.text)
		}

		return value{kind: vNum, num: n}, nil
	case tokStr:
		return value{kind: vStr, str: strings.ReplaceAll(t.text, `\"`, `"`)}, nil
	case tokRegex:
		return value{kind: vRegex, str: t.text}, nil
	case tokIdent:
		return p.option(t.text)
	case tokOp:
		if t.text == "(" {
			v, err := p.or()
			if err != nil {
				return value{}, err
			}

			if !p.isOp(")") {
				return value{}, fmt.Errorf("%w: a parenthesis is never closed", errCondition)
			}

			p.pos++

			return v, nil
		}
	}

	return value{}, fmt.Errorf("%w: unexpected %q", errCondition, t.text)
}

// option reads a printer option, with its [index] when it has one.
func (p *parser) option(name string) (value, error) {
	switch name {
	case "true":
		return boolValue(true), nil
	case "false":
		return boolValue(false), nil
	}

	raw, ok := p.env[name]
	if !ok {
		raw, ok = defaults[name]
	}

	if !ok {
		return value{}, fmt.Errorf("%w: %q is not an option it knows", errCondition, name)
	}

	if !p.isOp("[") {
		return value{kind: vStr, str: raw}, nil
	}

	p.pos++

	index := p.peek()
	if index.kind != tokNum {
		return value{}, fmt.Errorf("%w: [ needs an index", errCondition)
	}

	p.pos++

	if !p.isOp("]") {
		return value{}, fmt.Errorf("%w: a bracket is never closed", errCondition)
	}

	p.pos++

	n, err := strconv.Atoi(index.text)
	parts := strings.Split(raw, ",")

	if err != nil || n < 0 || n >= len(parts) {
		return value{}, fmt.Errorf("%w: %s has no element %s", errCondition, name, index.text)
	}

	return value{kind: vStr, str: strings.TrimSpace(parts[n])}, nil
}

// evalCondition evaluates a compatibility condition against the options of a
// printer preset.
func evalCondition(cond string, env map[string]string) (bool, error) {
	toks, err := lex(cond)
	if err != nil {
		return false, err
	}

	p := &parser{toks: toks, env: env}

	v, err := p.or()
	if err != nil {
		return false, err
	}

	if p.pos != len(p.toks) {
		return false, fmt.Errorf("%w: text after the end of the expression", errCondition)
	}

	return truth(v), nil
}

// HasPrinter reports whether the bundle holds a printer preset by this name.
func (c *Catalogue) HasPrinter(name string) bool {
	_, ok := c.printers[name]

	return ok
}

// FlattenPrinter returns the options of a printer preset with its parents
// folded in.
func (c *Catalogue) FlattenPrinter(name string) (keys []string, vals map[string]string, err error) {
	if !c.HasPrinter(name) {
		return nil, nil, fmt.Errorf("%w: %q", errNoPrinter, name)
	}

	vals = map[string]string{}

	if err = c.fold(c.printers, name, 0, &keys, vals); err != nil {
		return nil, nil, err
	}

	return keys, vals, nil
}

// printerEnv is what a condition sees of a printer: its options, its name, and
// the number of extruders its nozzle list implies.
func (c *Catalogue) printerEnv(name string) (map[string]string, error) {
	_, vals, err := c.FlattenPrinter(name)
	if err != nil {
		return nil, err
	}

	vals["printer_notes"] = strings.ReplaceAll(vals["printer_notes"], `\n`, "\n")
	vals["printer_settings_id"] = name
	vals["num_extruders"] = strconv.Itoa(len(strings.Split(vals["nozzle_diameter"], ",")))

	return vals, nil
}

// compatibleWith decides one filament section for a printer. An explicit
// `compatible_printers` list wins over the condition, as in PrusaSlicer.
func (c *Catalogue) compatibleWith(filament, printer string, env map[string]string) (bool, error) {
	_, vals, err := c.Flatten(filament)
	if err != nil {
		return false, err
	}

	if list := strings.TrimSpace(vals["compatible_printers"]); list != "" {
		for _, item := range strings.Split(list, ";") {
			if strings.Trim(strings.TrimSpace(item), `"`) == printer {
				return true, nil
			}
		}

		return false, nil
	}

	cond := strings.TrimSpace(vals["compatible_printers_condition"])
	if cond == "" {
		return true, nil
	}

	return evalCondition(cond, env)
}

// Compatible reports whether PrusaSlicer would offer the filament section for
// the printer preset.
func (c *Catalogue) Compatible(filament, printer string) (bool, error) {
	env, err := c.printerEnv(printer)
	if err != nil {
		return false, err
	}

	return c.compatibleWith(filament, printer, env)
}

// maxCachedPrinters bounds the printers whose answers are kept.
const maxCachedPrinters = 8

// compatibleSet is every filament section that fits a printer. A section whose
// condition cannot be read is left out: better a preset missing than a wrong
// one offered.
func (c *Catalogue) compatibleSet(printer string) (map[string]bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if set, ok := c.cache[printer]; ok {
		return set, nil
	}

	env, err := c.printerEnv(printer)
	if err != nil {
		return nil, err
	}

	set := map[string]bool{}

	for _, name := range c.order {
		if strings.HasPrefix(name, "*") {
			continue
		}

		if ok, err := c.compatibleWith(name, printer, env); err == nil && ok {
			set[name] = true
		}
	}

	if len(c.cache) >= maxCachedPrinters {
		c.cache = map[string]map[string]bool{}
	}

	c.cache[printer] = set

	return set, nil
}

// SectionFor names the section of a base preset that fits the printer: the
// base itself or one of its printer variants. When several fit, the most
// specific, that is the longest name, wins. It is empty when none fits.
func (c *Catalogue) SectionFor(base, printer string) (string, error) {
	set, err := c.compatibleSet(printer)
	if err != nil {
		return "", err
	}

	best := ""

	for _, name := range c.byBase[base] {
		if set[name] && len(name) > len(best) {
			best = name
		}
	}

	return best, nil
}

// Resolution says which section a base preset is written from for a printer.
type Resolution struct {
	// Section is the preset the settings come from.
	Section string
	// Reference, when it is set, is a generic section that fits the printer.
	// Section does not fit as written: its material settings are laid over
	// Reference, which keeps what belongs to the printer.
	Reference string
}

// Resolve decides how a base preset is written for a printer. The section that
// fits as written comes first. Failing that, a source may allow its presets on
// the printers of one family: the preset is then adapted on the generic preset
// of its material type that fits. The zero Resolution means no preset fits.
func (c *Catalogue) Resolve(base, printer string) (Resolution, error) {
	section, err := c.SectionFor(base, printer)
	if err != nil || section != "" {
		return Resolution{Section: section}, err
	}

	env, err := c.printerEnv(printer)
	if err != nil {
		return Resolution{}, err
	}

	for _, name := range c.byBase[base] {
		family := c.retarget[c.sections[name].Source]
		if family == nil || !family.MatchString(env["printer_model"]) {
			continue
		}

		_, vals, err := c.Flatten(name)
		if err != nil {
			continue
		}

		kind := strings.ToUpper(strings.TrimSpace(vals["filament_type"]))
		if kind == "" {
			continue
		}

		reference, err := c.SectionFor("Generic "+kind, printer)
		if err != nil || reference == "" {
			continue
		}

		return Resolution{Section: name, Reference: reference}, nil
	}

	return Resolution{}, nil
}

// Fits reports whether a base preset can be written for the printer.
func (c *Catalogue) Fits(base, printer string) bool {
	res, err := c.Resolve(base, printer)

	return err == nil && res.Section != ""
}

// PrinterInfo describes one printer preset for a list.
type PrinterInfo struct {
	Name     string  `json:"name"`
	Model    string  `json:"model,omitempty"`
	Variant  string  `json:"variant,omitempty"`
	Extruder int     `json:"extruders"`
	Nozzle   float64 `json:"nozzle_diameter,omitempty"`
	HighFlow bool    `json:"high_flow"`
}

// maxPrinters bounds the answer of a printer search.
const maxPrinters = 60

// Printers lists the printer presets whose name holds every word, in name
// order, without the abstract ones.
func (c *Catalogue) Printers(words []string) []PrinterInfo {
	names := make([]string, 0, len(c.printerOrder))

	for _, name := range c.printerOrder {
		if !strings.HasPrefix(name, "*") && containsAll(strings.Join(normalise(name), " "), words) {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	out := []PrinterInfo{}

	for _, name := range names {
		_, vals, err := c.FlattenPrinter(name)
		if err != nil {
			continue
		}

		nozzles := strings.Split(vals["nozzle_diameter"], ",")
		first, _ := strconv.ParseFloat(strings.TrimSpace(nozzles[0]), 64)
		flow := strings.Split(vals["nozzle_high_flow"], ",")

		out = append(out, PrinterInfo{
			Name:     name,
			Model:    vals["printer_model"],
			Variant:  vals["printer_variant"],
			Extruder: len(nozzles),
			Nozzle:   first,
			HighFlow: strings.TrimSpace(flow[0]) == "1",
		})

		if len(out) == maxPrinters {
			break
		}
	}

	return out
}
