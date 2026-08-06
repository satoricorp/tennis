package tennis

import (
	"fmt"
	"regexp"
	"strings"
)

// Filter narrows a query to documents whose attributes match, the way a SQL
// WHERE clause would. Filters are applied before ranking, so a filtered query
// is faster than an unfiltered one rather than slower.
//
// Build them with Eq, NotEq, In, Gt, Gte, Lt, Lte, Glob, And, and Or.
type Filter interface {
	// clause renders SQL against the docs table aliased as d, plus its args.
	clause() (string, []any, error)
}

// attrKeyPattern restricts attribute names to what can be embedded in a JSON
// path literal without quoting games. Attribute names come from callers, and a
// JSON path is a string SQLite parses, so this is the boundary where a hostile
// name would otherwise get interesting.
var attrKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$`)

func attrPath(key string) (string, error) {
	if !attrKeyPattern.MatchString(key) {
		return "", fmt.Errorf("invalid attribute name %q: use letters, digits, underscore, dot and dash", key)
	}
	return "json_extract(d.attrs, '$." + key + "')", nil
}

type cmpFilter struct {
	key string
	op  string
	val any
}

func (f cmpFilter) clause() (string, []any, error) {
	path, err := attrPath(f.key)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%s %s ?", path, f.op), []any{f.val}, nil
}

// Eq matches documents whose attribute equals val.
func Eq(key string, val any) Filter { return cmpFilter{key, "=", val} }

// NotEq matches documents whose attribute differs from val.
func NotEq(key string, val any) Filter { return cmpFilter{key, "!=", val} }

// Gt, Gte, Lt, Lte compare numerically or lexically, following SQLite's rules
// for the stored JSON type.
func Gt(key string, val any) Filter  { return cmpFilter{key, ">", val} }
func Gte(key string, val any) Filter { return cmpFilter{key, ">=", val} }
func Lt(key string, val any) Filter  { return cmpFilter{key, "<", val} }
func Lte(key string, val any) Filter { return cmpFilter{key, "<=", val} }

// Glob matches with SQLite GLOB syntax, where * and ? are the wildcards.
func Glob(key, pattern string) Filter { return cmpFilter{key, "GLOB", pattern} }

type inFilter struct {
	key  string
	vals []any
}

// In matches documents whose attribute is any of vals.
func In(key string, vals ...any) Filter { return inFilter{key, vals} }

func (f inFilter) clause() (string, []any, error) {
	path, err := attrPath(f.key)
	if err != nil {
		return "", nil, err
	}
	if len(f.vals) == 0 {
		// An empty IN matches nothing. Say so explicitly rather than emitting
		// "IN ()", which is a syntax error.
		return "0", nil, nil
	}
	return fmt.Sprintf("%s IN (%s)", path, strings.TrimSuffix(strings.Repeat("?,", len(f.vals)), ",")), f.vals, nil
}

type boolFilter struct {
	op    string
	parts []Filter
}

// And matches documents satisfying every part.
func And(parts ...Filter) Filter { return boolFilter{"AND", parts} }

// Or matches documents satisfying any part.
func Or(parts ...Filter) Filter { return boolFilter{"OR", parts} }

func (f boolFilter) clause() (string, []any, error) {
	if len(f.parts) == 0 {
		return "1", nil, nil
	}
	var (
		clauses []string
		args    []any
	)
	for _, p := range f.parts {
		c, a, err := p.clause()
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, c)
		args = append(args, a...)
	}
	return "(" + strings.Join(clauses, " "+f.op+" ") + ")", args, nil
}
